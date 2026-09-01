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


api_role = body.split("  APITaskRole:", 1)[1].split("  WorkerTaskRole:", 1)[0]
require("Action: [s3:GetObject, s3:PutObject]" in api_role,
        "API task role must retain object-level media read/write permissions")
require("Resource: !Sub ${MediaBucket.Arn}/*" in api_role,
        "media object permissions must remain scoped to objects in the media bucket")
require("Action: s3:ListBucket" in api_role,
        "API task role must authorize bucket metadata checks for missing snapshot JPEGs")
require("Resource: !GetAtt MediaBucket.Arn" in api_role,
        "ListBucket permission must target the bucket ARN without an object wildcard")

print("test_media_bucket_policy_contract: OK")
PY
