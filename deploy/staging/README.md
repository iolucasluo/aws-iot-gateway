# Staging deployment

This deployment is intentionally limited to `ap-east-1` staging. It creates two API tasks, two competing `iot-gateway-event-worker` tasks, PostgreSQL, Redis, IoT rules and policy, SQS/DLQ, KVS permissions, private media storage, a KMS P-256 signed-time key, and an AWS-managed HTTPS API Gateway URL. Status-heartbeat and LWT rules feed PostgreSQL-backed device presence through the same ingest queue. There is no Developer/Project/Product/Factory/RMA/OTA service.

迁移和回滚边界见 [`docs/contracts/provider-composition-rollback-v1.md`](../../docs/contracts/provider-composition-rollback-v1.md)；该契约只记录可审计的本地/远端证据分层，不授予 AWS mutation 权限。

The `INTERNAL_APPS_JSON` value lives in one explicitly created Secrets Manager secret. Do not pass it as a CloudFormation parameter or commit it. RDS creates and rotates its own master secret.

## 受控发布入口

当前仓库的 staging owner entrypoint 是：

```sh
./deploy/staging/deploy.sh --env staging --status
```

聚合工作区从 `docs/coding-rules/skills/aws-deploy/scripts/deploy.sh` 薄转发到本脚本。入口只支持 AWS account `135359468083`、region `ap-east-1`、runtime stack `aws-iot-gateway-staging`，不支持 production、`--force`、DB reset、Redis flush 或自动替换 secret。

常用模式：

```sh
# 只读 AWS 状态与公开 health
./deploy/staging/deploy.sh --env staging --status

# 模板/source/release 计划；不 build/push/update
./deploy/staging/deploy.sh --env staging --plan

# 完整本地发布门禁；不 build/push/update
./deploy/staging/deploy.sh --env staging --dry-run

# CloudWatch 日志
./deploy/staging/deploy.sh --env staging --logs api 200
./deploy/staging/deploy.sh --env staging --logs event-worker 200

# 线上 health/time/HMAC smoke
./deploy/staging/deploy.sh --env staging --verify

# 本地生成公开 Device Bootstrap Contract 候选目录
./deploy/staging/deploy.sh --env staging \
  --render-device-bootstrap-contract /absolute/new/staging-contract-candidate
```

`--verify` 不更新 AWS 基础设施或设备绑定，但 HMAC smoke 会签发已绑定设备 token，并在 Redis 消费 nonce 以验证 replay rejection。它不会 activate、bind、unbind、command 或修改设备。

真实发布和单独 seed 都是 AWS mutation。`--deploy` 还要求 Gateway 工作树 clean、不是 detached HEAD，且当前 `HEAD` 与该分支 remote upstream 已发布 commit 完全一致。除用户对上述明确 account/region/stack 授权外，还必须设置精确确认变量：

```sh
CONFIRM_AWS_STAGING_DEPLOY=deploy-aws-iot-gateway-staging \
  ./deploy/staging/deploy.sh --env staging --deploy

CONFIRM_AWS_STAGING_SEED=seed-aws-iot-gateway-staging \
  ./deploy/staging/deploy.sh --env staging --seed
```

普通 deploy 保留现有 CloudFormation NoEcho 参数，`InternalAppsSecretArn`、`IoTCredentialProviderEndpoint`、`EnvironmentName`、`DatabaseInstanceClass` 和 `RedisNodeType` 均使用 `UsePreviousValue=true`。Internal App secret 创建或轮换是独立高风险操作，不由该入口执行。

部署先把已发布的 Gateway commit 导出为临时 release source 运行门禁；门禁完成后丢弃其内容影响，
再从同一 commit 重新导出全新的 build release source。Docker build 和 CloudFormation template 只读取
第二份固定输入，不读取 gate 可能改写的目录，也不继续读取可能并发变化的 live worktree。每次发布必须使用一个尚不存在的
immutable ECR release tag，并在本次事务中从 pinned release source 重新 build/push；不接受外部
`--image-ref`，也不复用已有 tag。linux/amd64 image 写入 `org.opencontainers.image.revision` label 和
BuildKit provenance，随后只把本次 build 返回的 digest 传给 CloudFormation。

镜像 digest 传给 CloudFormation 后，普通发布只允许 `APITaskDefinition` 和 `WorkerTaskDefinition` 因
`ContainerDefinitions` 更新生成新的无状态 task revision；其他 resource removal、replacement 或这两个
TaskDefinition 的其他属性替换仍会拒绝。成功 rollout 后等待 API/worker stable，运行 one-off seed、
health/time/HMAC smoke，先写入被 Git 忽略的 `.runtime/aws-deploy/<release-id>/manifest.json`，再生成
Contract candidate。脚本不自动执行二进制回退；migration 是 forward-only，回退前必须先审查 schema
兼容性并重新取得 AWS mutation 授权。

## 初次建站或 secret 轮换

下列步骤只适用于首次创建 staging 或经单独批准的 secret 轮换，不是普通镜像发布流程：

初次建站顺序：

1. Deploy `deploy/cloudformation/bootstrap.yaml`.
2. Build the container for `linux/amd64`, push an immutable ECR tag, and capture its URI.
3. Create/update the internal-app secret from a local file using `aws secretsmanager create-secret --secret-string file://...`.
4. Deploy `deploy/cloudformation/staging.yaml` with the image URI, secret ARN, and account credential-provider endpoint.
5. Wait for both ECS services to stabilize; API/worker startup completes schema migration but deliberately does not run seeds.
6. Run the image's one-off `aws-iot-gateway-db-seed` task from the active API task definition, then require exit code 0. This is a release gate for every fresh staging database and is safe to repeat:

   ```sh
   export AWS_PROFILE=explorex-staging-hk
   export AWS_REGION=ap-east-1
   cluster=aws-iot-gateway-staging
   api_service=aws-iot-gateway-staging-api
   task_definition="$(aws ecs describe-services --cluster "$cluster" --services "$api_service" --query 'services[0].taskDefinition' --output text)"
   network_configuration="$(aws ecs describe-services --cluster "$cluster" --services "$api_service" --query 'services[0].networkConfiguration' --output json)"
   seed_task_arn="$(aws ecs run-task --cluster "$cluster" --task-definition "$task_definition" --launch-type FARGATE --network-configuration "$network_configuration" --overrides '{"containerOverrides":[{"name":"api","command":["/usr/local/bin/aws-iot-gateway-db-seed"]}]}' --query 'tasks[0].taskArn' --output text)"
   test "$seed_task_arn" != "None"
   aws ecs wait tasks-stopped --cluster "$cluster" --tasks "$seed_task_arn"
   seed_exit_code="$(aws ecs describe-tasks --cluster "$cluster" --tasks "$seed_task_arn" --query 'tasks[0].containers[?name==`api`].exitCode | [0]' --output text)"
   test "$seed_exit_code" = "0"
   ```

7. Verify `/healthz`, the `TimeBootstrapURL` stack output, and the signed staging smoke below. The smoke now requires all active PIDs (`feeder-x1`, `lamp-x1`, `minion-cam-x1`) before checking device isolation; an incomplete catalog fails deployment verification.
8. Run `AWS_PROFILE=explorex-staging-hk AWS_REGION=ap-east-1 ./deploy/device-bootstrap/scripts/render-staging-contract.sh --out /tmp/device-bootstrap-contract-staging-next`. Review the candidate, then manually replace and commit `deploy/device-bootstrap/contracts/staging/`. Taixin/Anyka/Dell copy that public Git directory; do not give their build hosts AWS credentials or the KMS signing authority.

After a deployment, run the owner-side HMAC v1 smoke from the Gateway repository:

```sh
python3 deploy/staging/test_internal_apps_smoke.py
python3 deploy/staging/smoke_internal_apps.py \
  --profile explorex-staging-hk --region ap-east-1 \
  --stack aws-iot-gateway-staging --cluster aws-iot-gateway-staging
```

The smoke discovers the currently injected `INTERNAL_APPS_JSON` secret ARN from the active API task definition,
but never prints API secrets, device tokens, or response bodies. It is intentionally limited to HMAC v1
product-catalog, device-list/token isolation and nonce-replay checks; it does not activate, bind, unbind, command, or modify a device.
Use explicit `--app-id`, `--peer-app-id`, and `--binding-external-user-id` values when the staging fixture changes.

The ECS tasks use public egress addresses because staging deliberately avoids a permanent NAT Gateway. The task security group allows port 14080 only from the internal NLB and the signed-time ALB. The Internet-facing ALB listener forwards only exact `POST /api/v1/device-time/bootstrap` path traffic; every other path receives its fixed 404 response. PostgreSQL and Redis have no public endpoint and accept traffic only from the task security group.
