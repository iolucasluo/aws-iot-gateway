#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
SCRIPT="$ROOT/deploy/device-bootstrap/scripts/render-local-contract.sh"

test -f "$SCRIPT"

python3 - "$SCRIPT" <<'PY'
from pathlib import Path
import sys

body = Path(sys.argv[1]).read_text(encoding="utf-8")
required = (
    "GATEWAY_DEVICE_BOOTSTRAP_GATEWAY_URL",
    "GATEWAY_DEVICE_BOOTSTRAP_TIME_URL",
    "GATEWAY_DEVICE_BOOTSTRAP_KEY_ID",
    "GATEWAY_DEVICE_BOOTSTRAP_PRIVATE_KEY",
    "GATEWAY_DEVICE_BOOTSTRAP_NEXT_KEY_ID",
    "GATEWAY_DEVICE_BOOTSTRAP_NEXT_PUBLIC_KEY",
    "--out",
    "cmd/gateway-device-bootstrap-contract",
)
for value in required:
    if value not in body:
        raise SystemExit(f"local Contract publisher is missing {value}")
if "openssl genpkey" in body:
    raise SystemExit("local Contract publisher must not silently create private keys")
PY

echo "local device bootstrap Contract publisher: ok"
