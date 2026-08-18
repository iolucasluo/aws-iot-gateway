#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
LOCAL_SCRIPT="$ROOT/deploy/device-bootstrap/scripts/render-local-contract.sh"
STAGING_SCRIPT="$ROOT/deploy/device-bootstrap/scripts/render-staging-contract.sh"

test -f "$LOCAL_SCRIPT"
test -f "$STAGING_SCRIPT"

python3 - "$LOCAL_SCRIPT" "$STAGING_SCRIPT" <<'PY'
from pathlib import Path
import sys

local = Path(sys.argv[1]).read_text(encoding="utf-8")
staging = Path(sys.argv[2]).read_text(encoding="utf-8")

for value in (
    "GATEWAY_DEVICE_BOOTSTRAP_GATEWAY_URL",
    "GATEWAY_DEVICE_BOOTSTRAP_TIME_URL",
    "GATEWAY_DEVICE_BOOTSTRAP_KEY_ID",
    "GATEWAY_DEVICE_BOOTSTRAP_PRIVATE_KEY",
    "GATEWAY_DEVICE_BOOTSTRAP_NEXT_KEY_ID",
    "GATEWAY_DEVICE_BOOTSTRAP_NEXT_PUBLIC_KEY",
    "--out",
    "cmd/gateway-device-bootstrap-contract",
):
    if value not in local:
        raise SystemExit(f"local Contract publisher is missing {value}")
if "openssl genpkey" in local:
    raise SystemExit("local Contract publisher must not silently create private keys")

for value in (
    "AWS_PROFILE",
    "AWS_REGION",
    "GATEWAY_DEVICE_BOOTSTRAP_STACK",
    "GATEWAY_DEVICE_BOOTSTRAP_NEXT_KEY_ID",
    "GATEWAY_DEVICE_BOOTSTRAP_NEXT_KMS_KEY_ID",
    "--out",
    "aws-release",
    "AmazonRootCA1.pem",
):
    if value not in staging:
        raise SystemExit(f"staging Contract publisher is missing {value}")
PY

echo "device bootstrap Contract publishers: ok"
