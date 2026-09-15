#!/usr/bin/env bash
# 平台面端到端（分支日志验证项 V4）
#
# 链路：bootstrap 首个 staff → set-password → login → 建 developer 账号 → 建项目/产品并挂载
#       → 签发项目凭证 → 平台以该凭证签名调用 gateway（OTA 固件目录）→ admin 审计可见
#
# 组成（全部为真实二进制 / 真实依赖）：
#   - cmd/explorex-platform-api      真实平台 HTTP 面（Postgres + Redis + KEK）
#   - cmd/explorex-platform-bootstrap 首个 staff 账号
#   - cmd/platform-plane-smoke       最小 gateway 内部 API（真实 HMAC 验签 + DB 凭证 + 项目 scope + OTA 只读）
#   - cmd/aws-iot-gateway-db-seed    迁移与 DP/产品 seed
#
# 用法：
#   DATABASE_URL='postgres://gateway:gateway-local-only@127.0.0.1:5432/aws_iot_gateway?sslmode=disable' \
#   REDIS_URL='redis://127.0.0.1:6379/2' \
#   test/tool/platform_plane_e2e.sh
#
# 前置：本机 PostgreSQL 与 Redis（可用 deploy/local/compose.yaml 起），Go 工具链与 openssl。
# **不需要** AWS 或国内 provider 凭据。
# 隔离：全部对象建在一次性 schema（SMOKE_SCHEMA）里，结束时 DROP ... CASCADE；
#       不读写既有的 public 数据。KEEP_SCHEMA=1 / KEEP_TMP=1 可保留现场排查。
set -euo pipefail

REPO_ROOT=$(git -C "$(dirname -- "$0")" rev-parse --show-toplevel)

BASE_DATABASE_URL=${DATABASE_URL:?DATABASE_URL is required (local PostgreSQL)}
REDIS_URL=${REDIS_URL:-redis://127.0.0.1:6379/2}
GATEWAY_PORT=${PLATFORM_E2E_GATEWAY_PORT:-14080}
PLATFORM_PORT=${PLATFORM_E2E_PLATFORM_PORT:-14081}

TMP_DIR=$(mktemp -d)
SCHEMA="platform_e2e_$(date +%s)_$$"
KEK=$(openssl rand -base64 32)
SUFFIX="$(date +%s)$$"
E2E_PID="e2e-pid-$SUFFIX"
E2E_PID_TWO="e2e-pid-two-$SUFFIX"
STAFF_EMAIL="e2e-staff-$SUFFIX@example.com"
DEV_EMAIL="e2e-dev-$SUFFIX@example.com"
STAFF_PASSWORD="e2e staff passphrase $SUFFIX"
DEV_PASSWORD="e2e developer passphrase $SUFFIX"

log() { printf '%s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

with_schema() {
  case "$1" in
    *\?*) printf '%s&search_path=%s' "$1" "$2" ;;
    *) printf '%s?search_path=%s' "$1" "$2" ;;
  esac
}

DATABASE_URL="$(with_schema "$BASE_DATABASE_URL" "$SCHEMA")"
export DATABASE_URL REDIS_URL GATEWAY_ENVIRONMENT=development
export PLATFORM_CREDENTIAL_KEK="$KEK"
export SMOKE_SCHEMA="$SCHEMA"

SEED_BIN="$TMP_DIR/aws-iot-gateway-db-seed"
SMOKE_BIN="$TMP_DIR/platform-plane-smoke"
PLATFORM_BIN="$TMP_DIR/explorex-platform-api"
BOOTSTRAP_BIN="$TMP_DIR/explorex-platform-bootstrap"

cleanup() {
  local status=$?
  if (( status != 0 )); then
    printf '\n--- gateway.log (tail) ---\n' >&2
    tail -n 30 "$TMP_DIR/gateway.log" >&2 2>/dev/null || true
    printf '\n--- platform.log (tail) ---\n' >&2
    tail -n 30 "$TMP_DIR/platform.log" >&2 2>/dev/null || true
  fi
  [[ -n "${PLATFORM_PID:-}" ]] && kill "$PLATFORM_PID" 2>/dev/null || true
  [[ -n "${GATEWAY_PID:-}" ]] && kill "$GATEWAY_PID" 2>/dev/null || true
  wait 2>/dev/null || true
  if [[ "${KEEP_SCHEMA:-0}" != "1" && -x "$SMOKE_BIN" ]]; then
    SMOKE_SCHEMA="$SCHEMA" DATABASE_URL="$BASE_DATABASE_URL" "$SMOKE_BIN" -mode=drop >/dev/null 2>&1 || true
  fi
  [[ "${KEEP_TMP:-0}" == "1" ]] || rm -rf "$TMP_DIR"
  return $status
}
trap cleanup EXIT

request() { # method url token body
  local method=$1 url=$2 token=${3:-} body=${4:-}
  local args=(-sS -o "$TMP_DIR/body.json" -w '%{http_code}' -X "$method" "$url")
  [[ -n "$token" ]] && args+=(-H "Authorization: Bearer $token")
  [[ -n "$body" ]] && args+=(-H 'Content-Type: application/json' --data "$body")
  HTTP_STATUS=$(curl "${args[@]}")
  HTTP_BODY=$(cat "$TMP_DIR/body.json")
}

expect() { # name want method url [token] [body]
  local name=$1 want=$2
  request "$3" "$4" "${5:-}" "${6:-}"
  [[ "$HTTP_STATUS" == "$want" ]] || fail "$name: status=$HTTP_STATUS want=$want body=$HTTP_BODY"
  log "ok   $name ($HTTP_STATUS)"
}

wait_for() { # url name
  for _ in $(seq 1 80); do
    if curl -fsS --max-time 2 "$1" >/dev/null 2>&1; then log "ok   $2 ready"; return 0; fi
    sleep 0.5
  done
  fail "$2 did not become ready at $1"
}

log "== build =="
(cd "$REPO_ROOT" &&
  go build -o "$SEED_BIN" ./cmd/aws-iot-gateway-db-seed &&
  go build -o "$SMOKE_BIN" ./cmd/platform-plane-smoke &&
  go build -o "$PLATFORM_BIN" ./cmd/explorex-platform-api &&
  go build -o "$BOOTSTRAP_BIN" ./cmd/explorex-platform-bootstrap)

log "== prepare isolated schema $SCHEMA =="
"$SMOKE_BIN" -mode=prepare
"$SEED_BIN" >/dev/null

log "== start gateway openapi surface (:$GATEWAY_PORT) and platform api (:$PLATFORM_PORT) =="
SMOKE_SCHEMA="$SCHEMA" SMOKE_LISTEN_ADDRESS="127.0.0.1:$GATEWAY_PORT" "$SMOKE_BIN" >"$TMP_DIR/gateway.log" 2>&1 &
GATEWAY_PID=$!
PLATFORM_LISTEN_ADDRESS="127.0.0.1:$PLATFORM_PORT" \
PLATFORM_GATEWAY_INTERNAL_BASE_URL="http://127.0.0.1:$GATEWAY_PORT" \
PLATFORM_CORS_ALLOWED_ORIGINS="http://127.0.0.1:$PLATFORM_PORT" \
  "$PLATFORM_BIN" >"$TMP_DIR/platform.log" 2>&1 &
PLATFORM_PID=$!
wait_for "http://127.0.0.1:$GATEWAY_PORT/healthz" "gateway openapi"
wait_for "http://127.0.0.1:$PLATFORM_PORT/healthz" "platform api"

BASE="http://127.0.0.1:$PLATFORM_PORT/api/v1"
log "== staff 账号 =="
PLATFORM_BOOTSTRAP_STAFF_EMAIL="$STAFF_EMAIL" PLATFORM_BOOTSTRAP_STAFF_NAME="E2E Staff" "$BOOTSTRAP_BIN" >"$TMP_DIR/bootstrap.out" 2>&1 ||
  fail "bootstrap staff failed: $(cat "$TMP_DIR/bootstrap.out")"
SETUP_TOKEN=$(sed -n 's/^setup token.*: //p' "$TMP_DIR/bootstrap.out")
[[ -n "$SETUP_TOKEN" ]] || fail "bootstrap printed no setup token"
expect "staff set-password" 200 POST "$BASE/admin/auth/set-password" "" "{\"token\":\"$SETUP_TOKEN\",\"new_password\":\"$STAFF_PASSWORD\"}"
expect "staff login" 200 POST "$BASE/admin/auth/login" "" "{\"email\":\"$STAFF_EMAIL\",\"password\":\"$STAFF_PASSWORD\"}"
STAFF_TOKEN=$(jq -r .token <<<"$HTTP_BODY")
[[ "$STAFF_TOKEN" != "null" && -n "$STAFF_TOKEN" ]] || fail "staff login returned no token"

log "== developer 账号（由 staff 创建 + 一次性邀请 token） =="
expect "create developer account" 201 POST "$BASE/admin/accounts" "$STAFF_TOKEN" \
  "{\"kind\":\"developer\",\"role\":\"developer\",\"email\":\"$DEV_EMAIL\",\"display_name\":\"E2E Developer\"}"
DEVELOPER_ID=$(jq -r .account.id <<<"$HTTP_BODY")
DEVELOPER_SETUP=$(jq -r .setup_token <<<"$HTTP_BODY")
expect "developer set-password" 200 POST "$BASE/developer/auth/set-password" "" "{\"token\":\"$DEVELOPER_SETUP\",\"new_password\":\"$DEV_PASSWORD\"}"
expect "developer login" 200 POST "$BASE/developer/auth/login" "" "{\"email\":\"$DEV_EMAIL\",\"password\":\"$DEV_PASSWORD\"}"
DEVELOPER_TOKEN=$(jq -r .token <<<"$HTTP_BODY")

log "== 项目 / 产品 / 凭证 =="
expect "create project" 201 POST "$BASE/developer/projects" "$DEVELOPER_TOKEN" '{"project_type":"cloud","name":"E2E Project"}'
PROJECT_ID=$(jq -r .project.id <<<"$HTTP_BODY")
expect "create product" 201 POST "$BASE/developer/products" "$DEVELOPER_TOKEN" \
  "{\"pid\":\"$E2E_PID\",\"name\":\"E2E Product\",\"dp_profile_code\":\"feeder-v1\"}"
PRODUCT_ID=$(jq -r .product.id <<<"$HTTP_BODY")
expect "attach product to project" 200 POST "$BASE/developer/projects/$PROJECT_ID/products" "$DEVELOPER_TOKEN" "{\"product_id\":\"$PRODUCT_ID\"}"
expect "issue project credential" 201 POST "$BASE/developer/projects/$PROJECT_ID/credentials" "$DEVELOPER_TOKEN" \
  '{"name":"e2e","permissions":["devices:read","ota:deploy"]}'
CREDENTIAL_KEY=$(jq -r .credential.api_key <<<"$HTTP_BODY")
CREDENTIAL_SECRET=$(jq -r .api_secret <<<"$HTTP_BODY")
[[ "$CREDENTIAL_KEY" != "null" && -n "$CREDENTIAL_SECRET" && "$CREDENTIAL_SECRET" != "null" ]] || fail "credential secret was not returned once"

log "== 平台以项目凭证签名调用 gateway（OTA 固件目录） =="
SMOKE_OTA_PID="$E2E_PID" SMOKE_OTA_PROJECT="$PROJECT_ID" "$SMOKE_BIN" -mode=seed-ota
expect "OTA catalog through gateway proxy" 200 GET "$BASE/developer/ota/firmware-versions?pid=$E2E_PID" "$DEVELOPER_TOKEN" ""
[[ "$(jq -r '.firmware_versions | length' <<<"$HTTP_BODY")" == "1" ]] ||
  fail "expected exactly the project's firmware version, got: $HTTP_BODY"
[[ "$(jq -r '.firmware_versions[0].version' <<<"$HTTP_BODY")" == "9.9.9" ]] || fail "unexpected firmware version body: $HTTP_BODY"

log "== 收窄与降级 =="
expect "out-of-scope pid rejected" 403 GET "$BASE/developer/ota/firmware-versions?pid=feeder-x1" "$DEVELOPER_TOKEN" ""
expect "missing pid rejected" 400 GET "$BASE/developer/ota/firmware-versions" "$DEVELOPER_TOKEN" ""
expect "create second project" 201 POST "$BASE/developer/projects" "$DEVELOPER_TOKEN" '{"project_type":"cloud","name":"E2E Project Two"}'
PROJECT_TWO=$(jq -r .project.id <<<"$HTTP_BODY")
expect "create second product" 201 POST "$BASE/developer/products" "$DEVELOPER_TOKEN" \
  "{\"pid\":\"$E2E_PID_TWO\",\"name\":\"E2E Product Two\",\"dp_profile_code\":\"feeder-v1\"}"
PRODUCT_TWO=$(jq -r .product.id <<<"$HTTP_BODY")
expect "attach second product" 200 POST "$BASE/developer/projects/$PROJECT_TWO/products" "$DEVELOPER_TOKEN" "{\"product_id\":\"$PRODUCT_TWO\"}"
expect "second credential without ota:deploy" 201 POST "$BASE/developer/projects/$PROJECT_TWO/credentials" "$DEVELOPER_TOKEN" '{"name":"read-only","permissions":["devices:read"]}'
expect "OTA without ota:deploy is a conflict" 409 GET "$BASE/developer/ota/firmware-versions?pid=$E2E_PID_TWO" "$DEVELOPER_TOKEN" ""

log "== 审计 =="
expect "admin audit trail" 200 GET "$BASE/admin/audit-logs?actor_account_id=$DEVELOPER_ID&limit=100" "$STAFF_TOKEN" ""
AUDIT_COUNT=$(jq -r '.items | length' <<<"$HTTP_BODY")
(( AUDIT_COUNT >= 5 )) || fail "expected at least 5 audit entries for the developer, got $AUDIT_COUNT"
if jq -e --arg secret "$CREDENTIAL_SECRET" 'tostring | contains($secret)' <<<"$HTTP_BODY" >/dev/null; then
  fail "audit log leaked the credential secret"
fi
log "ok   audit entries=$AUDIT_COUNT, no credential secret in audit"

log "== PASS：平台面端到端全链路通过（schema ${SCHEMA}） =="
