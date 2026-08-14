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

thing = "${!iot:Connection.Thing.ThingName}"
prefix = "aws-iot-gateway-${EnvironmentName}"

for suffix in ("event", "status", "lwt"):
    require(f"topic/dt/{prefix}/{thing}/{suffix}" in body,
            f"device policy must allow the firmware {suffix} publish topic")

require(f"topic/cmd/{prefix}/{thing}/res" in body,
        "device policy must allow command responses")
require(f"topic/cmd/{prefix}/{thing}/req" in body and
        f"topicfilter/cmd/{prefix}/{thing}/req" in body,
        "device policy must allow command receive and subscribe")

for suffix in ("update", "get"):
    require(f"topic/$aws/things/{thing}/shadow/name/time-config/{suffix}" in body,
            f"device policy must allow time-config shadow {suffix} publish")

for suffix in ("update/delta", "get/accepted"):
    require(f"topic/$aws/things/{thing}/shadow/name/time-config/{suffix}" in body and
            f"topicfilter/$aws/things/{thing}/shadow/name/time-config/{suffix}" in body,
            f"device policy must allow time-config shadow {suffix} receive and subscribe")

require(f"topic/$aws/things/{thing}/shadow/name/internal-aws/update" in body,
        "device policy must allow the runtime named shadow update")

print("test_device_policy_contract: OK")
PY
