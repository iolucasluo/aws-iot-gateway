#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
COMPOSITION_FILE="$SCRIPT_DIR/composition.cn-staging.example.yaml"
AUDIT_BINARY=$(mktemp)
trap 'rm -f "$AUDIT_BINARY"' EXIT

test -f "$COMPOSITION_FILE" || {
  echo "missing domestic composition example: $COMPOSITION_FILE" >&2
  exit 1
}

go build -o "$AUDIT_BINARY" ./cmd/provider-composition-runtime-audit

set +e
output=$(
  GATEWAY_ENVIRONMENT=staging \
  GATEWAY_PROVIDER_COMPOSITION_PATH="$COMPOSITION_FILE" \
  "$AUDIT_BINARY" 2>&1
)
status=$?
set -e

if [ "$status" -ne 2 ]; then
  echo "runtime audit expected blocked exit code 2, got $status" >&2
  printf '%s\n' "$output" >&2
  exit 1
fi
grep -Fq 'provider composition runtime audit: BLOCKED' <<<"$output"
grep -Fq 'binding=messaging instance=messaging.cn_staging capability=messaging driver=mqtt_broker status=SUPPORTED' <<<"$output"
grep -Fq 'binding=identity instance=identity.cn_staging capability=identity driver=ca_mqtt_identity status=BLOCKED' <<<"$output"
grep -Fq 'binding=signer instance=signer.cn_staging capability=signer driver=domestic_kms status=BLOCKED' <<<"$output"

if grep -Eqi '(secret://|credential_ref|trust_anchor_ref|bucket_ref|key_ref|mqtt\.example\.invalid)' <<<"$output"; then
  echo "runtime audit output leaked a sensitive reference or endpoint" >&2
  exit 1
fi

if grep -Eqi '(aws-sdk|amazonaws|cloudformation)' cmd/provider-composition-runtime-audit/main.go internal/runtime/runtime_plan.go; then
  echo "runtime audit implementation must not depend on AWS SDK/data-plane" >&2
  exit 1
fi

echo "domestic runtime audit contract: ok"
