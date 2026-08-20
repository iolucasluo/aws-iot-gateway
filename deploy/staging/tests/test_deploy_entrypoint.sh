#!/bin/sh
set -eu

# The contract tests must validate missing confirmations even when invoked by an
# authorized parent deploy process.
unset CONFIRM_AWS_STAGING_DEPLOY CONFIRM_AWS_STAGING_SEED

ROOT=$(git -C "$(dirname -- "$0")" rev-parse --show-toplevel)
DEPLOY="$ROOT/deploy/staging/deploy.sh"

fail() {
    echo "FAIL: $*" >&2
    exit 1
}

[ -x "$DEPLOY" ] || fail "deploy/staging/deploy.sh must exist and be executable"
bash -n "$DEPLOY" || fail "deploy.sh syntax"

help=$($DEPLOY --help)
for required in --status --plan --dry-run --logs --deploy --seed --verify --render-device-bootstrap-contract; do
    printf '%s\n' "$help" | grep -F -- "$required" >/dev/null || fail "help missing $required"
done

case $(sed -n '1p' "$DEPLOY") in
    '#!/usr/bin/env bash') ;;
    *) fail "deploy.sh must use bash" ;;
esac

for forbidden in explorex-cloud anyka-env-config 'get-secret-value' 'DB_RESET' 'REDIS_FLUSH'; do
    if grep -F "$forbidden" "$DEPLOY" >/dev/null; then
        fail "deploy.sh contains forbidden legacy/secret/destructive token: $forbidden"
    fi
done

for required in \
    '135359468083' \
    'ap-east-1' \
    'explorex-staging-hk' \
    'aws-iot-gateway-staging' \
    'UsePreviousValue=true' \
    'deployment requires HEAD' \
    'git -C "$GATEWAY_ROOT" ls-remote --exit-code' \
    'ordinary staging deploy refuses CloudFormation removals or resource replacements' \
    'ResourceChange.Replacement == `Conditional`' \
    'ResourceChange.ResourceType != `AWS::ECS::TaskDefinition`' \
    '[[ "$IMAGE_REF" == "$repository"@sha256:* ]]' \
    '@sha256:' \
    'aws ecs wait services-stable' \
    'aws-iot-gateway-db-seed' \
    'smoke_internal_apps.py' \
    'render-staging-contract.sh'; do
    grep -F "$required" "$DEPLOY" >/dev/null || fail "deploy.sh missing safety/deploy contract: $required"
done

tmp=$(mktemp -d "${TMPDIR:-/tmp}/aws-deploy-contract.XXXXXX")
trap 'rm -rf "$tmp"' EXIT INT TERM
mkdir -p "$tmp/bin"
cat > "$tmp/bin/aws" <<'AWS'
#!/bin/sh
printf '%s\n' "$*" >> "${AWS_CALL_LOG:?}"
exit 99
AWS
chmod +x "$tmp/bin/aws"
: > "$tmp/aws.log"

expect_reject_before_aws() {
    name=$1
    shift
    : > "$tmp/aws.log"
    if PATH="$tmp/bin:$PATH" AWS_CALL_LOG="$tmp/aws.log" "$DEPLOY" "$@" >"$tmp/out" 2>"$tmp/err"; then
        fail "$name unexpectedly succeeded"
    fi
    [ ! -s "$tmp/aws.log" ] || fail "$name reached AWS before local rejection"
}

expect_reject_before_aws 'prod environment' --env prod --status
expect_reject_before_aws 'force bypass' --env staging --force
expect_reject_before_aws 'deploy without confirmation' --env staging --deploy
expect_reject_before_aws 'seed without confirmation' --env staging --seed

# Validate exact ECR repository matching against a clean disposable Gateway repository.
fixture="$tmp/fixture"
mkdir -p "$fixture/deploy/staging" "$fixture/deploy/cloudformation"
cp "$DEPLOY" "$fixture/deploy/staging/deploy.sh"
printf 'FROM scratch\n' > "$fixture/Dockerfile"
printf 'AWSTemplateFormatVersion: 2010-09-09\nResources: {}\n' > "$fixture/deploy/cloudformation/staging.yaml"
printf 'AWSTemplateFormatVersion: 2010-09-09\nResources: {}\n' > "$fixture/deploy/cloudformation/bootstrap.yaml"
git -C "$fixture" init -q
git -C "$fixture" config user.name aws-deploy-test
git -C "$fixture" config user.email aws-deploy-test@example.invalid
git -C "$fixture" add Dockerfile deploy
git -C "$fixture" commit -qm fixture

cat > "$tmp/bin/aws-plan" <<'AWS'
#!/bin/sh
case "$*" in
    *'sts get-caller-identity --query Account'*) echo 135359468083 ;;
    *'sts get-caller-identity --query Arn'*) echo arn:aws:iam::135359468083:user/test ;;
    *'cloudformation describe-stacks'*'StackStatus'*) echo UPDATE_COMPLETE ;;
    *'cloudformation describe-stacks'*'RepositoryUri'*) echo 135359468083.dkr.ecr.ap-east-1.amazonaws.com/aws-iot-gateway-staging ;;
    *'cloudformation describe-stacks'*'ContainerImage'*) echo 135359468083.dkr.ecr.ap-east-1.amazonaws.com/aws-iot-gateway-staging@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa ;;
    *'cloudformation validate-template'*) exit 0 ;;
    *) echo "unexpected fake aws invocation: $*" >&2; exit 98 ;;
esac
AWS
chmod +x "$tmp/bin/aws-plan"
ln -sf "$tmp/bin/aws-plan" "$tmp/bin/aws"
digest=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
if PATH="$tmp/bin:$PATH" CONFIRM_AWS_STAGING_DEPLOY=deploy-aws-iot-gateway-staging \
    "$fixture/deploy/staging/deploy.sh" --env staging --deploy \
    --image-ref "135359468083.dkr.ecr.ap-east-1.amazonaws.com/aws-iot-gateway-staging@sha256:$digest" \
    >"$tmp/out" 2>"$tmp/err"; then
    fail "deploy without a published upstream unexpectedly succeeded"
fi
grep -F 'configured remote upstream' "$tmp/err" >/dev/null || \
    fail "deploy without a published upstream used an incompatible rejection"

if PATH="$tmp/bin:$PATH" "$fixture/deploy/staging/deploy.sh" --env staging --plan \
    --image-ref "135359468083XdkrXecrXap-east-1XamazonawsXcom/aws-iot-gateway-staging@sha256:$digest" \
    >"$tmp/out" 2>"$tmp/err"; then
    fail "regex-like repository variant unexpectedly passed image validation"
fi
PATH="$tmp/bin:$PATH" "$fixture/deploy/staging/deploy.sh" --env staging --plan \
    --image-ref "135359468083.dkr.ecr.ap-east-1.amazonaws.com/aws-iot-gateway-staging@sha256:$digest" \
    >"$tmp/out" 2>"$tmp/err" || fail "exact staging repository digest was rejected"
grep -F 'mutates_aws=false' "$tmp/out" >/dev/null || fail "valid image plan did not remain non-mutating"

echo "test_deploy_entrypoint: OK"
