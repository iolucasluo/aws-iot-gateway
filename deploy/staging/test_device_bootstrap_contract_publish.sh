#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
SCRIPT="$ROOT/deploy/device-bootstrap/scripts/render-staging-contract.sh"

test -f "$SCRIPT"

python3 - "$SCRIPT" <<'PY'
from pathlib import Path
import sys

body = Path(sys.argv[1]).read_text(encoding="utf-8")
required = (
    "AWS_PROFILE",
    "AWS_REGION",
    "GATEWAY_DEVICE_BOOTSTRAP_STACK",
    "GATEWAY_DEVICE_BOOTSTRAP_NEXT_KEY_ID",
    "GATEWAY_DEVICE_BOOTSTRAP_NEXT_KMS_KEY_ID",
    "--out",
    "aws-release",
    "AmazonRootCA1.pem",
)
for value in required:
    if value not in body:
        raise SystemExit(f"staging Contract publisher is missing {value}")
PY

echo "staging device bootstrap Contract publisher: ok"
