#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)

fail() {
    echo "FAIL: $*" >&2
    exit 1
}

for package in \
    ./internal/provider \
    ./internal/awsiot \
    ./internal/platform/awskms \
    ./internal/platform/awskvs \
    ./internal/platform/awss3 \
    ./internal/platform/awssqs; do
    (cd "$ROOT" && go test -race -count=1 "$package") ||
        fail "AWS compatibility package failed: $package"
done

python3 - "$ROOT" <<'PY'
import sys
from pathlib import Path

root = Path(sys.argv[1])
composition = (root / "internal/provider/composition.go").read_text(encoding="utf-8")
template = (root / "deploy/aws-staging/cloudformation/staging.yaml").read_text(encoding="utf-8")
deploy = (root / "deploy/aws-staging/deploy.sh").read_text(encoding="utf-8")


def require(condition, message):
    if not condition:
        raise SystemExit(f"FAIL {message}")


for marker in (
    '"aws_iot_identity"',
    '"aws_iot_mqtt"',
    '"aws_kvs"',
    '"aws_s3"',
    '"aws_sqs"',
    '"aws_kms"',
    'driverFamily(instance.Driver)',
    'AWS KVS instance',
):
    require(marker in composition, f"provider composition lost AWS compatibility marker: {marker}")

for marker in (
    "AWS::S3::Bucket",
    "AWS::KMS::Key",
    "AWS::SQS::Queue",
    "AWS::IoT::TopicRule",
    "AWS_IOT_CREDENTIAL_PROVIDER_ENDPOINT",
    "AWS_KVS_CHANNEL_PREFIX",
    "AWS_S3_MEDIA_BUCKET",
    "AWS_S3_OTA_BUCKET",
):
    require(marker in template, f"AWS staging template lost resource/config marker: {marker}")

for marker in (
    "prepare_pinned_release_source",
    "prepare_build_release_source",
    "@sha256:",
    "previous_image",
    "write_release_manifest",
    "verify_release",
    "change_set_guard.py",
    "ordinary staging deploy refuses removals or replacements",
):
    require(marker in deploy, f"AWS release rollback guard lost marker: {marker}")

print("test_aws_composition_regression_contract: OK")
PY
