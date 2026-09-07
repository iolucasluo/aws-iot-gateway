#!/usr/bin/env bash
set -euo pipefail
umask 077

if [[ $# -ne 1 || "$1" != /* || "$1" == "/" ]]; then
  echo "usage: $0 /absolute/operator-root" >&2
  exit 2
fi

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
DEPLOY_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
OPERATOR_ROOT=$1

required=(
  CN_CLOUD_PUBLIC_IP
  CN_CLOUD_OSS_BUCKET
  CN_CLOUD_OSS_ACCESS_KEY_ID
  CN_CLOUD_OSS_ACCESS_KEY_SECRET
  CN_CLOUD_GATEWAY_IMAGE
)
for name in "${required[@]}"; do
  if [[ -z "${!name:-}" || "${!name}" == *$'\n'* || "${!name}" == *$'\r'* ]]; then
    echo "$name is required and must be a single line" >&2
    exit 2
  fi
done
if [[ ! "$CN_CLOUD_PUBLIC_IP" =~ ^[0-9]+(\.[0-9]+){3}$ ]]; then
  echo "CN_CLOUD_PUBLIC_IP must be an IPv4 address" >&2
  exit 2
fi
IFS=. read -r -a public_octets <<<"$CN_CLOUD_PUBLIC_IP"
for octet in "${public_octets[@]}"; do
  if ((10#$octet > 255)); then
    echo "CN_CLOUD_PUBLIC_IP contains an invalid octet" >&2
    exit 2
  fi
done
if [[ ! "$CN_CLOUD_OSS_BUCKET" =~ ^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$ ]]; then
  echo "CN_CLOUD_OSS_BUCKET is invalid" >&2
  exit 2
fi
if [[ ! "$CN_CLOUD_GATEWAY_IMAGE" =~ ^[A-Za-z0-9._/:@-]+$ ]]; then
  echo "CN_CLOUD_GATEWAY_IMAGE is invalid" >&2
  exit 2
fi

CONFIG_DIR="$OPERATOR_ROOT/config"
RUNTIME_DIR="$OPERATOR_ROOT/runtime"
MQTT_TLS_DIR="$RUNTIME_DIR/mqtt-tls"
for path in "$CONFIG_DIR/stack.env" "$CONFIG_DIR/gateway.env" "$CONFIG_DIR/signal.env" \
  "$CONFIG_DIR/mosquitto.acl" "$RUNTIME_DIR/composition.yaml" "$RUNTIME_DIR/time-signer.pem" \
  "$RUNTIME_DIR/time-signer-public.der"; do
  if [[ -e "$path" ]]; then
    echo "refusing to overwrite existing operator artifact: $path" >&2
    exit 1
  fi
done

mkdir -p "$CONFIG_DIR" "$RUNTIME_DIR"
chmod 700 "$CONFIG_DIR" "$RUNTIME_DIR"

POSTGRES_PASSWORD=$(openssl rand -hex 24)
REDIS_PASSWORD=$(openssl rand -hex 24)
DISABLED_MINIO_PASSWORD=$(openssl rand -hex 24)
TURN_USER=turn-cn-cloud
TURN_PASSWORD=$(openssl rand -hex 24)
REALTIME_TOKEN=$(openssl rand -hex 32)
API_KEY=demo-cn-cloud
API_SECRET=$(openssl rand -hex 32)

MQTT_TLS_SERVER_IP="$CN_CLOUD_PUBLIC_IP" \
MQTT_TLS_SERVER_DNS=mqtt-staging.livpet.top \
MQTT_TLS_VALIDITY_DAYS=365 \
MQTT_TLS_INCLUDE_TEST_CLIENTS=0 \
  "$SCRIPT_DIR/generate_mtls_test_certs.sh" "$MQTT_TLS_DIR" >/dev/null
openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 \
  -out "$RUNTIME_DIR/time-signer.pem" >/dev/null 2>&1
openssl pkey -in "$RUNTIME_DIR/time-signer.pem" -pubout -outform DER \
  -out "$RUNTIME_DIR/time-signer-public.der" >/dev/null 2>&1

cp "$DEPLOY_DIR/composition.cloud-staging.example.yaml" "$RUNTIME_DIR/composition.yaml"
cp "$DEPLOY_DIR/mosquitto.mtls.acl.example" "$CONFIG_DIR/mosquitto.acl"

printf '%s\n' \
  'CN_STAGING_STATE_BIND_ADDRESS=127.0.0.1' \
  'CN_STAGING_EDGE_BIND_ADDRESS=0.0.0.0' \
  'CN_STAGING_POSTGRES_USER=gateway' \
  "CN_STAGING_POSTGRES_PASSWORD=$POSTGRES_PASSWORD" \
  'CN_STAGING_POSTGRES_DATABASE=gateway_cn_cloud_staging' \
  'CN_STAGING_POSTGRES_PORT=55432' \
  "CN_STAGING_REDIS_PASSWORD=$REDIS_PASSWORD" \
  'CN_STAGING_REDIS_PORT=56379' \
  'CN_STAGING_MINIO_ROOT_USER=disabled' \
  "CN_STAGING_MINIO_ROOT_PASSWORD=$DISABLED_MINIO_PASSWORD" \
  'CN_STAGING_MQTTS_PORT=8883' \
  "CN_STAGING_MQTT_TLS_DIR=$MQTT_TLS_DIR" \
  "CN_STAGING_MQTT_ACL_FILE=$CONFIG_DIR/mosquitto.acl" \
  'CN_STAGING_TURN_REALM=turn-staging.livpet.top' \
  "CN_STAGING_TURN_USER=$TURN_USER" \
  "CN_STAGING_TURN_PASSWORD=$TURN_PASSWORD" \
  'CN_STAGING_TURN_UDP_PORT=3478' \
  'CN_STAGING_TURN_TCP_PORT=3478' \
  'CN_STAGING_TURN_RELAY_MIN_PORT=49360' \
  'CN_STAGING_TURN_RELAY_MAX_PORT=49400' \
  "CN_CLOUD_PUBLIC_IP=$CN_CLOUD_PUBLIC_IP" \
  "CN_STAGING_GATEWAY_IMAGE=$CN_CLOUD_GATEWAY_IMAGE" \
  "CN_STAGING_GATEWAY_ENV_FILE=$CONFIG_DIR/gateway.env" \
  "CN_STAGING_SIGNAL_ENV_FILE=$CONFIG_DIR/signal.env" \
  "CN_STAGING_RUNTIME_DIR=$RUNTIME_DIR" \
  "CN_STAGING_REALTIME_AUTH_TOKEN=$REALTIME_TOKEN" \
  >"$CONFIG_DIR/stack.env"

printf '%s\n' \
  'GATEWAY_ENVIRONMENT=development' \
  'GATEWAY_PROVIDER_MODE=domestic-dev' \
  'GATEWAY_PROVIDER_COMPOSITION_PATH=/run/explorex/composition.yaml' \
  "DATABASE_URL=postgres://gateway:$POSTGRES_PASSWORD@postgres:5432/gateway_cn_cloud_staging?sslmode=disable" \
  "REDIS_URL=redis://:$REDIS_PASSWORD@redis:6379/1" \
  "INTERNAL_APPS_JSON='[{\"id\":\"demo-cn-cloud\",\"api_key\":\"$API_KEY\",\"api_secret\":\"$API_SECRET\"}]'" \
  'TIME_BOOTSTRAP_KEY_ID=time-cn-cloud-staging-g1' \
  'TIME_BOOTSTRAP_SIGNER=local' \
  'TIME_BOOTSTRAP_LOCAL_PRIVATE_KEY_PATH=/run/explorex/time-signer.pem' \
  'DOMESTIC_MQTT_ENDPOINT=mqtts://mqtt-staging.livpet.top:8883' \
  'DOMESTIC_MQTT_TOPIC_PREFIX=cn-staging' \
  'DOMESTIC_MQTT_CLIENT_ID=gateway-cn-staging' \
  'DOMESTIC_MQTT_CLIENT_CERT_PATH=/run/explorex/mqtt-tls/gateway-cn-staging.crt' \
  'DOMESTIC_MQTT_CLIENT_KEY_PATH=/run/explorex/mqtt-tls/gateway-cn-staging.key' \
  'DOMESTIC_CA_CERT_PATH=/run/explorex/mqtt-tls/ca.crt' \
  'DOMESTIC_CA_KEY_PATH=/run/explorex/mqtt-tls/ca.key' \
  'DOMESTIC_SIGNER_PRIVATE_KEY_PATH=/run/explorex/time-signer.pem' \
  'DOMESTIC_SIGNER_KEY_ID=time-cn-cloud-staging-g1' \
  'GATEWAY_OBJECT_PUBLIC_ENDPOINT=https://oss-cn-shenzhen.aliyuncs.com' \
  "GATEWAY_OBJECT_BUCKET=$CN_CLOUD_OSS_BUCKET" \
  "GATEWAY_OBJECT_ACCESS_KEY_ID=$CN_CLOUD_OSS_ACCESS_KEY_ID" \
  "GATEWAY_OBJECT_ACCESS_KEY_SECRET=$CN_CLOUD_OSS_ACCESS_KEY_SECRET" \
  >"$CONFIG_DIR/gateway.env"

printf '%s\n' \
  'EXPLOREX_SIGNAL_PUBLIC_BASE_URL=ws://rtc-staging.livpet.top' \
  "EXPLOREX_SIGNAL_ICE_SERVERS_JSON='[{\"urls\":[\"stun:turn-staging.livpet.top:3478\",\"turn:turn-staging.livpet.top:3478?transport=udp\",\"turn:turn-staging.livpet.top:3478?transport=tcp\"],\"username\":\"$TURN_USER\",\"credential\":\"$TURN_PASSWORD\"}]'" \
  >"$CONFIG_DIR/signal.env"

chmod 600 "$CONFIG_DIR/stack.env" "$CONFIG_DIR/gateway.env" "$CONFIG_DIR/signal.env"
chmod 640 "$CONFIG_DIR/mosquitto.acl"
chmod 444 "$RUNTIME_DIR/composition.yaml" "$RUNTIME_DIR/time-signer-public.der" "$MQTT_TLS_DIR"/*.crt
chmod 400 "$RUNTIME_DIR/time-signer.pem" "$MQTT_TLS_DIR/ca.key"
chmod 640 "$MQTT_TLS_DIR/server.key"
chmod 440 "$MQTT_TLS_DIR/gateway-cn-staging.key"
if [[ "$(id -u)" -eq 0 ]]; then
  chown 10001:10001 "$RUNTIME_DIR"
  chown 10001:1883 "$MQTT_TLS_DIR"
  chown 10001:10001 "$RUNTIME_DIR/time-signer.pem" "$MQTT_TLS_DIR/ca.key"
  chown 10001:1883 "$MQTT_TLS_DIR/gateway-cn-staging.key"
  chown 0:1883 "$MQTT_TLS_DIR/server.key"
  chown 0:1883 "$CONFIG_DIR/mosquitto.acl"
  chmod 750 "$RUNTIME_DIR" "$MQTT_TLS_DIR"
fi

printf 'prepared cn-cloud-staging operator files under %s\n' "$OPERATOR_ROOT"
