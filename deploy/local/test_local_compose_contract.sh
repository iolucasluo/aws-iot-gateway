#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
COMPOSE_FILE="$SCRIPT_DIR/compose.yaml"

test -f "$COMPOSE_FILE"

rendered=$(docker compose -f "$COMPOSE_FILE" config)

grep -q 'published: "5432"' <<<"$rendered"
grep -q 'target: 5432' <<<"$rendered"
grep -q 'published: "6379"' <<<"$rendered"
grep -q 'target: 6379' <<<"$rendered"
grep -q 'postgres:17-alpine' <<<"$rendered"
grep -q 'redis:7-alpine' <<<"$rendered"
grep -q 'POSTGRES_DB: aws_iot_gateway' <<<"$rendered"

if grep -Eqi '(replace-at-deploy|api_secret|aws_secret_access_key)' "$COMPOSE_FILE"; then
  echo "local compose must not contain application or AWS secrets" >&2
  exit 1
fi

echo "local compose contract: ok"
