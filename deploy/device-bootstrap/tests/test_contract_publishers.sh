#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
LOCAL_SCRIPT="$ROOT/deploy/device-bootstrap/scripts/render-local-contract.sh"
STAGING_SCRIPT="$ROOT/deploy/device-bootstrap/scripts/render-staging-contract.sh"
PUBLISHED_CONTRACTS="$ROOT/deploy/device-bootstrap/contracts"
DEVELOPMENT_CONTRACT="$PUBLISHED_CONTRACTS/development"
STAGING_CONTRACT="$PUBLISHED_CONTRACTS/staging"
CWJ_CONTRACT="$PUBLISHED_CONTRACTS/cwj"

test -f "$LOCAL_SCRIPT"
test -f "$STAGING_SCRIPT"
test -f "$DEVELOPMENT_CONTRACT/contract.json"
test -d "$DEVELOPMENT_CONTRACT/time-bootstrap"
test ! -e "$DEVELOPMENT_CONTRACT/gateway-ca.pem"
test -f "$STAGING_CONTRACT/contract.json"
test -f "$STAGING_CONTRACT/gateway-ca.pem"
test -d "$STAGING_CONTRACT/time-bootstrap"
test -f "$CWJ_CONTRACT/contract.json"
test -d "$CWJ_CONTRACT/time-bootstrap"
test ! -e "$CWJ_CONTRACT/gateway-ca.pem"
test ! -e "$ROOT/deploy/local/device-bootstrap-contract"
test ! -e "$ROOT/deploy/staging/device-bootstrap-contract"
git -C "$ROOT" check-ignore -q .local/device-bootstrap/cwj/time-bootstrap-private.pem

python3 - "$DEVELOPMENT_CONTRACT/contract.json" "$DEVELOPMENT_CONTRACT" <<'PY'
import hashlib
import json
from pathlib import Path
import sys

contract_path = Path(sys.argv[1])
contract_dir = Path(sys.argv[2])
contract = json.loads(contract_path.read_text(encoding="utf-8"))
if contract.get("environment") != "development":
    raise SystemExit("shared development Contract must use the development environment")
if contract.get("gateway") != {"base_url": "http://192.168.11.131:14081"}:
    raise SystemExit("shared development Contract must use the approved LAN Gateway origin")
if contract.get("time_bootstrap", {}).get("url") != "http://192.168.11.131:14081/api/v1/device-time/bootstrap":
    raise SystemExit("shared development Contract must use the approved time-bootstrap endpoint")
if contract.get("provenance") != {"signer_backend": "local"}:
    raise SystemExit("shared development Contract must use local signer provenance")
keys = contract.get("time_bootstrap", {}).get("keys")
if not isinstance(keys, list) or len(keys) != 1:
    raise SystemExit("shared development Contract must publish exactly one time key")
key = keys[0]
if key.get("kid") != "time-development-g1":
    raise SystemExit("shared development Contract has an unexpected key ID")
key_path = contract_dir / key.get("spki_der_file", "")
if not key_path.is_file():
    raise SystemExit("shared development Contract public key is missing")
actual_hash = hashlib.sha256(key_path.read_bytes()).hexdigest()
if actual_hash != key.get("sha256"):
    raise SystemExit("shared development Contract public key hash does not match")
for path in contract_dir.rglob("*"):
    if path.is_file() and ("private" in path.name.lower() or path.suffix.lower() == ".pem"):
        raise SystemExit("shared development Contract must not contain private key material")
PY

python3 - "$CWJ_CONTRACT/contract.json" <<'PY'
import json
from pathlib import Path
import sys

contract = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
if contract.get("environment") != "development":
    raise SystemExit("cwj Contract must be a development Contract")
if contract.get("gateway") != {"base_url": "http://192.168.11.146:14080"}:
    raise SystemExit("cwj Contract must use the local HTTP Gateway origin only")
if contract.get("time_bootstrap", {}).get("url") != "http://192.168.11.146:14080/api/v1/device-time/bootstrap":
    raise SystemExit("cwj Contract must use the local time-bootstrap endpoint")
if contract.get("provenance") != {"signer_backend": "local"}:
    raise SystemExit("cwj Contract must use only the local signer provenance")
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
    "GATEWAY_DEVICE_BOOTSTRAP_GATEWAY_URL",
    "GATEWAY_DEVICE_BOOTSTRAP_TIME_URL",
    "GATEWAY_DEVICE_BOOTSTRAP_KEY_ID",
    "GATEWAY_DEVICE_BOOTSTRAP_PRIVATE_KEY",
    "GATEWAY_DEVICE_BOOTSTRAP_NEXT_KEY_ID",
    "GATEWAY_DEVICE_BOOTSTRAP_NEXT_PUBLIC_KEY",
    "--out",
    "cmd/gateway-device-bootstrap-contract",
):
    if value not in local:
        raise SystemExit(f"local Contract publisher is missing {value}")
if "openssl genpkey" in local:
    raise SystemExit("local Contract publisher must not silently create private keys")

for value in (
    "AWS_PROFILE",
    "AWS_REGION",
    "GATEWAY_DEVICE_BOOTSTRAP_STACK",
    "GATEWAY_DEVICE_BOOTSTRAP_NEXT_KEY_ID",
    "GATEWAY_DEVICE_BOOTSTRAP_NEXT_KMS_KEY_ID",
    "--out",
    "aws-release",
    "AmazonRootCA1.pem",
):
    if value not in staging:
        raise SystemExit(f"staging Contract publisher is missing {value}")
PY

echo "device bootstrap Contract publishers: ok"
