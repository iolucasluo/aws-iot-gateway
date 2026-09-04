#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd)
BASE_COMPOSE="$SCRIPT_DIR/compose.yaml"
APP_COMPOSE="$SCRIPT_DIR/compose.single-node.yaml"
CLOUD_COMPOSE="$SCRIPT_DIR/compose.cloud-staging.yaml"
EDGE_CONFIG="$SCRIPT_DIR/Caddyfile.cloud-staging"
CLOUD_STACK_EXAMPLE="$SCRIPT_DIR/cloud-staging.stack.env.example"
CLOUD_GATEWAY_EXAMPLE="$SCRIPT_DIR/gateway-cloud-staging.env.example"
CLOUD_COMPOSITION_EXAMPLE="$SCRIPT_DIR/composition.cloud-staging.example.yaml"
CONTRACT_DIR=$(mktemp -d)
trap 'rm -rf "$CONTRACT_DIR"' EXIT

for path in "$BASE_COMPOSE" "$APP_COMPOSE" "$CLOUD_COMPOSE" "$EDGE_CONFIG" \
  "$CLOUD_STACK_EXAMPLE" "$CLOUD_GATEWAY_EXAMPLE" "$CLOUD_COMPOSITION_EXAMPLE"; do
  test -f "$path" || {
    echo "missing cloud staging contract input: $path" >&2
    exit 1
  }
done

mkdir -p "$CONTRACT_DIR/runtime"
touch "$CONTRACT_DIR/runtime/mosquitto.acl"
cat >"$CONTRACT_DIR/gateway.env" <<'EOF'
GATEWAY_ENVIRONMENT=development
GATEWAY_PROVIDER_MODE=domestic-dev
DATABASE_URL=postgres://gateway:contract-only-postgres-secret@postgres:5432/gateway_cn?sslmode=disable
REDIS_URL=redis://:contract-only-redis-secret@redis:6379/1
EOF
cat >"$CONTRACT_DIR/signal.env" <<'EOF'
EXPLOREX_SIGNAL_PUBLIC_BASE_URL=ws://rtc-staging.example.invalid
EOF
cat >"$CONTRACT_DIR/stack.env" <<EOF
CN_STAGING_STATE_BIND_ADDRESS=127.0.0.1
CN_STAGING_EDGE_BIND_ADDRESS=0.0.0.0
CN_STAGING_POSTGRES_USER=gateway
CN_STAGING_POSTGRES_PASSWORD=contract-only-postgres-secret
CN_STAGING_POSTGRES_DATABASE=gateway_cn
CN_STAGING_REDIS_PASSWORD=contract-only-redis-secret
CN_STAGING_MQTT_TLS_DIR=$CONTRACT_DIR/runtime
CN_STAGING_MQTT_ACL_FILE=$CONTRACT_DIR/runtime/mosquitto.acl
CN_STAGING_MINIO_ROOT_USER=contract-only-minio
CN_STAGING_MINIO_ROOT_PASSWORD=contract-only-minio-secret
CN_STAGING_TURN_REALM=turn-staging.example.invalid
CN_STAGING_TURN_USER=turn-contract
CN_STAGING_TURN_PASSWORD=contract-only-turn-secret
CN_STAGING_TURN_RELAY_MIN_PORT=49360
CN_STAGING_TURN_RELAY_MAX_PORT=49400
CN_STAGING_GATEWAY_IMAGE=explorex-cn-gateway:contract
CN_STAGING_GATEWAY_ENV_FILE=$CONTRACT_DIR/gateway.env
CN_STAGING_SIGNAL_ENV_FILE=$CONTRACT_DIR/signal.env
CN_STAGING_RUNTIME_DIR=$CONTRACT_DIR/runtime
CN_STAGING_REALTIME_AUTH_TOKEN=contract-only-signal-secret
CN_CLOUD_PUBLIC_IP=203.0.113.10
EOF

rendered=$(docker compose --env-file "$CONTRACT_DIR/stack.env" \
  -f "$BASE_COMPOSE" -f "$APP_COMPOSE" -f "$CLOUD_COMPOSE" \
  --profile single-node config --format json)

preflight=$(cd "$ROOT" && \
  GATEWAY_ENVIRONMENT=development \
  GATEWAY_PROVIDER_COMPOSITION_PATH="$CLOUD_COMPOSITION_EXAMPLE" \
  go run ./cmd/provider-composition-preflight)
grep -Fq 'id=cn-cloud-staging environment=development region=cn' <<<"$preflight"
grep -Fq 'instance=object.aliyun_oss capability=object driver=aliyun_oss' <<<"$preflight"

python3 - "$rendered" "$EDGE_CONFIG" "$CLOUD_STACK_EXAMPLE" "$CLOUD_GATEWAY_EXAMPLE" "$CLOUD_COMPOSITION_EXAMPLE" <<'PY'
import json
import pathlib
import sys

config = json.loads(sys.argv[1])
edge_config = pathlib.Path(sys.argv[2]).read_text()
stack_example = pathlib.Path(sys.argv[3]).read_text()
gateway_example = pathlib.Path(sys.argv[4]).read_text()
composition_example = pathlib.Path(sys.argv[5]).read_text()
services = config.get("services", {})

if config.get("name") != "explorex-cn-cloud-staging":
    raise SystemExit("cloud overlay must use an isolated Compose project name")
if "edge-proxy" not in services:
    raise SystemExit("cloud overlay must provide one Host-routing edge proxy")
for name in ("gateway-api", "rtc-signaling"):
    if services[name].get("ports"):
        raise SystemExit(f"{name} must not publish a direct public port in cloud staging")
proxy_ports = {(str(item.get("published")), item.get("protocol")) for item in services["edge-proxy"].get("ports", [])}
if not {("80", "tcp"), ("443", "tcp")}.issubset(proxy_ports):
    raise SystemExit("edge proxy must publish TCP 80 and 443")
if not {"application", "edge"}.issubset(services["edge-proxy"].get("networks", {})):
    raise SystemExit("edge proxy must join only the application edge boundary")
if "api-staging.livpet.top" not in edge_config or "rtc-staging.livpet.top" not in edge_config:
    raise SystemExit("edge config must route both public staging hosts")
if "gateway-api:14080" not in edge_config or "rtc-signaling:18084" not in edge_config:
    raise SystemExit("edge config routes do not target internal application services")

mqtt_ports = services["mqtt-mtls"].get("ports", [])
if len(mqtt_ports) != 1 or str(mqtt_ports[0].get("published")) != "8883":
    raise SystemExit("cloud MQTT mTLS must publish only 8883")
turn_ports = services["turn"].get("ports", [])
published = {(str(item.get("published")), item.get("protocol")) for item in turn_ports}
if ("3478", "udp") not in published or ("3478", "tcp") not in published:
    raise SystemExit("cloud TURN must publish TCP and UDP 3478")
command = " ".join(services["turn"].get("command", []))
if "--external-ip=203.0.113.10" not in command:
    raise SystemExit("cloud TURN must advertise the explicit ECS public IP")
if "49360" not in command or "49400" not in command:
    raise SystemExit("cloud TURN relay range must match the security-group contract")

for name in ("postgres", "redis"):
    for item in services[name].get("ports", []):
        if item.get("host_ip") != "127.0.0.1":
            raise SystemExit(f"{name} must remain loopback-only")

for marker in ("CN_STAGING_MQTTS_PORT=8883", "CN_STAGING_TURN_RELAY_MIN_PORT=49360", "CN_CLOUD_PUBLIC_IP="):
    if marker not in stack_example:
        raise SystemExit(f"cloud stack example lacks {marker}")
for marker in ("GATEWAY_OBJECT_PUBLIC_ENDPOINT=", "GATEWAY_OBJECT_BUCKET=", "GATEWAY_OBJECT_ACCESS_KEY_ID=", "GATEWAY_OBJECT_ACCESS_KEY_SECRET="):
    if marker not in gateway_example:
        raise SystemExit(f"cloud Gateway example lacks {marker}")
for marker in ("driver: aliyun_oss", "media_object: object.aliyun_oss", "credential_ref: env://GATEWAY_OBJECT_CREDENTIALS", "bucket_ref: env://GATEWAY_OBJECT_BUCKET"):
    if marker not in composition_example:
        raise SystemExit(f"cloud composition example lacks {marker}")
PY

echo "cn cloud staging compose contract: ok"
