#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
COMPOSE_FILE="$SCRIPT_DIR/compose.yaml"
COMPOSITION_FILE="$SCRIPT_DIR/composition.cn-staging.example.yaml"
README_FILE="$SCRIPT_DIR/README.md"
SIGNALING_CONTRACT="$SCRIPT_DIR/../../docs/contracts/explorex-signal-v1.md"
MQTT_TLS_CONFIG="$SCRIPT_DIR/mosquitto.mtls.conf"
MQTT_ACL_EXAMPLE="$SCRIPT_DIR/mosquitto.mtls.acl.example"

for path in "$COMPOSE_FILE" "$COMPOSITION_FILE" "$README_FILE" "$SIGNALING_CONTRACT" "$MQTT_TLS_CONFIG" "$MQTT_ACL_EXAMPLE"; do
  test -f "$path" || {
    echo "missing domestic trial contract input: $path" >&2
    exit 1
  }
done

python3 - "$COMPOSE_FILE" "$COMPOSITION_FILE" "$README_FILE" "$SIGNALING_CONTRACT" "$MQTT_TLS_CONFIG" "$MQTT_ACL_EXAMPLE" <<'PY'
import pathlib
import sys

compose, composition, readme, signaling, mqtt_tls_config, mqtt_acl = map(pathlib.Path, sys.argv[1:])
compose_text = compose.read_text()
composition_text = composition.read_text()
readme_text = readme.read_text()
signaling_text = signaling.read_text()
mqtt_tls_text = mqtt_tls_config.read_text()
mqtt_acl_text = mqtt_acl.read_text()


def require(condition, message):
    if not condition:
        raise SystemExit(f"FAIL {message}")


for service in ("postgres:", "redis:", "mqtt:", "turn:"):
    require(service in compose_text, f"domestic compose lacks {service}")
require("gateway:" not in compose_text and "gateway-api:" not in compose_text,
        "domestic trial must not present the AWS-bound Gateway as ready")
require("127.0.0.1" in compose_text and "private:" in compose_text and
        "internal: true" in compose_text,
        "state/network isolation must remain explicit")
require("aws" not in compose_text.lower() and "kvs" not in compose_text.lower() and
        "sqs" not in compose_text.lower(),
        "domestic compose must not contain AWS data-plane references")
require("mqtt-mtls:" in compose_text, "domestic compose lacks mqtt-mtls profile")
for marker in ("listener 8883", "require_certificate true", "use_identity_as_username true", "acl_file"):
    require(marker in mqtt_tls_text, f"MQTT TLS config is missing {marker}")
for marker in ("user gateway-cn-staging", "user feeder-cn-staging", "topic write cmd/cn-staging/+/req"):
    require(marker in mqtt_acl_text, f"MQTT ACL example is missing {marker}")

for marker in (
    "driver: explorex_signal",
    "realtime: realtime.cn_staging",
    "forbid_regions: [aws]",
    "allow_mixed_provider: true",
):
    require(marker in composition_text, f"composition is missing {marker}")

for marker in (
    "不伪造 signaling service",
    "真实 signaling",
    "coturn",
):
    require(marker in readme_text, f"staging README is missing boundary marker {marker}")

for marker in (
    "explorex-signal-v1",
    "Idempotency-Key",
    "ticket",
    "coturn",
    "Anyka",
):
    require(marker in signaling_text, f"signaling contract is missing {marker}")

print("domestic trial preflight contract: NO-SHIP (runtime wiring and feeder HIL pending)")
PY
