#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 || "$1" != /* || "$1" == "/" ]]; then
  echo "usage: $0 /absolute/operator-owned/output-directory" >&2
  exit 2
fi

OUTPUT_DIR=$1
INCLUDE_TEST_CLIENTS=${MQTT_TLS_INCLUDE_TEST_CLIENTS:-1}
if [[ "$INCLUDE_TEST_CLIENTS" != "0" && "$INCLUDE_TEST_CLIENTS" != "1" ]]; then
  echo "MQTT_TLS_INCLUDE_TEST_CLIENTS must be 0 or 1" >&2
  exit 2
fi
artifacts=(ca.key ca.crt server.key server.crt gateway-cn-staging.key gateway-cn-staging.crt)
if [[ "$INCLUDE_TEST_CLIENTS" == "1" ]]; then
  artifacts+=(feeder-cn-staging.key feeder-cn-staging.crt unauthorized-cn-staging.key unauthorized-cn-staging.crt)
fi
for artifact in "${artifacts[@]}"; do
  if [[ -e "$OUTPUT_DIR/$artifact" ]]; then
    echo "refusing to overwrite an existing MQTT TLS artifact: $OUTPUT_DIR/$artifact" >&2
    exit 1
  fi
done
SERVER_IP=${MQTT_TLS_SERVER_IP:-127.0.0.1}
SERVER_DNS=${MQTT_TLS_SERVER_DNS:-localhost}
VALIDITY_DAYS=${MQTT_TLS_VALIDITY_DAYS:-2}
if [[ ! "$SERVER_IP" =~ ^[0-9]+(\.[0-9]+){3}$ ]]; then
  echo "MQTT_TLS_SERVER_IP must be an IPv4 address" >&2
  exit 2
fi
IFS=. read -r -a server_octets <<< "$SERVER_IP"
for octet in "${server_octets[@]}"; do
  if ((10#$octet > 255)); then
    echo "MQTT_TLS_SERVER_IP contains an invalid octet" >&2
    exit 2
  fi
done
if [[ ! "$SERVER_DNS" =~ ^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$ ]] || [[ "$SERVER_DNS" == *..* ]]; then
  echo "MQTT_TLS_SERVER_DNS must be a hostname" >&2
  exit 2
fi
if [[ ! "$VALIDITY_DAYS" =~ ^[0-9]+$ ]] || ((10#$VALIDITY_DAYS < 1 || 10#$VALIDITY_DAYS > 825)); then
  echo "MQTT_TLS_VALIDITY_DAYS must be between 1 and 825" >&2
  exit 2
fi
SERVER_SAN_IPS=IP:127.0.0.1
if [[ "$SERVER_IP" != "127.0.0.1" ]]; then
  SERVER_SAN_IPS+=",IP:${SERVER_IP}"
fi
mkdir -p "$OUTPUT_DIR"
chmod 700 "$OUTPUT_DIR"
cd "$OUTPUT_DIR"

openssl req -x509 -newkey rsa:2048 -nodes -days "$VALIDITY_DAYS" \
  -subj /CN=ExploreX-CN-Staging-Test-CA \
  -keyout ca.key -out ca.crt >/dev/null 2>&1

cat >server.ext <<EOF
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=${SERVER_SAN_IPS},DNS:localhost,DNS:mqtt-mtls,DNS:${SERVER_DNS}
EOF
cat >client.ext <<'EOF'
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=clientAuth
EOF

client_names=(gateway-cn-staging)
if [[ "$INCLUDE_TEST_CLIENTS" == "1" ]]; then
  client_names+=(feeder-cn-staging unauthorized-cn-staging)
fi
for name in "${client_names[@]}"; do
  openssl req -newkey rsa:2048 -nodes -subj "/CN=$name" \
    -keyout "$name.key" -out "$name.csr" >/dev/null 2>&1
  openssl x509 -req -days "$VALIDITY_DAYS" -in "$name.csr" -CA ca.crt -CAkey ca.key \
    -CAcreateserial -out "$name.crt" -extfile client.ext >/dev/null 2>&1
done

openssl req -newkey rsa:2048 -nodes -subj /CN=cn-staging-mqtt-server \
  -keyout server.key -out server.csr >/dev/null 2>&1
openssl x509 -req -days "$VALIDITY_DAYS" -in server.csr -CA ca.crt -CAkey ca.key \
  -CAcreateserial -out server.crt -extfile server.ext >/dev/null 2>&1

chmod 755 "$OUTPUT_DIR"
chmod 600 ca.key
if [[ "$INCLUDE_TEST_CLIENTS" == "1" ]]; then
  chmod 600 feeder-cn-staging.key unauthorized-cn-staging.key
fi
chmod 640 server.key gateway-cn-staging.key
chmod 644 ca.crt server.crt gateway-cn-staging.crt
if [[ "$INCLUDE_TEST_CLIENTS" == "1" ]]; then
  chmod 644 feeder-cn-staging.crt unauthorized-cn-staging.crt
fi
if [[ "$(id -u)" -eq 0 ]]; then
  chgrp 1883 "$OUTPUT_DIR" server.key gateway-cn-staging.key server.crt gateway-cn-staging.crt ca.crt
  chmod 750 "$OUTPUT_DIR"
fi
rm -f ./*.csr server.ext client.ext ca.srl
printf 'generated MQTT mTLS test certificates in %s\n' "$OUTPUT_DIR"
