#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
TEMPLATE="$ROOT/deploy/cloudformation/staging.yaml"

python3 - "$TEMPLATE" <<'PY'
import sys
from pathlib import Path

body = Path(sys.argv[1]).read_text()

def require(value, message):
    if not value:
        raise SystemExit(f"FAIL {message}")

require("TimeBootstrapKey:" in body and "KeySpec: ECC_NIST_P256" in body and
        "KeyUsage: SIGN_VERIFY" in body, "staging must own a dedicated asymmetric time signer")
require("kms:GetPublicKey" in body and "kms:Sign" in body,
        "API task role must have only the time signer operations")
require(body.count("Name: TIME_BOOTSTRAP_KEY_ID") == 2 and
        body.count("Name: TIME_BOOTSTRAP_KMS_KEY_ID") == 2,
        "shared strict config requires signer identifiers in API and worker tasks")
require(body.count("Name: TIME_BOOTSTRAP_SIGNER") == 2 and
        body.count("Value: aws-kms") >= 2,
        "staging API and worker tasks must explicitly select the aws-kms signer")
require("PublicTimeLoadBalancer:" in body and "Scheme: internet-facing" in body and
        "PublicTimeRoute:" in body, "staging must expose the signed bootstrap over public HTTP")
require("/api/v1/device-time/bootstrap" in body and "Type: fixed-response" in body,
        "public listener must forward only the exact bootstrap path and reject every default route")
require("TimeBootstrapURL:" in body and
        "http://${PublicTimeLoadBalancer.DNSName}/api/v1/device-time/bootstrap" in body,
        "stack must output the release-owned exact HTTP bootstrap URL")

print("test_time_bootstrap_stack: OK")
PY
