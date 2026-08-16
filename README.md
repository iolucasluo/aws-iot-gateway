# aws-iot-gateway

`aws-iot-gateway` 是 LivPet 和内部 Demo App 共用的 **Internal AWS API**。它拥有用户激活、全局唯一绑定、设备运行身份和受限设备操作；不实现 Developer、Project、Product、Factory、tenant、IDevID、RMA 或平台 OTA。

`/api/v1/openapi/*` 中的 `openapi` 只是 LivPet wire compatibility，产品和代码语义均使用 internal app。固定 PID 是 `feeder-x1`。

## 认证与隔离

内部 App 由 `INTERNAL_APPS_JSON` 在部署时配置。请求使用 LivPet 已有 HMAC v1：

- `X-Api-Key`
- `X-Timestamp`：7 位计数器 + 13 位 Unix 毫秒
- `X-Nonce`
- `X-Signature`
- 设备授权操作额外使用 `X-Device-Token`

签名域保持 `ExploreX-OpenAPI-HMAC-v1`。Redis 以原子 `SET NX` 消费 nonce；Redis 不可用时验签 fail closed。不存在登录、自助创建 App/凭证或管理 API。

## 进程与依赖

- PostgreSQL：activation、binding、token、设备、presence、命令、事件、媒体和 outbox 的权威状态。
- Redis：跨 API 副本 nonce 与限流协调，不是业务权威存储。
- AWS IoT Core：ThingName、MQTT client ID 和运行 `device_sn` 使用 Gateway 首次激活时生成的同一随机值。
- AWS KMS：独立 P-256 key 只签固定的 device-time COSE payload；Redis 对该公共窄路由做跨副本限流。
- `cmd/event-worker` 消费 IoT Rule → SQS 事件，并处理 lifecycle 与 Webhook outbox；部署可执行文件与
  ECS service 使用无歧义别名 `iot-gateway-event-worker`。
- worker 同时消费设备 60 秒 `status` 心跳和 MQTT LWT，按 AWS IoT 接收时间防乱序写入 presence；
  设备列表在连续三次心跳缺失后 fail closed 为离线，不再把陈旧 Shadow 当作连接状态。
- KVS 与 S3：提供受 binding 约束的 WebRTC、快照、录像和喂食音频能力。

隔离的 dev/staging IaC 位于 `deploy/cloudformation/`。内部 API 仍走 API Gateway → VPC Link → private NLB；另一个公开 ALB 的 HTTP listener 只转发精确的 `/api/v1/device-time/bootstrap`，默认拒绝其他 path。两者共用双 API ECS，后台为双 worker ECS、RDS PostgreSQL、ElastiCache Redis、SQS/DLQ、IoT Rule、KVS、S3 和独立 KMS time signer。它不是生产部署基线。

更新 CloudFormation 时，不得把 `describe-stacks` 返回的 NoEcho 参数值 `****` 当成原值重新提交。
需要保持现有 secret ARN 时使用 `UsePreviousValue`；确需显式传值时，只能从获批准的安全输入或当前
ECS task definition 的 `secrets[].valueFrom` 读取 ARN，不能输出 secret 内容。

## 当前 HTTP 契约

| Endpoint | 调用方 | 认证 |
| --- | --- | --- |
| `GET /healthz` | 运行检查 | 无 |
| `POST /api/v1/device-time/bootstrap` | 无可信 RTC 的设备 | 公共窄路由；COSE 验签后可信 |
| `POST /api/v1/openapi/device-activations` | 内部 App backend | HMAC v1 |
| `GET /api/v1/openapi/device-activations/{id}` | 内部 App backend | HMAC v1 |
| `POST /api/v1/openapi/device-activations/{id}/sessions` | LivPet commissioning 兼容路径 | HMAC v1 |
| `POST /api/v1/openapi/devices/list` | 内部 App backend | HMAC v1 |
| `POST /api/v1/openapi/devices/token` | 内部 App backend | HMAC v1 |
| `POST /api/v1/openapi/devices/unbind/{device_token}` | 内部 App backend | HMAC v1 |
| `POST /api/v1/openapi/devices/recommission/{device_token}` | 内部 App backend | HMAC v1 |
| `/api/v1/openapi/devices/{device_sn}/*` | 内部 App backend | HMAC v1 + `X-Device-Token` |
| `POST /api/v1/openapi/devices/{device_sn}/network-reconfiguration-authorizations` | 已绑定设备换网转发 | HMAC v1 + `X-Device-Token` |
| `POST /api/v1/device-activations/session/prepare-installation` | 设备 | activation code |
| `POST /api/v1/device-activations/qr/prepare-installation` | 设备 | activation code |
| `POST /api/v1/device-activations/confirm-installation` | 设备 | activation code + operational-key proof |

设备子路由覆盖 detail、shadow、commands、schedules、events、streams、snapshots、recordings 和 feed-audio。Bearer `/v1/activations`、`/v1/devices` 和旧 `/v1/device-activations/{id}/*` 已从 router 删除。完整目标路由和阶段验收见根仓 `docs/2026-08-13-internal-aws-api-implementation-plan.md`。

`recommission` 只接受 `reset_scope=runtime|all`：默认 `runtime` 仅清理 Wi-Fi 与运行身份，`all`
还清理设备本地用户数据。两种 scope 都必须先取得设备 ACK，Gateway 才撤销 token、释放 binding
并进入旧运行证书禁用流程；离线、超时、未知 scope 或 ACK 失败均保持 binding 有效。

为保持 LivPet backend 无需改动，单条 `factory_reset` 且 `value.scope=all`、`wait_ack=true` 的
`commands` 请求由 Gateway 内部映射为设备 `recommission(all)`。设备返回零值 ACK 后才释放 binding，
并按原 `commands.results[]` 格式将结果重新标记为 `factory_reset`；非零/缺失 ACK、关闭 `wait_ack`
或在批量命令中夹带该生命周期操作都会 fail closed，绑定保持有效。旧 `reset_wifi`、
`factory_reset(scope=wifi)` 以及缺失显式 scope 的 `factory_reset` 均返回 `400 invalid_request`，不会
下发 MQTT；更换网络必须使用 ACK-based `recommission(runtime)`。`schedule`、`feed_log`、`settings`
等不改变设备运行身份的 scope 继续走普通设备命令路径。

物理按键恢复出厂只清除设备本地 Wi-Fi 与用户数据，不释放 binding，也不删除 `device_sn` 或 AWS IoT
运行身份。原绑定用户重新配网时，App 将上述 `network-reconfiguration-authorizations` 请求连同该
binding 的 `X-Device-Token` 预签名后交给设备；请求 body 包含 `binding_external_user_id` 与
`wifi_credentials_sha256`，不上传 Wi-Fi 明文。Gateway 必须把前者与 active binding 精确比较；摘要
固定为四个字段的 versioned length-prefixed 字节串的 SHA-256 小写 hex。字段顺序为 domain
`ExploreX-Bound-WiFi-Reconfiguration-v1`、`device_sn`、`wifi_ssid`、`wifi_password`；每个字段先编码
为 UTF-8，再在前面写 4 字节无符号大端字节长度。App 与设备都必须拒绝包含 U+0000 的字段。设备
临时联网并原样转发，Gateway 只有在 HMAC、nonce、时效、外部用户 ID 和 device token 均属于当前
active binding 时返回授权；该接口不创建 activation、不改 binding。跨语言 known-answer：`device_sn` =
`fdr-00000000000000000000000000000001`、SSID = `Lab-WiFi`、password = `demo-pass` 时摘要为
`0c8f371a2f7e8d9b4c717f22fea097fe53750c0c3c9705611ca4c58594e48fcc`。成功响应固定为
`200 {"authorized":true,"device_sn":"<device_sn>"}`；拒绝沿用现有 Internal API 扁平兼容错误格式。

`POST .../{device_sn}/streams` 保留既有 `webrtc.v1` 能力协商：新请求必须完整携带
`requested_capabilities`，Gateway 将同一请求交给设备并校验设备 ACK；成功响应为 `201`，包含
`stream_id`、`firmware_version`、`supported_capabilities`、`granted_capabilities`、短期
`signaling` 和 `ice_servers`。LivPet 现有 backend 省略能力字段但明确 `wait_ack=true`，兼容层会按其
实际 SDP 补成接收视频、双向音频且无 DataChannel，并要求设备完整授予；部分授予会撤销本次 viewer
grant 并返回 409。该兼容映射仍以设备真实 ACK 为准，不伪造设备能力。

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
