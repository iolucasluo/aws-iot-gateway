#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)

usage() {
    echo "usage: GATEWAY_DEVICE_BOOTSTRAP_GATEWAY_URL=http://<shared-local-host>:14080 \\
  GATEWAY_DEVICE_BOOTSTRAP_TIME_URL=http://<shared-local-host>:14080/api/v1/device-time/bootstrap \\
  GATEWAY_DEVICE_BOOTSTRAP_KEY_ID=time-local-<environment>-g1 \\
  GATEWAY_DEVICE_BOOTSTRAP_PRIVATE_KEY=/secure/local-time.pem \\
  $0 --out /absolute/new-contract-directory" >&2
    exit 2
}

require_value() {
    name=$1
    eval "value=\${$name:-}"
    [ -n "$value" ] || { echo "$name is required" >&2; exit 2; }
}

[ "$#" -eq 2 ] && [ "$1" = "--out" ] || usage
OUTPUT=$2
case "$OUTPUT" in
    /*) ;;
    *) echo "--out must be an absolute new directory" >&2; exit 2 ;;
esac
require_value GATEWAY_DEVICE_BOOTSTRAP_GATEWAY_URL
require_value GATEWAY_DEVICE_BOOTSTRAP_TIME_URL
require_value GATEWAY_DEVICE_BOOTSTRAP_KEY_ID
require_value GATEWAY_DEVICE_BOOTSTRAP_PRIVATE_KEY
[ ! -e "$OUTPUT" ] || { echo "--out already exists: $OUTPUT" >&2; exit 2; }

NEXT_KEY_ID=${GATEWAY_DEVICE_BOOTSTRAP_NEXT_KEY_ID:-}
NEXT_PUBLIC_KEY=${GATEWAY_DEVICE_BOOTSTRAP_NEXT_PUBLIC_KEY:-}
if [ -n "$NEXT_KEY_ID" ] || [ -n "$NEXT_PUBLIC_KEY" ]; then
    [ -n "$NEXT_KEY_ID" ] && [ -n "$NEXT_PUBLIC_KEY" ] || {
        echo "GATEWAY_DEVICE_BOOTSTRAP_NEXT_KEY_ID and GATEWAY_DEVICE_BOOTSTRAP_NEXT_PUBLIC_KEY must be set together" >&2
        exit 2
    }
    set -- --next-key-id "$NEXT_KEY_ID" --next-public-key "$NEXT_PUBLIC_KEY"
else
    set --
fi

exec go run "$ROOT/cmd/gateway-device-bootstrap-contract" local \
    --out "$OUTPUT" \
    --gateway-url "$GATEWAY_DEVICE_BOOTSTRAP_GATEWAY_URL" \
    --time-bootstrap-url "$GATEWAY_DEVICE_BOOTSTRAP_TIME_URL" \
    --key-id "$GATEWAY_DEVICE_BOOTSTRAP_KEY_ID" \
    --private-key "$GATEWAY_DEVICE_BOOTSTRAP_PRIVATE_KEY" \
    "$@"
