# aws-iot-gateway

`aws-iot-gateway` 是 LivPet 和内部 Demo App 共用的 **Internal AWS API**。它拥有用户激活、全局唯一绑定、设备运行身份和受限设备操作；不实现 Developer、Project、Product、Factory、tenant、IDevID、RMA 或平台 OTA。

`/api/v1/openapi/*` 中的 `openapi` 只是 LivPet wire compatibility，产品和代码语义均使用 internal app。固定 PID 是 `feeder-x1`。

## 认证与隔离

内部 App 由 `INTERNAL_APPS_JSON` 在部署时配置。请求使用 LivPet 已有 HMAC v2：

- `X-Api-Key`
- `X-Timestamp`：7 位计数器 + 13 位 Unix 毫秒
- `X-Nonce`
- `X-Signature`
- 设备授权操作额外使用 `X-Device-Token`

签名域保持 `ExploreX-OpenAPI-HMAC-v2`。Redis 以原子 `SET NX` 消费 nonce；Redis 不可用时验签 fail closed。不存在登录、自助创建 App/凭证或管理 API。

## 进程与依赖

- PostgreSQL：activation、binding、token、设备、命令、事件、媒体和 outbox 的权威状态。
- Redis：跨 API 副本 nonce 与限流协调，不是业务权威存储。
- AWS IoT Core：ThingName、MQTT client ID 和运行 `device_sn` 使用 Gateway 首次激活时生成的同一随机值。
- AWS KMS：独立 P-256 key 只签固定的 device-time COSE payload；Redis 对该公共窄路由做跨副本限流。
- `cmd/aws-iot-gateway-worker` 消费 IoT Rule → SQS 事件，并处理 lifecycle 与 Webhook outbox。
- KVS 与 S3：提供受 binding 约束的 WebRTC、快照、录像和喂食音频能力。

隔离的 dev/staging IaC 位于 `deploy/cloudformation/`。内部 API 仍走 API Gateway → VPC Link → private NLB；另一个公开 ALB 的 HTTP listener 只转发精确的 `/api/v1/device-time/bootstrap`，默认拒绝其他 path。两者共用双 API ECS，后台为双 worker ECS、RDS PostgreSQL、ElastiCache Redis、SQS/DLQ、IoT Rule、KVS、S3 和独立 KMS time signer。它不是生产部署基线。

## 当前 HTTP 契约

| Endpoint | 调用方 | 认证 |
| --- | --- | --- |
| `GET /healthz` | 运行检查 | 无 |
| `POST /api/v1/device-time/bootstrap` | 无可信 RTC 的设备 | 公共窄路由；COSE 验签后可信 |
| `POST /api/v1/openapi/device-activations` | 内部 App backend | HMAC v2 |
| `GET /api/v1/openapi/device-activations/{id}` | 内部 App backend | HMAC v2 |
| `POST /api/v1/openapi/device-activations/{id}/sessions` | LivPet commissioning 兼容路径 | HMAC v2 |
| `POST /api/v1/openapi/devices/list` | 内部 App backend | HMAC v2 |
| `POST /api/v1/openapi/devices/token` | 内部 App backend | HMAC v2 |
| `POST /api/v1/openapi/devices/unbind/{device_token}` | 内部 App backend | HMAC v2 |
| `POST /api/v1/openapi/devices/recommission/{device_token}` | 内部 App backend | HMAC v2 |
| `/api/v1/openapi/devices/{device_sn}/*` | 内部 App backend | HMAC v2 + `X-Device-Token` |
| `POST /api/v1/device-activations/session/prepare-installation` | 设备 | activation code |
| `POST /api/v1/device-activations/qr/prepare-installation` | 设备 | activation code |
| `POST /api/v1/device-activations/confirm-installation` | 设备 | activation code + operational-key proof |

设备子路由覆盖 detail、shadow、commands、schedules、events、streams、snapshots、recordings 和 feed-audio。Bearer `/v1/activations`、`/v1/devices` 和旧 `/v1/device-activations/{id}/*` 已从 router 删除。完整目标路由和阶段验收见根仓 `docs/2026-08-13-internal-aws-api-implementation-plan.md`。

`POST .../{device_sn}/streams` 保留既有 `webrtc.v1` 能力协商：请求必须完整携带
`requested_capabilities`，Gateway 将同一请求交给设备并校验设备 ACK；成功响应为 `201`，包含
`stream_id`、`firmware_version`、`supported_capabilities`、`granted_capabilities`、短期
`signaling` 和 `ice_servers`。Gateway 不以固定默认能力伪造设备 ACK。

## 本地运行与验证

首次本地联调把 `.env.local.example` 复制为开发者自己的 `.env.dev`、`.env.cwj` 等
`.env.<name>` 文件；这些文件全部被 Git 忽略。它们只保存该开发环境的 Gateway/AWS 资源参数和
Internal App 配置，AWS access key 仍走 standard credential chain，不写入环境文件。

本地开发优先复用 `127.0.0.1:5432/6379` 上已有的 PostgreSQL/Redis 进程，但必须使用独立的
`aws_iot_gateway` 数据库和 Redis DB index（示例为 `/1`）。根工作区 `local-dev` skill 只探测、
不启停用户已有的进程；端口为空时才使用 `deploy/local/compose.yaml` 启动 fallback 依赖。
直接使用 fallback 时可运行：

```sh
docker compose --env-file .env.cwj -f deploy/local/compose.yaml up -d --wait
set -a
. ./.env.cwj
set +a
go run ./cmd/aws-iot-gateway
```

测试：

```sh
go test -race -count=1 ./...
go vet ./...
```

PostgreSQL integration test 通过 `TEST_POSTGRES_URL` 显式启用。真实 AWS 资源创建与部署只针对获授权的隔离 dev/staging 环境；凭据存在不等于生产授权。
