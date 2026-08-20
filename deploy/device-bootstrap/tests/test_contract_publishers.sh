#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
LOCAL_SCRIPT="$ROOT/deploy/device-bootstrap/scripts/render-local-contract.sh"
STAGING_SCRIPT="$ROOT/deploy/device-bootstrap/scripts/render-staging-contract.sh"
PUBLISHED_CONTRACTS="$ROOT/deploy/device-bootstrap/contracts"
STAGING_CONTRACT="$PUBLISHED_CONTRACTS/staging"
CWJ_CONTRACT="$PUBLISHED_CONTRACTS/cwj"

test -f "$LOCAL_SCRIPT"
test -f "$STAGING_SCRIPT"
test -f "$STAGING_CONTRACT/contract.json"
test -f "$STAGING_CONTRACT/service-ca.pem"
test -d "$STAGING_CONTRACT/time-bootstrap"
test -f "$CWJ_CONTRACT/contract.json"
test -d "$CWJ_CONTRACT/time-bootstrap"
test ! -e "$CWJ_CONTRACT/service-ca.pem"
test ! -e "$ROOT/deploy/local/device-bootstrap-contract"
test ! -e "$ROOT/deploy/staging/device-bootstrap-contract"
git -C "$ROOT" check-ignore -q .local/device-bootstrap/cwj/time-bootstrap-private.pem

python3 - "$CWJ_CONTRACT/contract.json" <<'PY'
import json
from pathlib import Path
import sys

contract = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
if contract.get("environment") != "development":
    raise SystemExit("cwj Contract must be a development Contract")
if contract.get("kind") != "device-service-bootstrap":
    raise SystemExit("cwj Contract must use the provider-neutral kind")
if contract.get("service") != {"base_url": "http://192.168.11.146:14080"}:
    raise SystemExit("cwj Contract must use the local HTTP service origin only")
if "gateway" in contract:
    raise SystemExit("cwj Contract must not expose a provider-specific gateway field")
if contract.get("time_bootstrap", {}).get("url") != "http://192.168.11.146:14080/api/v1/device-time/bootstrap":
    raise SystemExit("cwj Contract must use the local time-bootstrap endpoint")
if "provenance" in contract:
    raise SystemExit("cwj Contract must not expose producer-specific provenance")
keys = contract.get("time_bootstrap", {}).get("keys")
if not isinstance(keys, list) or len(keys) != 1 or keys[0].get("kid") != "time-cwj-g1":
    raise SystemExit("cwj Contract must publish exactly the cwj local time key")
PY

python3 - "$LOCAL_SCRIPT" "$STAGING_SCRIPT" <<'PY'
from pathlib import Path
import sys

local = Path(sys.argv[1]).read_text(encoding="utf-8")
staging = Path(sys.argv[2]).read_text(encoding="utf-8")

for value in (
    "DEVICE_SERVICE_BOOTSTRAP_SERVICE_URL",
    "DEVICE_SERVICE_BOOTSTRAP_TIME_URL",
    "DEVICE_SERVICE_BOOTSTRAP_KEY_ID",
    "DEVICE_SERVICE_BOOTSTRAP_PRIVATE_KEY",
    "DEVICE_SERVICE_BOOTSTRAP_NEXT_KEY_ID",
    "DEVICE_SERVICE_BOOTSTRAP_NEXT_PUBLIC_KEY",
    "--out",
    "cmd/device-service-bootstrap-contract",
):
    if value not in local:
        raise SystemExit(f"local Contract publisher is missing {value}")
if "openssl genpkey" in local:
    raise SystemExit("local Contract publisher must not silently create private keys")

for value in (
    "AWS_PROFILE",
    "AWS_REGION",
    "DEVICE_SERVICE_BOOTSTRAP_STACK",
    "DEVICE_SERVICE_BOOTSTRAP_NEXT_KEY_ID",
    "DEVICE_SERVICE_BOOTSTRAP_NEXT_KMS_KEY_ID",
    "--out",
    "aws-release",
    "AmazonRootCA1.pem",
):
    if value not in staging:
        raise SystemExit(f"staging Contract publisher is missing {value}")
PY

echo "device bootstrap Contract publishers: ok"
