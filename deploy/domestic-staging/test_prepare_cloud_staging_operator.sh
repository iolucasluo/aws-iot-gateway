#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
PREPARE="$SCRIPT_DIR/scripts/prepare_cloud_staging_operator.sh"
OUTPUT_DIR=$(mktemp -d)/operator
trap 'rm -rf "${OUTPUT_DIR%/operator}"' EXIT

test -x "$PREPARE" || {
  echo "missing executable cloud operator preparation script: $PREPARE" >&2
  exit 1
}
bash -n "$PREPARE"
CN_CLOUD_PUBLIC_IP=203.0.113.10 \
CN_CLOUD_OSS_BUCKET=contract-media \
CN_CLOUD_OSS_ACCESS_KEY_ID=contract-access-key \
CN_CLOUD_OSS_ACCESS_KEY_SECRET=contract-access-secret \
CN_CLOUD_GATEWAY_IMAGE=explorex-cn-gateway:contract-amd64 \
  bash "$PREPARE" "$OUTPUT_DIR"

for path in config/stack.env config/gateway.env config/signal.env config/mosquitto.acl \
  runtime/composition.yaml runtime/time-signer.pem runtime/time-signer-public.der runtime/mqtt-tls/ca.crt \
  runtime/mqtt-tls/ca.key runtime/mqtt-tls/server.crt runtime/mqtt-tls/server.key \
  runtime/mqtt-tls/gateway-cn-staging.crt runtime/mqtt-tls/gateway-cn-staging.key; do
  test -f "$OUTPUT_DIR/$path" || {
    echo "cloud operator preparation did not create $path" >&2
    exit 1
  }
done
for path in runtime/mqtt-tls/feeder-cn-staging.key runtime/mqtt-tls/unauthorized-cn-staging.key; do
  if [[ -e "$OUTPUT_DIR/$path" ]]; then
    echo "cloud operator preparation retained test-only private key $path" >&2
    exit 1
  fi
done

python3 - "$OUTPUT_DIR" <<'PY'
import pathlib
import stat
import sys

root = pathlib.Path(sys.argv[1])
public_key = root / "runtime/time-signer-public.der"
if stat.S_IMODE(public_key.stat().st_mode) != 0o444:
    raise SystemExit("time signer public key mode must be 444")
stack = (root / "config/stack.env").read_text()
gateway = (root / "config/gateway.env").read_text()
signal = (root / "config/signal.env").read_text()
composition = (root / "runtime/composition.yaml").read_text()
acl = (root / "config/mosquitto.acl").read_text()

for marker in (
    "CN_CLOUD_PUBLIC_IP=203.0.113.10",
    "CN_STAGING_GATEWAY_IMAGE=explorex-cn-gateway:contract-amd64",
    "CN_STAGING_MINIO_ROOT_USER=disabled",
    f"CN_STAGING_RUNTIME_DIR={root}/runtime",
):
    if marker not in stack:
        raise SystemExit(f"stack.env lacks {marker}")
for marker in (
    "DOMESTIC_MQTT_ENDPOINT=mqtts://mqtt-staging.livpet.top:8883",
    "GATEWAY_OBJECT_BUCKET=contract-media",
    "GATEWAY_OBJECT_ACCESS_KEY_ID=contract-access-key",
    "GATEWAY_OBJECT_ACCESS_KEY_SECRET=contract-access-secret",
    "INTERNAL_APPS_JSON=",
):
    if marker not in gateway:
        raise SystemExit(f"gateway.env lacks {marker}")
for marker in (
    "EXPLOREX_SIGNAL_PUBLIC_BASE_URL=ws://rtc-staging.livpet.top",
    "turn:turn-staging.livpet.top:3478?transport=udp",
):
    if marker not in signal:
        raise SystemExit(f"signal.env lacks {marker}")
if "media_object: object.aliyun_oss" not in composition:
    raise SystemExit("composition lacks explicit OSS binding")
if "pattern read cmd/cn-staging/%u/req" not in acl:
    raise SystemExit("ACL lacks per-device username topic binding")
for relative in ("config/stack.env", "config/gateway.env", "config/signal.env"):
    mode = stat.S_IMODE((root / relative).stat().st_mode)
    if mode != 0o600:
        raise SystemExit(f"{relative} mode is {mode:o}, want 600")
for placeholder in ("replace-with", "example.invalid"):
    if placeholder in stack + gateway + signal + composition:
        raise SystemExit(f"operator files retain placeholder {placeholder}")
PY

if CN_CLOUD_PUBLIC_IP=203.0.113.10 \
  CN_CLOUD_OSS_BUCKET=contract-media \
  CN_CLOUD_OSS_ACCESS_KEY_ID=contract-access-key \
  CN_CLOUD_OSS_ACCESS_KEY_SECRET=contract-access-secret \
  CN_CLOUD_GATEWAY_IMAGE=explorex-cn-gateway:contract-amd64 \
  bash "$PREPARE" "$OUTPUT_DIR" >/dev/null 2>&1; then
  echo "cloud operator preparation overwrote existing operator files" >&2
  exit 1
fi

echo "cn cloud staging operator preparation contract: ok"
