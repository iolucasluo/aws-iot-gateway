# aws-iot-gateway

`aws-iot-gateway` 是 LivPet 和内部 Demo App 共用的 **Internal AWS API**。它拥有用户激活、全局唯一绑定、设备运行身份和受限设备操作；`products` 与 `product_dps` 作为受控的只读激活/设备合同目录，不实现 Developer、Project、Product/DP CRUD/owner、Factory、tenant、IDevID、RMA 或 OTA campaign 平台。OTA 只保留签名版本和单设备 deployment。

`/api/v1/openapi/*` 中的 `openapi` 只是 LivPet wire compatibility，产品和代码语义均使用 internal app。App 必须先读取受控产品目录并在创建 activation 时显式提交 active PID；Gateway 不再默认 `feeder-x1`。

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
- KVS 与 S3：提供受 binding 约束的 WebRTC、快照、录像、喂食音频，以及与 media bucket 隔离的 OTA artifact。
- 局域网媒体：Gateway 只授权并经 MQTT 开关设备的 ONVIF/RTSP 短租约；不代理、
  不拨号 RFC 1918 设备地址，App 在同一 LAN 直连设备。

隔离的 dev/staging IaC 位于 `deploy/cloudformation/`。内部 API 仍走 API Gateway → VPC Link → private NLB；另一个公开 ALB 的 HTTP listener 只转发精确的 `/api/v1/device-time/bootstrap`，默认拒绝其他 path。两者共用双 API ECS，后台为双 worker ECS、RDS PostgreSQL、ElastiCache Redis、SQS/DLQ、IoT Rule、KVS、S3 和独立 KMS time signer。它不是生产部署基线。

更新 CloudFormation 时，不得把 `describe-stacks` 返回的 NoEcho 参数值 `****` 当成原值重新提交。
需要保持现有 secret ARN 时使用 `UsePreviousValue`；确需显式传值时，只能从获批准的安全输入或当前
ECS task definition 的 `secrets[].valueFrom` 读取 ARN，不能输出 secret 内容。

## 当前 HTTP 契约

| Endpoint | 调用方 | 认证 |
| --- | --- | --- |
| `GET /healthz` | 运行检查 | 无 |
| `POST /api/v1/device-time/bootstrap` | 无可信 RTC 的设备 | 公共窄路由；COSE 验签后可信 |
| `GET /api/v1/openapi/device-products` | 内部 App backend | HMAC v1；只读 active PID/name/capabilities 目录 |
| `POST /api/v1/openapi/device-activations` | 内部 App backend | HMAC v1 |
| `GET /api/v1/openapi/device-activations/{id}` | 内部 App backend | HMAC v1 |
| `POST /api/v1/openapi/device-activations/{id}/sessions` | LivPet commissioning 兼容路径 | HMAC v1 |
| `POST /api/v1/openapi/devices/list` | 内部 App backend | HMAC v1 |
| `POST /api/v1/openapi/devices/token` | 内部 App backend | HMAC v1 |
| `POST /api/v1/openapi/devices/unbind/{device_token}` | 内部 App backend | HMAC v1 |
| `POST /api/v1/openapi/devices/recommission/{device_token}` | 内部 App backend | HMAC v1 |
| `POST/GET /api/v1/openapi/ota/firmware-versions` | OTA 发布方 / 内部 App | HMAC v1 + `ota:publish` / `ota:deploy` |
| `POST /api/v1/openapi/ota/firmware-versions/{id}/complete` | OTA 发布方 | HMAC v1 + `ota:publish` |
| `POST /api/v1/openapi/devices/{device_sn}/ota-deployments` | Demo App / 内部 App | HMAC v1 + `ota:deploy` + `X-Device-Token` |
| `GET /api/v1/openapi/devices/{device_sn}/ota-deployments/{id}` | Demo App / 内部 App | 同上 |
| `GET /api/v1/openapi/devices/{device_sn}/status` | 内部 App backend | HMAC v1 + `X-Device-Token` |
| `POST/DELETE /api/v1/openapi/devices/{device_sn}/local-media-sessions` | 内部 App backend | HMAC v1 + `X-Device-Token`；`no-store` |
| `/api/v1/openapi/devices/{device_sn}/*` | 内部 App backend | HMAC v1 + `X-Device-Token` |
| `POST /api/v1/openapi/devices/{device_sn}/network-reconfiguration-authorizations` | 已绑定设备换网转发 | HMAC v1 + `X-Device-Token` |
| `POST /api/v1/device-activations/session/prepare-installation` | 设备 | activation code |
| `POST /api/v1/device-activations/qr/prepare-installation` | 设备 | activation code |
| `POST /api/v1/device-activations/confirm-installation` | 设备 | activation code + operational-key proof |

设备子路由覆盖 detail、shadow、commands、schedules、events、streams、local-media-sessions、snapshots、recordings、feed-audio 和 ota-deployments。Bearer `/v1/activations`、`/v1/devices` 和旧 `/v1/device-activations/{id}/*` 已从 router 删除。完整目标路由和阶段验收快照见根仓 `docs/branches/.archived/wicgeechan/2026-08-17/2026-08-13-internal-aws-api-implementation-plan.md`；当前未完成项见 `docs/branches/iot-flash-develop-internal-aws-follow-ups.md`。

OTA 不使用 AWS IoT Jobs。发布方先创建版本：staging/production 得到 15 分钟 presigned PUT，SHA-256 checksum 已绑定在 presigned URL，上传方只携带响应 `upload.headers` 中的 Content-Type、Content-Length 等已签名 header，S3 校验对象字节后再调用 `complete`；development 还可提交 origin 已列入 `OTA_DEV_ARTIFACT_ALLOWED_ORIGINS` 的局域网 `source_url`。Gateway 永不接收签名私钥。Demo App 只对声明 `ota.app.v1` 的设备展示升级入口，创建单设备 deployment 后轮询。HTTP 202 和设备 ACK 仅表示 `accepted`；只有重启 `boot` 事件的 version/build 与目标精确一致才转为 `succeeded`，安装后回到其他版本则记为 `rolled_back`。

同一设备最多一条 active deployment；设备必须有三分钟内的在线 presence，且通用 `commands` 不能冒充 `ota_install`。当前不提供 campaign、定时调度、批量发布、离线队列或自动重试。

设备冷启动的公开 Contract → signed-time → 严格 TLS 接入闭环，以及登记、activation、binding、reset、MQTT
事件与媒体的代码反向工程流程图见 [`docs/flows/目录说明.md`](docs/flows/目录说明.md)。每个场景单独成篇；
它们不恢复旧 IoT 平台的 Project、IDevID、Factory、RMA 或 OTA 语义。

`GET /api/v1/openapi/devices/{device_sn}/shadow` 不向 App 暴露 AWS Shadow envelope、metadata、
version 或 timestamp；Gateway 只返回已校验的 `state.reported` 设备投影。投影至少包含非空
`sn`、非空 `pid` 和 JSON boolean `online`，缺失或类型错误时 fail closed 为
`503 device_service_unavailable`。Wi-Fi MAC 字段固定为 `wifi_mac`。

`recommission` 只接受 `reset_scope=runtime|all`：默认 `runtime` 仅清理 Wi-Fi 与运行身份，`all`
还清理设备本地用户数据。两种 scope 都必须先取得设备 ACK，Gateway 才撤销 token、释放 binding
并进入旧运行证书禁用流程；离线、超时、未知 scope 或 ACK 失败均保持 binding 有效。

为保持 LivPet backend 无需改动，单条 `factory_reset` 且 `value.scope=all`、`wait_ack=true` 的
`commands` 请求由 Gateway 内部映射为设备 `recommission(all)`。设备返回零值 ACK 后才释放 binding，
并按原 `commands.results[]` 格式将结果重新标记为 `factory_reset`；非零/缺失 ACK、关闭 `wait_ack`
或在批量命令中夹带该生命周期操作都会 fail closed，绑定保持有效。旧 `reset_wifi`、
`factory_reset(scope=wifi)` 以及缺失显式 scope 的 `factory_reset` 均返回 `400 invalid_request`，不会
下发 MQTT；设备移交必须使用 ACK-based `recommission(runtime)`。物理恢复出厂后仍由原绑定用户换网
时使用下述预签名恢复路径，不释放 binding。`schedule`、`feed_log`、`settings` 等不改变设备运行
身份的 scope 继续走普通设备命令路径。

PTZ 控制沿用 `commands`：指定坐标使用
`{"code":"ptz","value":{"op":"move_to","pan_normalized":0.45,"tilt_normalized":-0.25}}`，
两轴归一化范围均为 `[-1,1]`，Gateway 不换算为设备步数。`wait_ack=false` 返回 HTTP 202 和
`accepted`，只表示命令已发布，不能解释为电机运动完成；`wait_ack=true` 才返回设备 ACK 结果。实时位置由
`GET /api/v1/openapi/devices/{device_sn}/status` 查询，Gateway 固定生成
`{"action":"ptz","op":"get_position","session_id":"..."}` 并等待对应 res topic ACK。响应包含
整数 `pan/tilt/limit_h/limit_v`、范围 `[-1,1]` 的归一化坐标和 JSON boolean `moving`。Gateway
校验 action、op、session、零值 code 和全部字段，但不保存或自行累加 PTZ 坐标；协议字段非法时返回
`502 invalid_device_response`，离线/超时和 busy/jammed 继续沿用现有设备错误映射。

物理按键恢复出厂只清除设备本地 Wi-Fi 与用户数据，不释放 binding，也不删除 `device_sn` 或 AWS IoT
运行身份。原绑定用户重新配网时，App 将上述 `network-reconfiguration-authorizations` 请求连同该
binding 的 `X-Device-Token` 预签名后交给设备；请求 body 包含 `binding_external_user_id` 与
`wifi_credentials_sha256`，不上传 Wi-Fi 明文。Gateway 必须把前者与 active binding 精确比较；摘要
固定为四个字段的 versioned length-prefixed 字节串的 SHA-256 小写 hex。字段顺序为 domain
`ExploreX-Bound-WiFi-Reconfiguration-v1`、`device_sn`、`wifi_ssid`、`wifi_password`；每个字段先编码
为 UTF-8，再在前面写 4 字节无符号大端字节长度。`device_sn` 使用受限可打印 ASCII；SSID/密码拒绝
C0 控制字符（U+0000–U+001F）和 DEL（U+007F），密码只允许空值、8–63-byte passphrase 或
64-byte hex PSK。设备
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

`POST .../{device_sn}/local-media-sessions` 创建 30–900 秒的 `local-media.v1` 租约（body
省略时为 300 秒）。Gateway 先以设备 PID 检查受控产品目录中的显式 capability，
再为该请求生成一次性 username/password/generation 和单设备严格递增 revision，经专用
MQTT `local_media(open)` 路径下发。该路径不写 `device_commands`，不允许通用
`commands` 冒充 `local_media`；`lm_` session 前缀由该 typed 路径保留，通用 command 必须在
持久化和 MQTT publish 前拒绝。只有设备返回字段集精确匹配、凭据指纹正确、且 ONVIF/RTSP
locator 同属一个 RFC 1918 IPv4 时，Gateway 才把凭据和 locator 一次性返回 App。首次应用 ACK
使用 `result=applied`；MQTT QoS 1 相同 revision/payload 的重投可使用 `result=duplicate`，但其余
ACK 字段仍须逐项精确匹配，避免把冲突或过期指令误判为成功。

`DELETE .../{device_sn}/local-media-sessions` 必须携带该租约的
`credential_generation`。Gateway 将其映射为 `target_credential_generation`，防止延迟 close
关闭后续租约。开启/关闭响应均使用 `Cache-Control: no-store` 和 `Pragma: no-cache`。
凭据只允许存在于当次 Gateway 请求内存、MQTT 运输和设备运行内存，不得写库或日志。
Gateway 不转发 ONVIF SOAP/RTSP 媒体，DP/PTZ 继续走 OpenAPI → MQTT。

## 本地运行与验证

首次本地联调把 `.env.local.example` 复制为开发者自己的 `.env.dev`、`.env.cwj` 等
`.env.<name>` 文件；这些文件全部被 Git 忽略。它们只保存该开发环境的 Gateway/AWS 资源参数和
Internal App 配置，AWS access key 仍走 standard credential chain，不写入环境文件。

本地开发优先复用声明端口上已有的 PostgreSQL/Redis 进程，但 `DATABASE_URL`/`REDIS_URL` 必须使用
loopback host、匹配 `GATEWAY_LOCAL_*_PORT`，并使用独立的 `aws_iot_gateway` 数据库和非零 Redis DB index
（示例为 `/1`）。PostgreSQL query 仅允许省略或精确为 `sslmode=disable`，Redis query 不允许出现，避免驱动
覆盖已校验的连接目标；真正导出给 seed/API 的环境快照会再次校验。任何一项不满足都在 seed 前失败关闭。
本仓 `tool/dev.sh` contract v1 拥有
`.env.<name>` 校验、端口探测、`deploy/local/compose.yaml` fallback、构建、显式 seed 和 API 前台生命周期；
它只复用、不停止开发者已有进程。聚合工作区 `local-dev` 的 `status/up/down/test` 只检查 contract version
并委托本入口，避免不同聚合仓复制 Gateway 启动实现：

```sh
tool/dev.sh contract-version
tool/dev.sh status --environment-id cwj
tool/dev.sh up --environment-id cwj
tool/dev.sh down --environment-id cwj
tool/dev.sh test
```

`up` 会先同步执行显式 seed runner，成功后才以前台生命周期 `exec` API；Ctrl+C 只终止 API，保留共享
依赖。compose project 固定为 `aws-iot-gateway-local`，不接受调用方 `COMPOSE_PROJECT_NAME` 覆盖。等价的
手工 fallback 流程是：

```sh
docker compose --env-file .env.cwj -f deploy/local/compose.yaml up -d --wait
set -a
. ./.env.cwj
set +a
go run ./cmd/aws-iot-gateway-db-seed
go run ./cmd/aws-iot-gateway
```

`aws-iot-gateway-db-seed` 是 development/staging 的显式一次性初始化命令：它先幂等执行 migration，再事务执行
`db/seeds/`，当前写入 `feeder-x1`、`lamp-x1` 和 `minion-cam-x1`。API/worker 启动不会自动 seed；漏掉该步骤时产品目录为空、
activation 必然返回 `product_unavailable`。该命令拒绝空环境和 `production`。详细边界见
[db/seeds/README.md](db/seeds/README.md)，staging 一次性 ECS 执行方式见
[deploy/staging/README.md](deploy/staging/README.md)。

需要让真实 Taixin/Anyka 硬件访问 Gateway 时，不要在各硬件仓手写 endpoint、CA 或 time SPKI。共享
`development`（local/LAN）与 `staging` 环境的公开 `Gateway Device Bootstrap Contract` 发布位置分别是
`deploy/device-bootstrap/contracts/development/` 与
`deploy/device-bootstrap/contracts/staging/`；硬件构建只复制存在且经审核的目录。Gateway owner 从本地 signer 或已部署 CloudFormation/KMS 的**公开** Outputs 生成候选
目录，再人工审核、替换并提交环境 Contract；Dell 和硬件仓不需要 AWS authority。命令、环境变量、轮换和回滚见
[deploy/device-bootstrap/README.md](deploy/device-bootstrap/README.md)，格式和消费者校验规则见
[docs/contracts/device-service-bootstrap-v1.md](docs/contracts/device-service-bootstrap-v1.md)。

本地真机命令使用 `wait_ack=true` 时，可在 `development` 环境显式设置
`AWS_IOT_DIRECT_ACK_SUBSCRIBER_ENABLED=true`。API 将通过当前 AWS 标准凭据链使用 MQTT over
WebSocket 订阅 `cmd/<AWS_IOT_TOPIC_PREFIX>/+/res`、`dt/<AWS_IOT_TOPIC_PREFIX>/+/status` 和
`dt/<AWS_IOT_TOPIC_PREFIX>/+/lwt`：ACK 写入本机 Redis，status/LWT 使用 Gateway 接收时间更新本地
PostgreSQL presence。该模式不读取共享 staging SQS，也不会替代 staging 的 IoT Rule/event-worker；
设备 payload 中自带的接收时间不会参与 presence 排序。`staging` 和 `production` 环境启用此开关
会启动失败。运行身份需要上述 topic 的 `iot:Connect`、`iot:Subscribe` 和 `iot:Receive` 权限。

测试：

```sh
go test -race -count=1 ./...
go vet ./...
```

PostgreSQL integration test 通过 `TEST_POSTGRES_URL` 显式启用。真实 AWS 资源创建与部署只针对获授权的隔离 dev/staging 环境；凭据存在不等于生产授权。

OTA 发布记录可选携带成对的 `target_environment_id` 与两位 `target_environment_code`，用于 Demo App
从当前 Gateway 的 available 版本中选择环境迁移包。两字段必须同时存在，编号范围为 `01`–`99`；普通
同环境 OTA 保持为空。环境迁移版本允许复用任意正整数 build，不受普通软件 OTA 的 build 单调递增规则
限制；普通软件 OTA 仍通过无目标环境的唯一 build、App 过滤和设备端校验保持严格递增。Gateway 仍只以
签名 release/manifest、目标 version/build 和设备重启事件判定
升级结果，不把环境标签或 MQTT ACK 当作安装成功证据。
