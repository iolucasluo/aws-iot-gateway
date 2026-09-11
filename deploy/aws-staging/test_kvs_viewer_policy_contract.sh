#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
TEMPLATE="$ROOT/deploy/aws-staging/cloudformation/staging.yaml"

python3 - "$TEMPLATE" <<'PY'
import sys
from pathlib import Path

body = Path(sys.argv[1]).read_text()

def require(value, message):
    if not value:
        raise SystemExit(f"FAIL {message}")

api_role = body.split("  APITaskRole:", 1)[1].split("  WorkerTaskRole:", 1)[0]
require("kinesisvideo:ConnectAsViewer" in api_role,
        "API task role must authorize KVS ConnectAsViewer for presigned WSS URLs")
require("channel/aws-iot-gateway-${EnvironmentName}-*" in api_role,
        "ConnectAsViewer permission must remain scoped to this environment's channels")

print("test_kvs_viewer_policy_contract: OK")
PY
