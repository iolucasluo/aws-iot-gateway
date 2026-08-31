#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel)
COMPOSE_FILE="$REPO_ROOT/deploy/local/compose.yaml"
COMPOSE_PROJECT=aws-iot-gateway-local

ACTION=${1:-help}
if [[ $# -gt 0 ]]; then shift; fi
ENVIRONMENT_ID=dev

while [[ $# -gt 0 ]]; do
  case "$1" in
    --environment-id|--env)
      ENVIRONMENT_ID=${2:?$1 requires a name}
      shift 2
      ;;
    *)
      echo "unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

if [[ ! "$ENVIRONMENT_ID" =~ ^[a-zA-Z0-9_-]+$ ]]; then
  echo "invalid environment id: $ENVIRONMENT_ID" >&2
  exit 2
fi

ENV_FILE="$REPO_ROOT/.env.$ENVIRONMENT_ID"
BIN_DIR=${GATEWAY_DEV_BIN_DIR:-"$REPO_ROOT/bin"}
SEED_BIN="$BIN_DIR/aws-iot-gateway-db-seed"
API_BIN="$BIN_DIR/aws-iot-gateway"
POSTGRES_PORT=5432
REDIS_PORT=6379

usage() {
  cat <<'USAGE'
usage: tool/dev.sh <contract-version|status|up|down|test> [--environment-id NAME]

  contract-version  print the Gateway local-dev contract version
  status            inspect the selected env and local ports without mutation
  up                reuse/start dependencies, seed, then run the API in foreground
  down              stop only this repository's fallback compose dependencies
  test              run launcher, compose, race, and vet checks
USAGE
}

port_listens() {
  lsof -nP -iTCP:"$1" -sTCP:LISTEN >/dev/null 2>&1
}

require_env_file() {
  if [[ ! -f "$ENV_FILE" ]]; then
    echo "missing $ENV_FILE; copy .env.local.example and fill the authorized environment" >&2
    exit 1
  fi
  if ! git -C "$REPO_ROOT" check-ignore -q ".env.$ENVIRONMENT_ID"; then
    echo "$ENV_FILE is not ignored by Git" >&2
    exit 1
  fi
}

validate_env_file() {
  require_env_file
  (
    set +a
    # shellcheck disable=SC1090
    . "$ENV_FILE"
    validate_local_data_isolation
  )
}

export_env_for_gateway() {
  set -a
  # shellcheck disable=SC1090
  . "$ENV_FILE"
  set +a
}

validate_local_port() {
  local variable_name=$1
  local value=$2
  if [[ ! "$value" =~ ^[1-9][0-9]{0,4}$ ]] || (( value > 65535 )); then
    echo "$variable_name must be a TCP port from 1 to 65535" >&2
    exit 1
  fi
}

load_local_dependency_ports() {
  local ports
  ports=$(
    set +a
    # shellcheck disable=SC1090
    . "$ENV_FILE"
    printf '%s %s\n' \
      "${GATEWAY_LOCAL_POSTGRES_PORT:-5432}" \
      "${GATEWAY_LOCAL_REDIS_PORT:-6379}"
  )
  read -r POSTGRES_PORT REDIS_PORT <<<"$ports"
  validate_local_port GATEWAY_LOCAL_POSTGRES_PORT "$POSTGRES_PORT"
  validate_local_port GATEWAY_LOCAL_REDIS_PORT "$REDIS_PORT"
}

validate_loopback_url() {
  local label=$1
  local url=$2
  local scheme=$3
  local expected_port=$4
  local port_variable=$5
  local authority endpoint host port

  if [[ "$url" != "$scheme://"* ]]; then
    echo "$label must use the $scheme scheme" >&2
    exit 1
  fi
  authority=${url#*://}
  authority=${authority%%/*}
  endpoint=${authority##*@}
  if [[ "$endpoint" == \[*\]:* ]]; then
    host=${endpoint%%]*}
    host=${host#\[}
    port=${endpoint#*\]:}
  elif [[ "$endpoint" == *:* ]]; then
    host=${endpoint%:*}
    port=${endpoint##*:}
  else
    echo "$label must include an explicit local port" >&2
    exit 1
  fi
  case "$host" in
    127.0.0.1|localhost|::1) ;;
    *)
      echo "$label must use a loopback host" >&2
      exit 1
      ;;
  esac
  if [[ "$port" != "$expected_port" ]]; then
    echo "$label port must match $port_variable=$expected_port" >&2
    exit 1
  fi
}

validate_local_data_isolation() {
  local expected_database=${GATEWAY_LOCAL_POSTGRES_DATABASE:?GATEWAY_LOCAL_POSTGRES_DATABASE is required}
  local expected_postgres_port=${GATEWAY_LOCAL_POSTGRES_PORT:-5432}
  local expected_redis_port=${GATEWAY_LOCAL_REDIS_PORT:-6379}
  local database_url=${DATABASE_URL:?DATABASE_URL is required}
  local redis_url=${REDIS_URL:?REDIS_URL is required}
  local database_query=
  local environment_schema=${ENVIRONMENT_ID//-/_}
  local search_path_pattern=
  local database_path=${database_url%%\?*}
  local database_name=${database_path##*/}
  local redis_path=${redis_url%%\?*}
  local redis_db=${redis_path##*/}

  if [[ "$database_url" == *\?* ]]; then
    database_query=${database_url#*\?}
    search_path_pattern="^sslmode=disable&options=-csearch_path%3Dgateway_${environment_schema}(_v[1-9][0-9]*)?%2Cpublic$"
    if [[ "$database_query" != "sslmode=disable" &&
          ! "$database_query" =~ $search_path_pattern ]]; then
      echo "DATABASE_URL query must be empty or exactly sslmode=disable, optionally with an environment-owned search_path" >&2
      exit 1
    fi
  fi
  if [[ "$redis_url" == *\?* ]]; then
    echo "REDIS_URL query is not allowed" >&2
    exit 1
  fi
  validate_local_port GATEWAY_LOCAL_POSTGRES_PORT "$expected_postgres_port"
  validate_local_port GATEWAY_LOCAL_REDIS_PORT "$expected_redis_port"
  validate_loopback_url DATABASE_URL "$database_url" postgres \
    "$expected_postgres_port" GATEWAY_LOCAL_POSTGRES_PORT
  validate_loopback_url REDIS_URL "$redis_url" redis \
    "$expected_redis_port" GATEWAY_LOCAL_REDIS_PORT
  if [[ "$database_name" != "$expected_database" || "$database_name" == "explorex_api" ]]; then
    echo "DATABASE_URL must target the dedicated $expected_database database" >&2
    exit 1
  fi
  if [[ ! "$redis_db" =~ ^[1-9][0-9]*$ ]]; then
    echo "REDIS_URL must use a non-zero local Redis DB index" >&2
    exit 1
  fi
}

compose() {
  docker compose -p "$COMPOSE_PROJECT" --env-file "$ENV_FILE" -f "$COMPOSE_FILE" "$@"
}

ensure_dependencies() {
  local targets=()
  if port_listens "$POSTGRES_PORT"; then
    echo "PostgreSQL 127.0.0.1:$POSTGRES_PORT: reuse existing process"
  else
    targets+=(postgres)
  fi
  if port_listens "$REDIS_PORT"; then
    echo "Redis 127.0.0.1:$REDIS_PORT: reuse existing process"
  else
    targets+=(redis)
  fi
  if [[ ${#targets[@]} -gt 0 ]]; then
    compose up -d --wait "${targets[@]}"
  fi
}

status() {
  if [[ -f "$ENV_FILE" ]]; then
    if git -C "$REPO_ROOT" check-ignore -q ".env.$ENVIRONMENT_ID"; then
      echo "env=$ENVIRONMENT_ID file=present ignored=yes"
    else
      echo "env=$ENVIRONMENT_ID file=present ignored=no"
    fi
  else
    echo "env=$ENVIRONMENT_ID file=missing"
  fi
  if [[ -f "$ENV_FILE" ]]; then
    load_local_dependency_ports
  fi
  for port in "$POSTGRES_PORT" "$REDIS_PORT" 14080; do
    if port_listens "$port"; then
      echo "tcp:$port listening"
    else
      echo "tcp:$port stopped"
    fi
  done
  if curl --silent --show-error --fail --max-time 2 http://127.0.0.1:14080/healthz >/dev/null 2>&1; then
    echo "gateway_health=ok"
  else
    echo "gateway_health=unavailable"
  fi
}

run_gateway() {
  validate_env_file
  load_local_dependency_ports
  ensure_dependencies
  export_env_for_gateway
  validate_local_data_isolation
  mkdir -p "$BIN_DIR"
  (
    cd "$REPO_ROOT"
    go build -o "$SEED_BIN" ./cmd/aws-iot-gateway-db-seed
    go build -o "$API_BIN" ./cmd/aws-iot-gateway
  )
  "$SEED_BIN"
  echo "Gateway API env=$ENVIRONMENT_ID"
  exec "$API_BIN"
}

run_tests() {
  bash "$REPO_ROOT/test/tool/dev_launcher_test.sh"
  bash "$REPO_ROOT/deploy/local/test_local_compose_contract.sh"
  (
    cd "$REPO_ROOT"
    go test -race -count=1 ./...
    go vet ./...
  )
}

case "$ACTION" in
  contract-version) echo 1 ;;
  help|-h|--help) usage ;;
  status) status ;;
  up) run_gateway ;;
  down)
    require_env_file
    compose down
    ;;
  test) run_tests ;;
  *)
    usage >&2
    exit 2
    ;;
esac
