#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT=$(git -C "$(dirname -- "$0")" rev-parse --show-toplevel)
DEV_SH="$REPO_ROOT/tool/dev.sh"
FIXTURE_BIN="$REPO_ROOT/test/tool/fixtures/bin"
TMP_DIR=$(mktemp -d)
ENVIRONMENT_ID="launcher-contract-$$"
ENV_FILE="$REPO_ROOT/.env.$ENVIRONMENT_ID"
CAPTURE_FILE="$TMP_DIR/capture.log"
SECRET_MARKER="launcher-contract-secret-value"

cleanup() {
  rm -f "$ENV_FILE"
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

fail() {
  echo "$*" >&2
  exit 1
}

test -x "$DEV_SH" || fail "missing executable Gateway local-dev launcher: $DEV_SH"
test "$($DEV_SH contract-version)" = 1

cat >"$ENV_FILE" <<EOF
GATEWAY_ENVIRONMENT=development
GATEWAY_LOCAL_POSTGRES_DATABASE=aws_iot_gateway
GATEWAY_LOCAL_POSTGRES_PORT=5432
GATEWAY_LOCAL_REDIS_PORT=6379
DATABASE_URL=postgres://gateway:local-only@127.0.0.1:5432/aws_iot_gateway?sslmode=disable
REDIS_URL=redis://127.0.0.1:6379/2
INTERNAL_APPS_JSON='[{"id":"demo-local","api_key":"demo-key","api_secret":"$SECRET_MARKER"}]'
EOF
chmod 600 "$ENV_FILE"
git -C "$REPO_ROOT" check-ignore -q ".env.$ENVIRONMENT_ID"
cp "$ENV_FILE" "$TMP_DIR/valid.env"

export PATH="$FIXTURE_BIN:$PATH"
export GATEWAY_DEV_BIN_DIR="$TMP_DIR/bin"
export GATEWAY_FAKE_CAPTURE="$CAPTURE_FILE"
export GATEWAY_FAKE_LISTEN_PORTS="5432,6379"
export GATEWAY_FAKE_HEALTHY=1

status_output=$($DEV_SH status --environment-id "$ENVIRONMENT_ID")
grep -q "env=$ENVIRONMENT_ID file=present ignored=yes" <<<"$status_output"
grep -q 'tcp:5432 listening' <<<"$status_output"
grep -q 'tcp:6379 listening' <<<"$status_output"
grep -q 'tcp:14080 stopped' <<<"$status_output"
grep -q 'gateway_health=ok' <<<"$status_output"

if $DEV_SH status --environment-id '../escape' >"$TMP_DIR/invalid.out" 2>&1; then
  fail "invalid environment id must be rejected"
fi
grep -q 'invalid environment id' "$TMP_DIR/invalid.out"

: >"$CAPTURE_FILE"
up_output=$($DEV_SH up --environment-id "$ENVIRONMENT_ID" 2>&1)
if grep -Fq "$SECRET_MARKER" <<<"$up_output" || grep -Fq "$SECRET_MARKER" "$CAPTURE_FILE"; then
  fail "Gateway launcher leaked INTERNAL_APPS_JSON secret"
fi
test "$(wc -l <"$CAPTURE_FILE" | tr -d ' ')" = 4 || {
  echo "unexpected reuse lifecycle:" >&2
  cat "$CAPTURE_FILE" >&2
  exit 1
}
test "$(sed -n '1p' "$CAPTURE_FILE")" = "go build -o $TMP_DIR/bin/aws-iot-gateway-db-seed ./cmd/aws-iot-gateway-db-seed"
test "$(sed -n '2p' "$CAPTURE_FILE")" = "go build -o $TMP_DIR/bin/aws-iot-gateway ./cmd/aws-iot-gateway"
test "$(sed -n '3p' "$CAPTURE_FILE")" = seed
test "$(sed -n '4p' "$CAPTURE_FILE")" = api

: >"$CAPTURE_FILE"
sed 's/@127\.0\.0\.1:5432/@staging.example.com:5432/' "$TMP_DIR/valid.env" >"$ENV_FILE"
if $DEV_SH up --environment-id "$ENVIRONMENT_ID" >"$TMP_DIR/remote-postgres.out" 2>&1; then
  fail "remote PostgreSQL must be rejected"
fi
grep -q 'DATABASE_URL must use a loopback host' "$TMP_DIR/remote-postgres.out"
test ! -s "$CAPTURE_FILE" || fail "remote PostgreSQL reached a mutation"

: >"$CAPTURE_FILE"
sed 's#redis://127\.0\.0\.1:6379#redis://staging.example.com:6379#' "$TMP_DIR/valid.env" >"$ENV_FILE"
if $DEV_SH up --environment-id "$ENVIRONMENT_ID" >"$TMP_DIR/remote-redis.out" 2>&1; then
  fail "remote Redis must be rejected"
fi
grep -q 'REDIS_URL must use a loopback host' "$TMP_DIR/remote-redis.out"
test ! -s "$CAPTURE_FILE" || fail "remote Redis reached a mutation"

: >"$CAPTURE_FILE"
sed 's/GATEWAY_LOCAL_POSTGRES_PORT=5432/GATEWAY_LOCAL_POSTGRES_PORT=55432/' "$TMP_DIR/valid.env" >"$ENV_FILE"
if $DEV_SH up --environment-id "$ENVIRONMENT_ID" >"$TMP_DIR/port-mismatch.out" 2>&1; then
  fail "PostgreSQL URL/local port mismatch must be rejected"
fi
grep -q 'DATABASE_URL port must match GATEWAY_LOCAL_POSTGRES_PORT=55432' "$TMP_DIR/port-mismatch.out"
test ! -s "$CAPTURE_FILE" || fail "port mismatch reached a mutation"

sed \
  -e 's/GATEWAY_LOCAL_POSTGRES_PORT=5432/GATEWAY_LOCAL_POSTGRES_PORT=55432/' \
  -e 's/GATEWAY_LOCAL_REDIS_PORT=6379/GATEWAY_LOCAL_REDIS_PORT=56379/' \
  -e 's/127\.0\.0\.1:5432/127.0.0.1:55432/' \
  -e 's/127\.0\.0\.1:6379/127.0.0.1:56379/' \
  "$TMP_DIR/valid.env" >"$ENV_FILE"
custom_status=$(GATEWAY_FAKE_LISTEN_PORTS='55432,56379' \
  $DEV_SH status --environment-id "$ENVIRONMENT_ID")
grep -q 'tcp:55432 listening' <<<"$custom_status"
grep -q 'tcp:56379 listening' <<<"$custom_status"
: >"$CAPTURE_FILE"
GATEWAY_FAKE_LISTEN_PORTS='55432,56379' $DEV_SH up \
  --environment-id "$ENVIRONMENT_ID" >"$TMP_DIR/custom-port.out" 2>&1
test "$(wc -l <"$CAPTURE_FILE" | tr -d ' ')" = 4
if grep -q '^docker ' "$CAPTURE_FILE"; then
  fail "custom listening ports must be reused"
fi

cp "$TMP_DIR/valid.env" "$ENV_FILE"
: >"$CAPTURE_FILE"
GATEWAY_FAKE_LISTEN_PORTS='' $DEV_SH up --environment-id "$ENVIRONMENT_ID" >"$TMP_DIR/fallback.out" 2>&1
first_line=$(sed -n '1p' "$CAPTURE_FILE")
grep -q 'docker compose .* up -d --wait postgres redis' <<<"$first_line"
grep -q '^seed$' "$CAPTURE_FILE"
grep -q '^api$' "$CAPTURE_FILE"

: >"$CAPTURE_FILE"
$DEV_SH down --environment-id "$ENVIRONMENT_ID"
grep -q 'docker compose .* down$' "$CAPTURE_FILE"

sed 's#/aws_iot_gateway?#/explorex_api?#' "$TMP_DIR/valid.env" >"$TMP_DIR/invalid-db.env"
cp "$TMP_DIR/invalid-db.env" "$ENV_FILE"
: >"$CAPTURE_FILE"
if $DEV_SH up --environment-id "$ENVIRONMENT_ID" >"$TMP_DIR/isolation.out" 2>&1; then
  fail "explorex_api database must be rejected"
fi
grep -q 'must target the dedicated aws_iot_gateway database' "$TMP_DIR/isolation.out"
test ! -s "$CAPTURE_FILE" || fail "invalid data isolation mutated local dependencies"

grep -q 'test/tool/dev_launcher_test.sh' "$DEV_SH"
grep -q 'deploy/local/test_local_compose_contract.sh' "$DEV_SH"
grep -q 'go test -race -count=1 ./...' "$DEV_SH"
grep -q 'go vet ./...' "$DEV_SH"

echo "Gateway local-dev launcher contract: ok"
