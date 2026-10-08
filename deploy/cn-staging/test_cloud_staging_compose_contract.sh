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
CLOUD_PLATFORM_EXAMPLE="$SCRIPT_DIR/platform.cloud-staging.env.example"
CONTRACT_DIR=$(mktemp -d)
trap 'rm -rf "$CONTRACT_DIR"' EXIT

for path in "$BASE_COMPOSE" "$APP_COMPOSE" "$CLOUD_COMPOSE" "$EDGE_CONFIG" \
  "$CLOUD_STACK_EXAMPLE" "$CLOUD_GATEWAY_EXAMPLE" "$CLOUD_COMPOSITION_EXAMPLE" \
  "$CLOUD_PLATFORM_EXAMPLE"; do
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
cat >"$CONTRACT_DIR/platform.env" <<'EOF'
GATEWAY_ENVIRONMENT=development
DATABASE_URL=postgres://gateway:contract-only-postgres-secret@postgres:5432/gateway_cn?sslmode=disable
REDIS_URL=redis://:contract-only-redis-secret@redis:6379/1
PLATFORM_CREDENTIAL_KEK=Y29udHJhY3Qtb25seS1rZWstMzItYnl0ZXMtYmFzZTY0
PLATFORM_CREDENTIAL_KEK_ID=kek-v1
PLATFORM_CORS_ALLOWED_ORIGINS=https://admin-staging.example.invalid
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
CN_STAGING_PLATFORM_ENV_FILE=$CONTRACT_DIR/platform.env
CN_STAGING_ADMIN_CONSOLE_IMAGE=explorex-admin-console:contract
CN_STAGING_DEVELOPER_PORTAL_IMAGE=explorex-developer-portal:contract
CN_STAGING_MANUFACTURER_PORTAL_IMAGE=explorex-manufacturer-portal:contract
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

python3 - "$rendered" "$EDGE_CONFIG" "$CLOUD_STACK_EXAMPLE" "$CLOUD_GATEWAY_EXAMPLE" "$CLOUD_COMPOSITION_EXAMPLE" "$CLOUD_PLATFORM_EXAMPLE" <<'PY'
import json
import pathlib
import sys

config = json.loads(sys.argv[1])
edge_config = pathlib.Path(sys.argv[2]).read_text()
stack_example = pathlib.Path(sys.argv[3]).read_text()
gateway_example = pathlib.Path(sys.argv[4]).read_text()
composition_example = pathlib.Path(sys.argv[5]).read_text()
platform_example = pathlib.Path(sys.argv[6]).read_text()
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
if services["edge-proxy"].get("cap_drop") != ["ALL"] or services["edge-proxy"].get("cap_add") != ["NET_BIND_SERVICE"]:
    raise SystemExit("edge proxy must keep only the Caddy binary file capability required for exec")
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
for marker in ("PET_PHOTO_PUBLIC_ORIGIN=https://api-staging.livpet.top",):
    if marker not in gateway_example:
        raise SystemExit(f"cloud Gateway example lacks {marker}")
for marker in ("GATEWAY_OBJECT_PUBLIC_ENDPOINT=", "GATEWAY_OBJECT_BUCKET=", "GATEWAY_OBJECT_ACCESS_KEY_ID=", "GATEWAY_OBJECT_ACCESS_KEY_SECRET="):
    if marker not in gateway_example:
        raise SystemExit(f"cloud Gateway example lacks {marker}")
for marker in ("driver: aliyun_oss", "media_object: object.aliyun_oss", "credential_ref: env://GATEWAY_OBJECT_CREDENTIALS", "bucket_ref: env://GATEWAY_OBJECT_BUCKET"):
    if marker not in composition_example:
        raise SystemExit(f"cloud composition example lacks {marker}")

# 平台面：与 gateway-api 同镜像的第二个进程；不得发布公网端口。
platform = services.get("explorex-platform-api")
if platform is None:
    raise SystemExit("cloud overlay must run the ExploreX platform API")
if platform.get("ports"):
    raise SystemExit("explorex-platform-api must not publish a direct public port")
if platform.get("image") != services["gateway-api"].get("image"):
    raise SystemExit("platform API must reuse the gateway image (same module)")
command = " ".join(platform.get("command", []))
if "explorex-platform-api" not in command:
    raise SystemExit("platform API command must run the second binary")
if not {"private", "application"}.issubset(platform.get("networks", {})):
    raise SystemExit("platform API must reach postgres/redis and the edge application network")
# docker compose 会把 env_file 内联进 environment，因此按内容而不是按 key 断言。
platform_env = platform.get("environment", {})
if "PLATFORM_CREDENTIAL_KEK" not in platform_env:
    raise SystemExit("platform API must load the platform env file (KEK is missing)")
if platform_env.get("PLATFORM_GATEWAY_INTERNAL_BASE_URL") != "http://gateway-api:14080":
    raise SystemExit("platform API must proxy device I/O to gateway-api")
if platform_env.get("PLATFORM_LISTEN_ADDRESS") != "0.0.0.0:14081":
    raise SystemExit("platform API must listen on the container interface only")

# 三门户：静态 SPA 容器，只加入 application 网络，不发布端口。
for name, marker in (("admin-console", "CN_STAGING_ADMIN_CONSOLE_IMAGE="),
                     ("developer-portal", "CN_STAGING_DEVELOPER_PORTAL_IMAGE="),
                     ("manufacturer-portal", "CN_STAGING_MANUFACTURER_PORTAL_IMAGE=")):
    portal = services.get(name)
    if portal is None:
        raise SystemExit(f"cloud overlay must run {name}")
    if portal.get("ports"):
        raise SystemExit(f"{name} must not publish a direct public port")
    if list(portal.get("networks", {})) != ["application"]:
        raise SystemExit(f"{name} must join only the application network")
    if marker not in stack_example:
        raise SystemExit(f"cloud stack example lacks {marker}")

if "CN_STAGING_PLATFORM_ENV_FILE=" not in stack_example:
    raise SystemExit("cloud stack example lacks CN_STAGING_PLATFORM_ENV_FILE")
for marker in ("PLATFORM_CREDENTIAL_KEK=", "PLATFORM_GATEWAY_INTERNAL_BASE_URL=", "PLATFORM_CORS_ALLOWED_ORIGINS="):
    if marker not in platform_example:
        raise SystemExit(f"platform env example lacks {marker}")

# 门户子域由 edge proxy 反代到门户服务；平台面 API 不单独暴露公网。
for host, target in (("admin-staging.livpet.top", "admin-console:80"),
                     ("developer-staging.livpet.top", "developer-portal:80"),
                     ("manufacturer-staging.livpet.top", "manufacturer-portal:80")):
    if host not in edge_config or target not in edge_config:
        raise SystemExit(f"edge config must route {host} to {target}")
# 注释里可以提到平台面地址；这里只禁止 edge proxy 真的把平台面直接反代出去。
for line in edge_config.splitlines():
    stripped = line.strip()
    if stripped.startswith("reverse_proxy explorex-platform-api"):
        raise SystemExit("platform API must stay behind the portal nginx, not the edge proxy")
PY

echo "cn cloud staging compose contract: ok"
