#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
TEMPLATE="$ROOT/deploy/cloudformation/staging.yaml"
SERVER="$ROOT/internal/httpapi/server.go"

require() {
    grep -F "$1" "$2" >/dev/null || {
        echo "missing OTA download proxy contract: $1 in $2" >&2
        exit 1
    }
}

require 'Name: OTA_DEVICE_DOWNLOAD_BASE_URL' "$TEMPLATE"
require 'https://${HTTPAPI}.execute-api.${AWS::Region}.${AWS::URLSuffix}' "$TEMPLATE"
require '/api/v1/device-ota/download/' "$SERVER"

echo "test_ota_download_proxy_contract: OK"
