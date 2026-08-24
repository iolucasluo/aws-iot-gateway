#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
AWS_PROFILE=${AWS_PROFILE:-explorex-staging-hk}
AWS_REGION=${AWS_REGION:-ap-east-1}
DEVICE_SERVICE_BOOTSTRAP_STACK=${DEVICE_SERVICE_BOOTSTRAP_STACK:-aws-iot-gateway-staging}
DEVICE_SERVICE_BOOTSTRAP_CA=${DEVICE_SERVICE_BOOTSTRAP_CA:-"$ROOT/deploy/device-bootstrap/trust-roots/AmazonRootCA1.pem"}

usage() {
    echo "usage: AWS_PROFILE=<authorized-profile> AWS_REGION=<region> $0 --out /absolute/new-contract-directory" >&2
    exit 2
}

[ "$#" -eq 2 ] && [ "$1" = "--out" ] || usage
OUTPUT=$2
case "$OUTPUT" in
    /*) ;;
    *) echo "--out must be an absolute new directory" >&2; exit 2 ;;
esac
[ ! -e "$OUTPUT" ] || { echo "--out already exists: $OUTPUT" >&2; exit 2; }
[ -r "$DEVICE_SERVICE_BOOTSTRAP_CA" ] || {
    echo "service CA cannot be read: $DEVICE_SERVICE_BOOTSTRAP_CA" >&2
    exit 2
}

NEXT_KEY_ID=${DEVICE_SERVICE_BOOTSTRAP_NEXT_KEY_ID:-}
NEXT_KMS_KEY_ID=${DEVICE_SERVICE_BOOTSTRAP_NEXT_KMS_KEY_ID:-}
if [ -n "$NEXT_KEY_ID" ] || [ -n "$NEXT_KMS_KEY_ID" ]; then
    [ -n "$NEXT_KEY_ID" ] && [ -n "$NEXT_KMS_KEY_ID" ] || {
        echo "DEVICE_SERVICE_BOOTSTRAP_NEXT_KEY_ID and DEVICE_SERVICE_BOOTSTRAP_NEXT_KMS_KEY_ID must be set together" >&2
        exit 2
    }
    set -- --next-key-id "$NEXT_KEY_ID" --next-kms-key-id "$NEXT_KMS_KEY_ID"
else
    set --
fi

export AWS_PROFILE AWS_REGION
cd "$ROOT"
exec go run ./cmd/device-service-bootstrap-contract aws-release \
    --out "$OUTPUT" \
    --environment staging \
    --stack "$DEVICE_SERVICE_BOOTSTRAP_STACK" \
    --region "$AWS_REGION" \
    --service-ca "$DEVICE_SERVICE_BOOTSTRAP_CA" \
    "$@"
