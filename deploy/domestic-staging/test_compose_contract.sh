#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
COMPOSE_FILE="$SCRIPT_DIR/compose.yaml"
ENV_FILE=$(mktemp)
trap 'rm -f "$ENV_FILE"' EXIT

cat >"$ENV_FILE" <<'EOF'
CN_STAGING_POSTGRES_USER=gateway
CN_STAGING_POSTGRES_PASSWORD=contract-only-postgres-secret
CN_STAGING_POSTGRES_DATABASE=aws_iot_gateway_cn_staging
CN_STAGING_STATE_BIND_ADDRESS=127.0.0.1
CN_STAGING_EDGE_BIND_ADDRESS=127.0.0.1
CN_STAGING_POSTGRES_PORT=55432
CN_STAGING_REDIS_PASSWORD=contract-only-redis-secret
CN_STAGING_REDIS_PORT=56379
CN_STAGING_MQTT_PORT=51883
CN_STAGING_TURN_REALM=cn-staging.local
CN_STAGING_TURN_USER=turn-contract
CN_STAGING_TURN_PASSWORD=contract-only-turn-secret
CN_STAGING_TURN_UDP_PORT=53478
CN_STAGING_TURN_TCP_PORT=53478
CN_STAGING_MINIO_API_PORT=59000
CN_STAGING_MINIO_CONSOLE_PORT=59001
CN_STAGING_MINIO_ROOT_USER=contract-only-minio
CN_STAGING_MINIO_ROOT_PASSWORD=contract-only-minio-secret
EOF

test -f "$COMPOSE_FILE" || {
  echo "missing domestic staging compose: $COMPOSE_FILE" >&2
  exit 1
}

rendered=$(docker compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" config --format json)
rendered_with_object=$(docker compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" --profile object config --format json)
python3 - "$rendered" "$rendered_with_object" <<'PY'
import json
import sys

config = json.loads(sys.argv[1])
services = config.get("services", {})
required = {"postgres", "redis", "mqtt", "turn"}
missing = required - services.keys()
if missing:
    raise SystemExit(f"missing required services: {sorted(missing)}")

for name in required:
    if not services[name].get("healthcheck"):
        raise SystemExit(f"service lacks healthcheck: {name}")

for name in ("postgres", "redis"):
    ports = services[name].get("ports", [])
    if not ports or any("127.0.0.1" not in str(port) for port in ports):
        raise SystemExit(f"stateful service must bind loopback only: {name}")

for name in ("postgres", "redis"):
    networks = services[name].get("networks", {})
    if "private" not in networks or "host-access" not in networks:
        raise SystemExit(f"stateful service must join private and host-access networks: {name}")
    if "edge" in networks:
        raise SystemExit(f"stateful service must not join edge network: {name}")

for name in ("mqtt", "turn"):
    networks = services[name].get("networks", {})
    if "edge" not in networks or "private" not in networks:
        raise SystemExit(f"edge service must join both private and edge networks: {name}")
    if any(port.get("host_ip") != "127.0.0.1" for port in services[name].get("ports", [])):
        raise SystemExit(f"edge service default bind must be loopback-only: {name}")

networks = config.get("networks", {})
if not networks.get("private", {}).get("internal"):
    raise SystemExit("private network must be internal")
if networks.get("edge", {}).get("internal"):
    raise SystemExit("edge network must permit published loopback ports")
if networks.get("host-access", {}).get("internal"):
    raise SystemExit("host-access network must permit published loopback ports")

turn_ports = services["turn"].get("ports", [])
if not any(str(port.get("published")) == "49160" for port in turn_ports):
    raise SystemExit("TURN relay port range must be published for WebRTC checks")

if "gateway" in services or "gateway-api" in services:
    raise SystemExit("Phase 1 must not claim the AWS-bound Gateway binary is domestic-ready")

object_services = json.loads(sys.argv[2]).get("services", {})
minio = object_services.get("minio")
if not minio or not minio.get("healthcheck"):
    raise SystemExit("object profile must provide a health-checked MinIO adapter harness")
if any("127.0.0.1" not in str(port) for port in minio.get("ports", [])):
    raise SystemExit("MinIO test harness must bind loopback only")
PY

if grep -Eqi '(aws|amazon|sqs|kvs|access[_-]?key|secret[_-]?key)' "$COMPOSE_FILE"; then
  echo "domestic staging compose must not contain AWS or cloud credentials" >&2
  exit 1
fi

echo "domestic staging compose contract: ok"
