#!/usr/bin/env bash
# Controlled owner entrypoint for the current aws-iot-gateway AWS staging deployment.

set -euo pipefail

DEPLOY_DIR=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
GATEWAY_ROOT=$(CDPATH= cd -- "$DEPLOY_DIR/../.." && pwd)
RUNTIME_ROOT="$GATEWAY_ROOT/.runtime/aws-deploy"
RELEASE_SOURCE_ROOT="$GATEWAY_ROOT"
RELEASE_DEPLOY_DIR="$DEPLOY_DIR"
RELEASE_SOURCE_PARENT=""
TEMPLATE="$RELEASE_SOURCE_ROOT/deploy/aws-staging/cloudformation/staging.yaml"
BOOTSTRAP_TEMPLATE="$RELEASE_SOURCE_ROOT/deploy/aws-staging/cloudformation/bootstrap.yaml"
SMOKE="$RELEASE_DEPLOY_DIR/smoke_internal_apps.py"
CONTRACT_RENDERER="$RELEASE_SOURCE_ROOT/deploy/device-bootstrap/scripts/render-staging-contract.sh"

EXPECTED_ACCOUNT_ID=135359468083
EXPECTED_REGION=ap-east-1
DEFAULT_PROFILE=explorex-staging-hk
STACK_NAME=aws-iot-gateway-staging
BOOTSTRAP_STACK_NAME=aws-iot-gateway-staging-bootstrap
CLUSTER_NAME=aws-iot-gateway-staging
API_SERVICE=api
WORKER_SERVICE=iot-gateway-event-worker
ECR_REPOSITORY=aws-iot-gateway-staging

ENVIRONMENT=""
PROFILE="$DEFAULT_PROFILE"
REGION="$EXPECTED_REGION"
MODE=""
LOG_TARGET=""
LOG_LINES=100
RELEASE_ID=""
IMAGE_REF=""
CONTRACT_OUT=""
SOURCE_BRANCH=""
SOURCE_COMMIT=""

usage() {
    cat <<'USAGE'
Usage: deploy.sh --env staging <mode> [options]

Non-infrastructure-mutation modes:
  --status                              Inspect STS, CloudFormation, ECS and public health.
  --plan                                Validate the template and print the current/proposed release facts.
  --dry-run                             Run local release gates without building, pushing or updating AWS.
  --logs <api|event-worker> [lines]     Read recent CloudWatch logs (default: 100 lines).
  --verify                              Run post-deploy health and signed staging smoke checks.
  --render-device-bootstrap-contract <absolute-new-dir>
                                        Generate a local public Contract candidate from live stack/KMS outputs.

AWS mutation modes (exact confirmation environment variables required):
  --deploy                              Build/push an immutable linux/amd64 image, update the stack,
                                        wait for ECS, run DB seed, verify and render a Contract candidate.
  --seed                                Run only the idempotent staging DB seed task.

Options:
  --profile <name>                      AWS CLI profile (default: explorex-staging-hk).
  --region <region>                     Must remain ap-east-1.
  --release-id <id>                     New immutable ECR tag; defaults to git-<12-char-commit>.
  --help                                Show this help.

Mutation confirmations:
  CONFIRM_AWS_STAGING_DEPLOY=deploy-aws-iot-gateway-staging
  CONFIRM_AWS_STAGING_SEED=seed-aws-iot-gateway-staging

The entrypoint is staging-only. --force, production, DB reset, Redis flush and secret updates are unsupported.
USAGE
}

fail() {
    printf '[ERROR] %s\n' "$*" >&2
    exit 1
}

log() {
    printf '[aws-deploy] %s\n' "$*"
}

set_mode() {
    local requested=$1
    [[ -z "$MODE" ]] || fail "choose exactly one mode"
    MODE=$requested
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --env)
            [[ $# -ge 2 ]] || fail "--env requires staging"
            ENVIRONMENT=$2
            shift 2
            ;;
        --profile)
            [[ $# -ge 2 ]] || fail "--profile requires a value"
            PROFILE=$2
            shift 2
            ;;
        --region)
            [[ $# -ge 2 ]] || fail "--region requires a value"
            REGION=$2
            shift 2
            ;;
        --status)
            set_mode status
            shift
            ;;
        --plan)
            set_mode plan
            shift
            ;;
        --dry-run)
            set_mode dry-run
            shift
            ;;
        --logs)
            set_mode logs
            [[ $# -ge 2 ]] || fail "--logs requires api or event-worker"
            LOG_TARGET=$2
            shift 2
            if [[ ${1:-} =~ ^[1-9][0-9]*$ ]]; then
                LOG_LINES=$1
                shift
            fi
            ;;
        --verify)
            set_mode verify
            shift
            ;;
        --render-device-bootstrap-contract)
            set_mode render-contract
            [[ $# -ge 2 ]] || fail "--render-device-bootstrap-contract requires an absolute new directory"
            CONTRACT_OUT=$2
            shift 2
            ;;
        --deploy)
            set_mode deploy
            shift
            ;;
        --seed)
            set_mode seed
            shift
            ;;
        --release-id)
            [[ $# -ge 2 ]] || fail "--release-id requires a value"
            RELEASE_ID=$2
            shift 2
            ;;
        --force|--db-reset|--redis-flush|--infra-apply)
            fail "$1 is intentionally unsupported"
            ;;
        --help|-h)
            usage
            exit 0
            ;;
        *)
            fail "unknown option: $1"
            ;;
    esac
done

[[ "$ENVIRONMENT" == staging ]] || fail "--env staging is required; production is unsupported"
[[ "$REGION" == "$EXPECTED_REGION" ]] || fail "region must remain $EXPECTED_REGION"
[[ -n "$MODE" ]] || fail "choose a mode; use --help"
[[ "$LOG_LINES" -le 10000 ]] || fail "log line limit must be <= 10000"

case "$MODE" in
    deploy)
        [[ ${CONFIRM_AWS_STAGING_DEPLOY:-} == deploy-aws-iot-gateway-staging ]] ||
            fail "set CONFIRM_AWS_STAGING_DEPLOY=deploy-aws-iot-gateway-staging after explicit authorization"
        ;;
    seed)
        [[ ${CONFIRM_AWS_STAGING_SEED:-} == seed-aws-iot-gateway-staging ]] ||
            fail "set CONFIRM_AWS_STAGING_SEED=seed-aws-iot-gateway-staging after explicit authorization"
        ;;
esac

require_command() {
    command -v "$1" >/dev/null 2>&1 || fail "required command is missing: $1"
}

aws_cli() {
    aws --profile "$PROFILE" --region "$REGION" "$@"
}

stack_output() {
    local stack=$1
    local key=$2
    aws_cli cloudformation describe-stacks \
        --stack-name "$stack" \
        --query "Stacks[0].Outputs[?OutputKey==\`$key\`].OutputValue | [0]" \
        --output text
}

stack_parameter() {
    local key=$1
    aws_cli cloudformation describe-stacks \
        --stack-name "$STACK_NAME" \
        --query "Stacks[0].Parameters[?ParameterKey==\`$key\`].ParameterValue | [0]" \
        --output text
}

verify_identity() {
    require_command aws
    local account arn
    account=$(aws_cli sts get-caller-identity --query Account --output text)
    arn=$(aws_cli sts get-caller-identity --query Arn --output text)
    [[ "$account" == "$EXPECTED_ACCOUNT_ID" ]] ||
        fail "AWS account mismatch: expected $EXPECTED_ACCOUNT_ID, received $account"
    [[ -n "$arn" && "$arn" != None ]] || fail "AWS caller ARN is unavailable"
    printf 'profile=%s\nregion=%s\naccount=%s\ncaller_arn=%s\n' "$PROFILE" "$REGION" "$account" "$arn"
}

require_stack_ready() {
    local stack=$1
    local status
    status=$(aws_cli cloudformation describe-stacks --stack-name "$stack" \
        --query 'Stacks[0].StackStatus' --output text)
    case "$status" in
        CREATE_COMPLETE|UPDATE_COMPLETE|UPDATE_ROLLBACK_COMPLETE) ;;
        *) fail "stack $stack is not ready: $status" ;;
    esac
}

assert_clean_source() {
    require_command git
    local branch dirty
    branch=$(git -C "$GATEWAY_ROOT" branch --show-current)
    [[ -n "$branch" ]] || fail "detached HEAD cannot be deployed"
    dirty=$(git -C "$GATEWAY_ROOT" status --short)
    [[ -z "$dirty" ]] || fail "Gateway worktree must be clean before release"
    [[ -f "$GATEWAY_ROOT/Dockerfile" ]] || fail "Gateway Dockerfile is missing"
    SOURCE_BRANCH=$branch
    SOURCE_COMMIT=$(git -C "$GATEWAY_ROOT" rev-parse HEAD)
    printf 'source_branch=%s\nsource_commit=%s\n' \
        "$SOURCE_BRANCH" "$SOURCE_COMMIT"
}

assert_published_source() {
    local branch remote merge_ref local_commit remote_commit
    branch=$(git -C "$GATEWAY_ROOT" branch --show-current)
    remote=$(git -C "$GATEWAY_ROOT" config --get "branch.$branch.remote" || true)
    merge_ref=$(git -C "$GATEWAY_ROOT" config --get "branch.$branch.merge" || true)
    [[ -n "$remote" && "$remote" != . && -n "$merge_ref" ]] ||
        fail "deployment requires a configured remote upstream for branch $branch"
    local_commit=$(git -C "$GATEWAY_ROOT" rev-parse HEAD)
    if ! remote_commit=$(git -C "$GATEWAY_ROOT" ls-remote --exit-code "$remote" "$merge_ref" |
        awk 'NR == 1 {print $1}'); then
        fail "could not resolve published upstream $remote/$merge_ref"
    fi
    [[ "$remote_commit" == "$local_commit" ]] ||
        fail "deployment requires HEAD $local_commit to equal published upstream $remote/$merge_ref"
    printf 'source_upstream=%s/%s\nsource_published_commit=%s\n' \
        "$remote" "$merge_ref" "$remote_commit"
}

validate_release_id() {
    if [[ -z "$RELEASE_ID" ]]; then
        RELEASE_ID="git-$(git -C "$GATEWAY_ROOT" rev-parse --short=12 HEAD)"
    fi
    [[ "$RELEASE_ID" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ ]] ||
        fail "release ID must match [A-Za-z0-9][A-Za-z0-9._-]{0,63}"
}

use_release_source() {
    RELEASE_SOURCE_ROOT=$1
    RELEASE_DEPLOY_DIR="$RELEASE_SOURCE_ROOT/deploy/aws-staging"
    TEMPLATE="$RELEASE_SOURCE_ROOT/deploy/aws-staging/cloudformation/staging.yaml"
    BOOTSTRAP_TEMPLATE="$RELEASE_SOURCE_ROOT/deploy/aws-staging/cloudformation/bootstrap.yaml"
    SMOKE="$RELEASE_DEPLOY_DIR/smoke_internal_apps.py"
    CONTRACT_RENDERER="$RELEASE_SOURCE_ROOT/deploy/device-bootstrap/scripts/render-staging-contract.sh"
}

prepare_pinned_release_source() {
    require_command tar
    local helper
    helper="$DEPLOY_DIR/lib/release_source.sh"
    [[ -f "$helper" ]] || fail "release source helper is missing"
    # shellcheck source=lib/release_source.sh
    source "$helper"
    RELEASE_SOURCE_PARENT=$(mktemp -d "${TMPDIR:-/tmp}/gateway-release-source.XXXXXX")
    trap 'rm -rf "$RELEASE_SOURCE_PARENT"' EXIT
    RELEASE_SOURCE_ROOT="$RELEASE_SOURCE_PARENT/gates"
    mkdir -p "$RELEASE_SOURCE_ROOT"
    create_release_source "$GATEWAY_ROOT" "$SOURCE_COMMIT" "$RELEASE_SOURCE_ROOT" ||
        fail "could not materialize pinned release source $SOURCE_COMMIT"
    use_release_source "$RELEASE_SOURCE_ROOT"
    printf 'release_source_role=gates\nrelease_source_commit=%s\nrelease_source_root=%s\n' \
        "$SOURCE_COMMIT" "$RELEASE_SOURCE_ROOT"
}

prepare_build_release_source() {
    local build_source
    [[ -n "$RELEASE_SOURCE_PARENT" && -d "$RELEASE_SOURCE_PARENT" ]] ||
        fail "release source parent is unavailable"
    build_source="$RELEASE_SOURCE_PARENT/build"
    mkdir -p "$build_source"
    create_release_source "$GATEWAY_ROOT" "$SOURCE_COMMIT" "$build_source" ||
        fail "could not rematerialize build source $SOURCE_COMMIT"
    use_release_source "$build_source"
    printf 'release_source_role=build\nrelease_source_commit=%s\nrelease_source_root=%s\n' \
        "$SOURCE_COMMIT" "$RELEASE_SOURCE_ROOT"
}

assert_source_unchanged() {
    local current_commit dirty
    current_commit=$(git -C "$GATEWAY_ROOT" rev-parse HEAD)
    dirty=$(git -C "$GATEWAY_ROOT" status --short)
    [[ "$current_commit" == "$SOURCE_COMMIT" && -z "$dirty" ]] ||
        fail "Gateway source changed after release input was pinned"
}

validate_template() {
    [[ -f "$TEMPLATE" && -f "$BOOTSTRAP_TEMPLATE" ]] || fail "CloudFormation template is missing"
    aws_cli cloudformation validate-template --template-body "file://$TEMPLATE" >/dev/null
    aws_cli cloudformation validate-template --template-body "file://$BOOTSTRAP_TEMPLATE" >/dev/null
    log "CloudFormation templates validated"
}

run_local_release_gates() {
    require_command go
    require_command python3
    log "running Gateway Go tests"
    (cd "$RELEASE_SOURCE_ROOT" && go test -race -count=1 ./...)
    (cd "$RELEASE_SOURCE_ROOT" && go vet ./...)
    bash "$RELEASE_SOURCE_ROOT/deploy/local/test_local_compose_contract.sh"
    bash "$RELEASE_SOURCE_ROOT/deploy/device-bootstrap/tests/test_contract_publishers.sh"
    python3 "$RELEASE_DEPLOY_DIR/test_internal_apps_smoke.py"
    python3 "$RELEASE_DEPLOY_DIR/test_change_set_guard.py"
    bash "$RELEASE_DEPLOY_DIR/tests/test_deploy_entrypoint.sh"
    local test_script
    for test_script in "$RELEASE_DEPLOY_DIR"/test_*.sh; do
        [[ -f "$test_script" ]] || continue
        bash "$test_script"
    done
    log "local release gates passed"
}

api_health_url() {
    printf '%s/healthz\n' "$(stack_output "$STACK_NAME" APIBaseURL)"
}

status_mode() {
    require_command curl
    verify_identity
    require_stack_ready "$BOOTSTRAP_STACK_NAME"
    require_stack_ready "$STACK_NAME"
    printf 'stack=%s\nstack_status=%s\n' "$STACK_NAME" \
        "$(aws_cli cloudformation describe-stacks --stack-name "$STACK_NAME" --query 'Stacks[0].StackStatus' --output text)"
    printf 'api_base_url=%s\n' "$(stack_output "$STACK_NAME" APIBaseURL)"
    printf 'time_bootstrap_url=%s\n' "$(stack_output "$STACK_NAME" TimeBootstrapURL)"
    printf 'container_image=%s\n' "$(stack_parameter ContainerImage)"
    aws_cli ecs describe-services --cluster "$CLUSTER_NAME" \
        --services "$API_SERVICE" "$WORKER_SERVICE" \
        --query 'services[].{service:serviceName,status:status,desired:desiredCount,running:runningCount,pending:pendingCount,task_definition:taskDefinition}' \
        --output table
    curl --noproxy '*' --silent --show-error --fail --max-time 15 "$(api_health_url)"
    printf '\nstatus=ok\n'
}

plan_mode() {
    verify_identity
    require_stack_ready "$BOOTSTRAP_STACK_NAME"
    require_stack_ready "$STACK_NAME"
    assert_clean_source
    validate_release_id
    validate_template
    printf 'mode=plan\nstack=%s\ncurrent_image=%s\nproposed_release_id=%s\n' \
        "$STACK_NAME" "$(stack_parameter ContainerImage)" "$RELEASE_ID"
    printf 'proposed_image=build-and-push-pinned-source:%s:%s\n' \
        "$(stack_output "$BOOTSTRAP_STACK_NAME" RepositoryUri)" "$RELEASE_ID"
    printf 'mutates_aws=false\n'
}

dry_run_mode() {
    verify_identity
    require_stack_ready "$BOOTSTRAP_STACK_NAME"
    require_stack_ready "$STACK_NAME"
    assert_clean_source
    validate_release_id
    validate_template
    run_local_release_gates
    printf 'mode=dry-run\nrelease_id=%s\nmutates_aws=false\n' "$RELEASE_ID"
}

logs_mode() {
    verify_identity
    case "$LOG_TARGET" in
        api) log_group=/ecs/aws-iot-gateway-staging/api ;;
        event-worker) log_group=/ecs/aws-iot-gateway-staging/event-worker ;;
        *) fail "--logs requires api or event-worker" ;;
    esac
    aws_cli logs tail "$log_group" --since 30m --format short | tail -n "$LOG_LINES"
}

repository_uri() {
    stack_output "$BOOTSTRAP_STACK_NAME" RepositoryUri
}

validate_image_ref() {
    local repository=$1
    local digest
    [[ "$IMAGE_REF" == "$repository"@sha256:* ]] ||
        fail "image must use the exact staging ECR repository with an immutable @sha256 digest"
    digest=${IMAGE_REF#"$repository"@}
    [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] ||
        fail "image must use the exact staging ECR repository with an immutable @sha256 digest"
}

assert_ecr_immutable() {
    local mutability
    mutability=$(aws_cli ecr describe-repositories --repository-names "$ECR_REPOSITORY" \
        --query 'repositories[0].imageTagMutability' --output text)
    [[ "$mutability" == IMMUTABLE ]] ||
        fail "staging ECR repository must enforce immutable tags"
}

build_or_resolve_image() {
    local repository digest
    repository=$(repository_uri)
    require_command docker
    validate_release_id
    assert_ecr_immutable
    digest=$(aws_cli ecr describe-images --repository-name "$ECR_REPOSITORY" \
        --image-ids "imageTag=$RELEASE_ID" --query 'imageDetails[0].imageDigest' --output text 2>/dev/null || true)
    [[ ! "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] ||
        fail "release tag already exists; choose a new --release-id"
    log "authenticating Docker to staging ECR"
    aws_cli ecr get-login-password | \
        docker login --username AWS --password-stdin "${repository%%/*}"
    log "building and pushing linux/amd64 image tag $RELEASE_ID from pinned source $SOURCE_COMMIT"
    docker buildx build --platform linux/amd64 --provenance=mode=max --push \
        --label "org.opencontainers.image.revision=$SOURCE_COMMIT" \
        --tag "$repository:$RELEASE_ID" "$RELEASE_SOURCE_ROOT"
    digest=$(aws_cli ecr describe-images --repository-name "$ECR_REPOSITORY" \
        --image-ids "imageTag=$RELEASE_ID" --query 'imageDetails[0].imageDigest' --output text)
    [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "ECR did not return an immutable digest"
    IMAGE_REF="$repository@$digest"
    validate_image_ref "$repository"
    printf 'image_ref=%s\n' "$IMAGE_REF"
}

create_and_execute_change_set() {
    local change_set status reason change_set_json
    change_set="aws-deploy-${RELEASE_ID}-$(date -u +%Y%m%dT%H%M%SZ)"
    log "creating CloudFormation change set $change_set"
    aws_cli cloudformation create-change-set \
        --stack-name "$STACK_NAME" \
        --change-set-name "$change_set" \
        --change-set-type UPDATE \
        --template-body "file://$TEMPLATE" \
        --capabilities CAPABILITY_NAMED_IAM \
        --parameters \
        "ParameterKey=ContainerImage,ParameterValue=$IMAGE_REF" \
        'ParameterKey=InternalAppsSecretArn,UsePreviousValue=true' \
        'ParameterKey=IoTCredentialProviderEndpoint,UsePreviousValue=true' \
        'ParameterKey=EnvironmentName,UsePreviousValue=true' \
        'ParameterKey=DatabaseInstanceClass,UsePreviousValue=true' \
        'ParameterKey=RedisNodeType,UsePreviousValue=true' \
        >/dev/null

    if ! aws_cli cloudformation wait change-set-create-complete \
        --stack-name "$STACK_NAME" --change-set-name "$change_set"; then
        status=$(aws_cli cloudformation describe-change-set --stack-name "$STACK_NAME" \
            --change-set-name "$change_set" --query Status --output text 2>/dev/null || true)
        reason=$(aws_cli cloudformation describe-change-set --stack-name "$STACK_NAME" \
            --change-set-name "$change_set" --query StatusReason --output text 2>/dev/null || true)
        if [[ "$status" == FAILED && "$reason" == *"didn't contain changes"* ]]; then
            log "change set contains no infrastructure change"
            aws_cli cloudformation delete-change-set --stack-name "$STACK_NAME" \
                --change-set-name "$change_set" >/dev/null
            return 0
        fi
        fail "CloudFormation change set failed: ${reason:-unknown reason}"
    fi

    change_set_json=$(mktemp "${TMPDIR:-/tmp}/gateway-change-set.XXXXXX.json")
    if ! aws_cli cloudformation describe-change-set --stack-name "$STACK_NAME" \
        --change-set-name "$change_set" --output json >"$change_set_json"; then
        rm -f "$change_set_json"
        fail "could not inspect CloudFormation replacement/removal risk"
    fi
    if ! python3 "$RELEASE_DEPLOY_DIR/change_set_guard.py" "$change_set_json"; then
        aws_cli cloudformation describe-change-set --stack-name "$STACK_NAME" \
            --change-set-name "$change_set" \
            --query 'Changes[].ResourceChange.{Action:Action,LogicalResourceId:LogicalResourceId,ResourceType:ResourceType,Replacement:Replacement}' \
            --output table || true
        aws_cli cloudformation delete-change-set --stack-name "$STACK_NAME" \
            --change-set-name "$change_set" >/dev/null || true
        rm -f "$change_set_json"
        fail "ordinary staging deploy refuses removals or replacements outside the expected ECS task revisions"
    fi
    rm -f "$change_set_json"

    aws_cli cloudformation describe-change-set --stack-name "$STACK_NAME" \
        --change-set-name "$change_set" \
        --query 'Changes[].ResourceChange.{Action:Action,LogicalResourceId:LogicalResourceId,ResourceType:ResourceType,Replacement:Replacement}' \
        --output table
    log "executing non-destructive CloudFormation change set"
    aws_cli cloudformation execute-change-set --stack-name "$STACK_NAME" \
        --change-set-name "$change_set"
    aws_cli cloudformation wait stack-update-complete --stack-name "$STACK_NAME"
}

wait_for_services() {
    # Equivalent AWS CLI command: aws ecs wait services-stable
    aws_cli ecs wait services-stable --cluster "$CLUSTER_NAME" \
        --services "$API_SERVICE" "$WORKER_SERVICE"
    aws_cli ecs describe-services --cluster "$CLUSTER_NAME" \
        --services "$API_SERVICE" "$WORKER_SERVICE" \
        --query 'services[].{service:serviceName,desired:desiredCount,running:runningCount,pending:pendingCount,task_definition:taskDefinition}' \
        --output table
}

run_seed_task() {
    local task_definition network_configuration task_arn exit_code
    task_definition=$(aws_cli ecs describe-services --cluster "$CLUSTER_NAME" \
        --services "$API_SERVICE" --query 'services[0].taskDefinition' --output text)
    network_configuration=$(aws_cli ecs describe-services --cluster "$CLUSTER_NAME" \
        --services "$API_SERVICE" --query 'services[0].networkConfiguration' --output json)
    task_arn=$(aws_cli ecs run-task --cluster "$CLUSTER_NAME" \
        --task-definition "$task_definition" --launch-type FARGATE \
        --network-configuration "$network_configuration" \
        --overrides '{"containerOverrides":[{"name":"api","command":["/usr/local/bin/aws-iot-gateway-db-seed"]}]}' \
        --query 'tasks[0].taskArn' --output text)
    [[ -n "$task_arn" && "$task_arn" != None ]] || fail "seed task did not start"
    aws_cli ecs wait tasks-stopped --cluster "$CLUSTER_NAME" --tasks "$task_arn"
    exit_code=$(aws_cli ecs describe-tasks --cluster "$CLUSTER_NAME" --tasks "$task_arn" \
        --query 'tasks[0].containers[?name==`api`].exitCode | [0]' --output text)
    [[ "$exit_code" == 0 ]] || fail "seed task failed with exit code $exit_code"
    printf 'seed_task=%s\nseed_exit_code=0\n' "$task_arn"
}

verify_release() {
    require_command curl
    require_command python3
    local api_url time_url time_status time_body
    api_url=$(stack_output "$STACK_NAME" APIBaseURL)
    time_url=$(stack_output "$STACK_NAME" TimeBootstrapURL)
    curl --noproxy '*' --silent --show-error --fail --max-time 15 "$api_url/healthz" >/dev/null
    time_body=$(mktemp "${TMPDIR:-/tmp}/aws-time-bootstrap.XXXXXX")
    trap 'rm -f "$time_body"' RETURN
    time_status=$(curl --noproxy '*' --silent --show-error --max-time 15 \
        --output "$time_body" --write-out '%{http_code}' \
        --header 'Content-Type: application/json' --data '{}' "$time_url")
    [[ "$time_status" == 400 ]] || fail "time bootstrap route returned HTTP $time_status"
    grep -F 'invalid_time_bootstrap_request' "$time_body" >/dev/null ||
        fail "time bootstrap route returned an incompatible error"
    rm -f "$time_body"
    trap - RETURN
    python3 "$RELEASE_DEPLOY_DIR/test_internal_apps_smoke.py"
    python3 "$SMOKE" --profile "$PROFILE" --region "$REGION" \
        --stack "$STACK_NAME" --cluster "$CLUSTER_NAME"
    printf 'verify=ok\napi_base_url=%s\ntime_bootstrap_url=%s\n' "$api_url" "$time_url"
}

render_contract() {
    local destination=$1
    [[ "$destination" == /* ]] || fail "Contract candidate output must be an absolute path"
    [[ ! -e "$destination" ]] || fail "Contract candidate output already exists: $destination"
    AWS_PROFILE="$PROFILE" AWS_REGION="$REGION" \
        "$CONTRACT_RENDERER" --out "$destination"
    printf 'device_bootstrap_contract_candidate=%s\n' "$destination"
}

write_release_manifest() {
    local release_dir=$1
    local previous_image=$2
    local before_api=$3
    local before_worker=$4
    local after_api after_worker api_url time_url commit branch
    after_api=$(aws_cli ecs describe-services --cluster "$CLUSTER_NAME" --services "$API_SERVICE" \
        --query 'services[0].taskDefinition' --output text)
    after_worker=$(aws_cli ecs describe-services --cluster "$CLUSTER_NAME" --services "$WORKER_SERVICE" \
        --query 'services[0].taskDefinition' --output text)
    api_url=$(stack_output "$STACK_NAME" APIBaseURL)
    time_url=$(stack_output "$STACK_NAME" TimeBootstrapURL)
    commit=$SOURCE_COMMIT
    branch=$SOURCE_BRANCH
    mkdir -p "$release_dir"
    MANIFEST_PATH="$release_dir/manifest.json" \
    RELEASE_ID_VALUE="$RELEASE_ID" PROFILE_VALUE="$PROFILE" REGION_VALUE="$REGION" \
    ACCOUNT_VALUE="$EXPECTED_ACCOUNT_ID" STACK_VALUE="$STACK_NAME" COMMIT_VALUE="$commit" \
    BRANCH_VALUE="$branch" PREVIOUS_IMAGE_VALUE="$previous_image" IMAGE_REF_VALUE="$IMAGE_REF" \
    BEFORE_API_VALUE="$before_api" BEFORE_WORKER_VALUE="$before_worker" \
    AFTER_API_VALUE="$after_api" AFTER_WORKER_VALUE="$after_worker" \
    API_URL_VALUE="$api_url" TIME_URL_VALUE="$time_url" \
    python3 - <<'PY'
import json
import os
from datetime import datetime, timezone
from pathlib import Path

payload = {
    "schema_version": 1,
    "environment": "staging",
    "release_id": os.environ["RELEASE_ID_VALUE"],
    "created_at": datetime.now(timezone.utc).isoformat(),
    "aws": {
        "profile": os.environ["PROFILE_VALUE"],
        "region": os.environ["REGION_VALUE"],
        "account_id": os.environ["ACCOUNT_VALUE"],
        "stack": os.environ["STACK_VALUE"],
    },
    "source": {
        "repository": "aws-iot-gateway",
        "branch": os.environ["BRANCH_VALUE"],
        "commit": os.environ["COMMIT_VALUE"],
    },
    "images": {
        "previous": os.environ["PREVIOUS_IMAGE_VALUE"],
        "current": os.environ["IMAGE_REF_VALUE"],
    },
    "task_definitions": {
        "before": {
            "api": os.environ["BEFORE_API_VALUE"],
            "event_worker": os.environ["BEFORE_WORKER_VALUE"],
        },
        "after": {
            "api": os.environ["AFTER_API_VALUE"],
            "event_worker": os.environ["AFTER_WORKER_VALUE"],
        },
    },
    "outputs": {
        "api_base_url": os.environ["API_URL_VALUE"],
        "time_bootstrap_url": os.environ["TIME_URL_VALUE"],
    },
}
path = Path(os.environ["MANIFEST_PATH"])
path.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n")
path.chmod(0o600)
print(f"release_manifest={path}")
PY
}

deploy_mode() {
    verify_identity
    require_stack_ready "$BOOTSTRAP_STACK_NAME"
    require_stack_ready "$STACK_NAME"
    assert_clean_source
    assert_published_source
    validate_release_id
    prepare_pinned_release_source
    validate_template
    run_local_release_gates
    assert_source_unchanged
    prepare_build_release_source
    validate_template
    assert_source_unchanged

    local previous_image before_api before_worker release_dir contract_candidate
    previous_image=$(stack_parameter ContainerImage)
    before_api=$(aws_cli ecs describe-services --cluster "$CLUSTER_NAME" --services "$API_SERVICE" \
        --query 'services[0].taskDefinition' --output text)
    before_worker=$(aws_cli ecs describe-services --cluster "$CLUSTER_NAME" --services "$WORKER_SERVICE" \
        --query 'services[0].taskDefinition' --output text)

    build_or_resolve_image
    create_and_execute_change_set
    wait_for_services
    run_seed_task
    verify_release

    release_dir="$RUNTIME_ROOT/$RELEASE_ID"
    contract_candidate="$release_dir/device-bootstrap-contract-staging-next"
    mkdir -p "$release_dir"
    [[ ! -e "$contract_candidate" ]] || fail "release Contract candidate already exists: $contract_candidate"
    write_release_manifest "$release_dir" "$previous_image" "$before_api" "$before_worker"
    render_contract "$contract_candidate"
    printf 'deploy=ok\nrelease_id=%s\nimage_ref=%s\n' "$RELEASE_ID" "$IMAGE_REF"
}

case "$MODE" in
    status) status_mode ;;
    plan) plan_mode ;;
    dry-run) dry_run_mode ;;
    logs) logs_mode ;;
    verify)
        verify_identity
        require_stack_ready "$STACK_NAME"
        verify_release
        ;;
    render-contract)
        verify_identity
        require_stack_ready "$STACK_NAME"
        render_contract "$CONTRACT_OUT"
        ;;
    seed)
        verify_identity
        require_stack_ready "$STACK_NAME"
        run_seed_task
        ;;
    deploy) deploy_mode ;;
    *) fail "unsupported mode: $MODE" ;;
esac
