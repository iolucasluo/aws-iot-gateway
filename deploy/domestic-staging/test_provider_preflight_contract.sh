#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd)
COMPOSITION_FILE="$SCRIPT_DIR/composition.cn-staging.example.yaml"

test -f "$COMPOSITION_FILE" || {
  echo "missing domestic composition example: $COMPOSITION_FILE" >&2
  exit 1
}

output=$(GATEWAY_ENVIRONMENT=staging \
  GATEWAY_PROVIDER_COMPOSITION_PATH="$COMPOSITION_FILE" \
  go run ./cmd/provider-composition-preflight 2>&1)
printf '%s\n' "$output"
grep -Fq 'provider composition preflight: OK' <<<"$output"
grep -Fq 'id=cn-staging environment=staging region=cn' <<<"$output"
grep -Fq 'instance=messaging.cn_staging capability=messaging driver=mqtt_broker' <<<"$output"
if grep -Eq 'secret://|credential_ref|bucket_ref|key_ref|trust_anchor_ref' <<<"$output"; then
  echo "preflight output leaked a credential reference" >&2
  exit 1
fi

if GATEWAY_ENVIRONMENT=development \
  GATEWAY_PROVIDER_COMPOSITION_PATH="$COMPOSITION_FILE" \
  go run ./cmd/provider-composition-preflight >/dev/null 2>&1; then
  echo "preflight accepted an environment mismatch" >&2
  exit 1
fi

if GATEWAY_ENVIRONMENT=staging go run ./cmd/provider-composition-preflight >/dev/null 2>&1; then
  echo "preflight accepted a missing composition path" >&2
  exit 1
fi

echo "provider composition preflight contract: ok"
