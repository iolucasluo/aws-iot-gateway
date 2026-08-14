#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
TEMPLATE="$ROOT/deploy/cloudformation/staging.yaml"

python3 - "$TEMPLATE" <<'PY'
import sys
from pathlib import Path

body = Path(sys.argv[1]).read_text()

def require(value, message):
    if not value:
        raise SystemExit(f"FAIL {message}")

prefix = "dt/aws-iot-gateway-${EnvironmentName}/+/"
for suffix in ("status", "lwt"):
    rule_sql = f"SELECT online, topic() AS topic, timestamp() AS received_at_ms FROM '{prefix}{suffix}'"
    require(rule_sql in body,
            f"staging must route device {suffix} messages to the ingest queue")

require(body.count("timestamp() AS received_at_ms") >= 2,
        "presence rules must retain AWS IoT receipt time for SQS reordering protection")

print("test_presence_ingest_contract: OK")
PY
