#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd)
CERT_SCRIPT="$SCRIPT_DIR/scripts/generate_mtls_test_certs.sh"

test -x "$CERT_SCRIPT" || {
  echo "missing executable MQTT certificate generator: $CERT_SCRIPT" >&2
  exit 1
}
bash -n "$CERT_SCRIPT"
CERT_DIR=$(mktemp -d)
trap 'rm -rf "$CERT_DIR"' EXIT
bash "$CERT_SCRIPT" "$CERT_DIR"
for file in ca.crt server.crt server.key gateway-cn-staging.crt gateway-cn-staging.key feeder-cn-staging.crt feeder-cn-staging.key unauthorized-cn-staging.crt unauthorized-cn-staging.key; do
  test -f "$CERT_DIR/$file" || {
    echo "certificate generator did not create $file" >&2
    exit 1
  }
done
if bash "$CERT_SCRIPT" "$CERT_DIR" >/dev/null 2>&1; then
  echo "certificate generator overwrote an existing CA private key" >&2
  exit 1
fi
if MQTT_TLS_SERVER_IP=not-an-ip bash "$CERT_SCRIPT" "$CERT_DIR/invalid-ip" >/dev/null 2>&1; then
  echo "certificate generator accepted an invalid server IP" >&2
  exit 1
fi
MQTT_TLS_SERVER_IP=10.0.0.7 bash "$CERT_SCRIPT" "$CERT_DIR/lan-ip" >/dev/null
openssl x509 -in "$CERT_DIR/lan-ip/server.crt" -noout -text | grep -Fq "IP Address:10.0.0.7"

(cd "$ROOT" && go test -race -count=1 ./internal/runtime ./internal/platform/mqttbroker ./cmd/mqtt-provider-smoke)

python3 - "$SCRIPT_DIR/mosquitto.mtls.conf" "$SCRIPT_DIR/mosquitto.mtls.acl.example" <<'PY'
import pathlib
import sys

config = pathlib.Path(sys.argv[1]).read_text()
acl = pathlib.Path(sys.argv[2]).read_text()
for marker in (
    "listener 8883",
    "allow_anonymous false",
    "require_certificate true",
    "use_identity_as_username true",
    "cafile /mosquitto/certs/ca.crt",
    "certfile /mosquitto/certs/server.crt",
    "keyfile /mosquitto/certs/server.key",
    "acl_file /mosquitto/config/acl",
):
    if marker not in config:
        raise SystemExit(f"MQTT TLS config missing {marker}")
for marker in (
    "user gateway-cn-staging",
    "topic write cmd/cn-staging/+/req",
    "topic read cmd/cn-staging/+/res",
    "user feeder-cn-staging",
    "topic read cmd/cn-staging/+/req",
    "topic write cmd/cn-staging/+/res",
):
    if marker not in acl:
        raise SystemExit(f"MQTT ACL example missing {marker}")
if "topic read #" in acl or "topic write #" in acl:
    raise SystemExit("MQTT ACL example grants a global wildcard")
print("MQTT mTLS/ACL contract: ok")
PY

grep -Fq '/mosquitto/certs/gateway-cn-staging.crt' "$SCRIPT_DIR/compose.yaml"
grep -Fq '/mosquitto/certs/gateway-cn-staging.key' "$SCRIPT_DIR/compose.yaml"
