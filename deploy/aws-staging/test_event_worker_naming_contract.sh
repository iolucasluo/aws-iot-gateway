#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"

python3 - "$ROOT" <<'PY'
from pathlib import Path
import sys

root = Path(sys.argv[1])
dockerfile = (root / "Dockerfile").read_text()
template = (root / "deploy/aws-staging/cloudformation/staging.yaml").read_text()

def require(condition: bool, message: str) -> None:
    if not condition:
        raise SystemExit(message)

require((root / "cmd/event-worker/main.go").is_file(),
        "event worker entrypoint must be cmd/event-worker")
require(not (root / "cmd/aws-iot-gateway-worker").exists(),
        "legacy cmd/aws-iot-gateway-worker entrypoint must be removed")
require("./cmd/event-worker" in dockerfile,
        "Dockerfile must build cmd/event-worker")
require("/usr/local/bin/iot-gateway-event-worker" in dockerfile,
        "container binary must use the deployment alias iot-gateway-event-worker")
require("Family: !Sub aws-iot-gateway-${EnvironmentName}-event-worker" in template,
        "ECS task family must identify the event worker")
require("- Name: event-worker" in template,
        "ECS container must be named event-worker")
require("Command: [/usr/local/bin/iot-gateway-event-worker]" in template,
        "ECS task must invoke the event-worker deployment alias")
require("ServiceName: iot-gateway-event-worker" in template,
        "ECS service must use the unambiguous deployment alias")
PY
