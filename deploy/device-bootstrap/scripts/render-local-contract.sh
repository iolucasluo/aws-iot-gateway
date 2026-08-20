#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)

usage() {
    echo "usage: DEVICE_SERVICE_BOOTSTRAP_SERVICE_URL=http://<shared-local-host>:14080 \\
  DEVICE_SERVICE_BOOTSTRAP_TIME_URL=http://<shared-local-host>:14080/api/v1/device-time/bootstrap \\
  DEVICE_SERVICE_BOOTSTRAP_KEY_ID=time-local-<environment>-g1 \\
  DEVICE_SERVICE_BOOTSTRAP_PRIVATE_KEY=/secure/local-time.pem \\
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
require_value DEVICE_SERVICE_BOOTSTRAP_SERVICE_URL
require_value DEVICE_SERVICE_BOOTSTRAP_TIME_URL
require_value DEVICE_SERVICE_BOOTSTRAP_KEY_ID
require_value DEVICE_SERVICE_BOOTSTRAP_PRIVATE_KEY
[ ! -e "$OUTPUT" ] || { echo "--out already exists: $OUTPUT" >&2; exit 2; }

NEXT_KEY_ID=${DEVICE_SERVICE_BOOTSTRAP_NEXT_KEY_ID:-}
NEXT_PUBLIC_KEY=${DEVICE_SERVICE_BOOTSTRAP_NEXT_PUBLIC_KEY:-}
if [ -n "$NEXT_KEY_ID" ] || [ -n "$NEXT_PUBLIC_KEY" ]; then
    [ -n "$NEXT_KEY_ID" ] && [ -n "$NEXT_PUBLIC_KEY" ] || {
        echo "DEVICE_SERVICE_BOOTSTRAP_NEXT_KEY_ID and DEVICE_SERVICE_BOOTSTRAP_NEXT_PUBLIC_KEY must be set together" >&2
        exit 2
    }
    set -- --next-key-id "$NEXT_KEY_ID" --next-public-key "$NEXT_PUBLIC_KEY"
else
    set --
fi

exec go run "$ROOT/cmd/device-service-bootstrap-contract" local \
    --out "$OUTPUT" \
    --service-url "$DEVICE_SERVICE_BOOTSTRAP_SERVICE_URL" \
    --time-bootstrap-url "$DEVICE_SERVICE_BOOTSTRAP_TIME_URL" \
    --key-id "$DEVICE_SERVICE_BOOTSTRAP_KEY_ID" \
    --private-key "$DEVICE_SERVICE_BOOTSTRAP_PRIVATE_KEY" \
    "$@"
