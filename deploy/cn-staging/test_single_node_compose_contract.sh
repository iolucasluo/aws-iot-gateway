#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
BASE_COMPOSE="$SCRIPT_DIR/compose.yaml"
APP_COMPOSE="$SCRIPT_DIR/compose.single-node.yaml"
DOCKERFILE="$SCRIPT_DIR/../../Dockerfile"
STACK_ENV_EXAMPLE="$SCRIPT_DIR/single-node.stack.env.example"
GATEWAY_ENV_EXAMPLE="$SCRIPT_DIR/gateway-single-node.env.example"
SIGNAL_ENV_EXAMPLE="$SCRIPT_DIR/signal-single-node.env.example"
COMPOSITION_EXAMPLE="$SCRIPT_DIR/composition.single-node.example.yaml"
CONTRACT_DIR=$(mktemp -d)
trap 'rm -rf "$CONTRACT_DIR"' EXIT

mkdir -p "$CONTRACT_DIR/runtime"
cat >"$CONTRACT_DIR/gateway.env" <<'EOF'
GATEWAY_ENVIRONMENT=development
GATEWAY_PROVIDER_MODE=domestic-dev
DATABASE_URL=postgres://gateway:contract-only-postgres-secret@postgres:5432/gateway_cn?sslmode=disable
REDIS_URL=redis://:contract-only-redis-secret@redis:6379/1
EOF
cat >"$CONTRACT_DIR/signal.env" <<'EOF'
EXPLOREX_SIGNAL_PUBLIC_BASE_URL=ws://127.0.0.1:28084
EOF
cat >"$CONTRACT_DIR/stack.env" <<EOF
CN_STAGING_STATE_BIND_ADDRESS=127.0.0.1
CN_STAGING_EDGE_BIND_ADDRESS=127.0.0.1
CN_STAGING_POSTGRES_USER=gateway
CN_STAGING_POSTGRES_PASSWORD=contract-only-postgres-secret
CN_STAGING_POSTGRES_DATABASE=gateway_cn
CN_STAGING_POSTGRES_PORT=55432
CN_STAGING_REDIS_PASSWORD=contract-only-redis-secret
CN_STAGING_REDIS_PORT=56379
CN_STAGING_MQTT_PORT=51883
CN_STAGING_MQTTS_PORT=51884
CN_STAGING_MQTT_TLS_DIR=$CONTRACT_DIR/runtime
CN_STAGING_MQTT_ACL_FILE=$CONTRACT_DIR/runtime/mosquitto.acl
CN_STAGING_TURN_REALM=cn-staging.local
CN_STAGING_TURN_USER=turn-contract
CN_STAGING_TURN_PASSWORD=contract-only-turn-secret
CN_STAGING_TURN_UDP_PORT=53478
CN_STAGING_TURN_TCP_PORT=53478
CN_STAGING_TURN_RELAY_MIN_PORT=49160
CN_STAGING_TURN_RELAY_MAX_PORT=49200
CN_STAGING_MINIO_API_PORT=59000
CN_STAGING_MINIO_CONSOLE_PORT=59001
CN_STAGING_MINIO_ROOT_USER=contract-only-minio
CN_STAGING_MINIO_ROOT_PASSWORD=contract-only-minio-secret
CN_STAGING_GATEWAY_IMAGE=explorex-cn-gateway:contract
CN_STAGING_GATEWAY_ENV_FILE=$CONTRACT_DIR/gateway.env
CN_STAGING_SIGNAL_ENV_FILE=$CONTRACT_DIR/signal.env
CN_STAGING_RUNTIME_DIR=$CONTRACT_DIR/runtime
CN_STAGING_GATEWAY_PORT=24083
CN_STAGING_SIGNAL_PORT=28084
CN_STAGING_REALTIME_AUTH_TOKEN=contract-only-signal-secret
EOF
touch "$CONTRACT_DIR/runtime/mosquitto.acl"

for path in "$BASE_COMPOSE" "$APP_COMPOSE" "$DOCKERFILE" "$STACK_ENV_EXAMPLE" "$GATEWAY_ENV_EXAMPLE" "$SIGNAL_ENV_EXAMPLE" "$COMPOSITION_EXAMPLE"; do
  test -f "$path" || {
    echo "missing single-node contract input: $path" >&2
    exit 1
  }
done

rendered=$(docker compose --env-file "$CONTRACT_DIR/stack.env" \
  -f "$BASE_COMPOSE" -f "$APP_COMPOSE" --profile single-node config --format json)

python3 - "$rendered" "$DOCKERFILE" "$STACK_ENV_EXAMPLE" "$GATEWAY_ENV_EXAMPLE" "$SIGNAL_ENV_EXAMPLE" "$COMPOSITION_EXAMPLE" <<'PY'
import json
import pathlib
import sys

config = json.loads(sys.argv[1])
dockerfile = pathlib.Path(sys.argv[2]).read_text()
stack_example = pathlib.Path(sys.argv[3]).read_text()
gateway_example = pathlib.Path(sys.argv[4]).read_text()
signal_example = pathlib.Path(sys.argv[5]).read_text()
composition_example = pathlib.Path(sys.argv[6]).read_text()
services = config.get("services", {})

if config.get("name") != "explorex-cn-single-node":
    raise SystemExit("single-node overlay must use an isolated Compose project name")

required = {
    "postgres",
    "redis",
    "mqtt-mtls",
    "turn",
    "gateway-seed",
    "gateway-api",
    "rtc-signaling",
}
missing = required - services.keys()
if missing:
    raise SystemExit(f"single-node profile lacks services: {sorted(missing)}")
for forbidden in ("mqtt", "minio", "gateway-worker", "event-worker"):
    if forbidden in services:
        raise SystemExit(f"single-node profile must not enable {forbidden}")

images = {services[name].get("image") for name in ("gateway-seed", "gateway-api", "rtc-signaling")}
if len(images) != 1 or None in images:
    raise SystemExit("application services must use one Gateway image input")
for name in ("gateway-seed", "gateway-api", "rtc-signaling"):
    service = services[name]
    if not service.get("read_only") or not service.get("init"):
        raise SystemExit(f"application service must be read-only and init-enabled: {name}")
    if "ALL" not in service.get("cap_drop", []):
        raise SystemExit(f"application service must drop Linux capabilities: {name}")
    if "no-new-privileges:true" not in service.get("security_opt", []):
        raise SystemExit(f"application service must deny privilege escalation: {name}")
    logging = service.get("logging", {})
    options = logging.get("options", {})
    if options.get("max-size") != "10m" or options.get("max-file") != "3":
        raise SystemExit(f"application service must bound local logs: {name}")
for name in ("postgres", "redis", "mqtt-mtls", "turn"):
    options = services[name].get("logging", {}).get("options", {})
    if options.get("max-size") != "10m" or options.get("max-file") != "3":
        raise SystemExit(f"single-node dependency must bound local logs: {name}")

if services["gateway-seed"].get("restart") not in ("no", "\"no\""):
    raise SystemExit("database seed must remain a one-shot container")
for name in ("gateway-api", "rtc-signaling"):
    if services[name].get("restart") != "unless-stopped":
        raise SystemExit(f"long-running application service lacks restart policy: {name}")
    if not services[name].get("healthcheck"):
        raise SystemExit(f"long-running application service lacks healthcheck: {name}")

expected_networks = {
    "gateway-seed": {"private"},
    "gateway-api": {"private", "application", "edge"},
    "rtc-signaling": {"application", "edge"},
}
for name, expected in expected_networks.items():
    actual = set(services[name].get("networks", {}))
    if actual != expected:
        raise SystemExit(f"unexpected network boundary for {name}: {sorted(actual)}")
if not config.get("networks", {}).get("private", {}).get("internal"):
    raise SystemExit("state network must remain internal")
if not config.get("networks", {}).get("application", {}).get("internal"):
    raise SystemExit("application network must remain internal")

for name, published in (("gateway-api", "24083"), ("rtc-signaling", "28084")):
    ports = services[name].get("ports", [])
    if len(ports) != 1 or ports[0].get("host_ip") != "127.0.0.1" or str(ports[0].get("published")) != published:
        raise SystemExit(f"{name} must default to its isolated loopback validation port")

gateway_volumes = services["gateway-api"].get("volumes", [])
if not any(volume.get("target") == "/run/explorex" and volume.get("read_only") for volume in gateway_volumes):
    raise SystemExit("Gateway runtime material must be mounted read-only")
signal_environment = services["rtc-signaling"].get("environment", {})
if any(key in signal_environment for key in ("DATABASE_URL", "REDIS_URL", "DOMESTIC_CA_KEY_PATH")):
    raise SystemExit("signaling must not receive Gateway or state credentials")

if "/usr/local/bin/explorex-signal" not in dockerfile:
    raise SystemExit("runtime image must package the signaling binary")

for text, markers, label in (
    (stack_example, ("CN_STAGING_POSTGRES_DATABASE=gateway_cn_single_node", "CN_STAGING_MQTTS_PORT=61884", "CN_STAGING_TURN_RELAY_MIN_PORT=49360"), "stack env"),
    (gateway_example, ("/gateway_cn_single_node?sslmode=disable", "mqtts://cn-staging.example.invalid:61884"), "Gateway env"),
    (signal_example, ("ws://cn-staging.example.invalid:28084", "EXPLOREX_SIGNAL_ICE_SERVERS_JSON=[]"), "signaling env"),
    (composition_example, ("endpoint: mqtts://mqtt-mtls:8883", "endpoint: http://rtc-signaling:18084"), "composition"),
):
    for marker in markers:
        if marker not in text:
            raise SystemExit(f"{label} example is inconsistent: missing {marker}")
PY

echo "domestic single-node compose contract: ok"
