# 国内 `cn-staging` 本地/ECS 模拟

> 当前源码目录为 `deploy/cn-staging`（原 `deploy/domestic-staging`）。首次初始化与已有实例更新
> 的操作边界见 [operations.md](operations.md)。目录更名不修改既有服务器 release、project、
> 数据卷或运行配置；下文保留各阶段说明，不能用早期阶段状态代替当前源码/部署验收。

真实期望配置在维护电脑的 Gateway `deploy/environments/aliyun-staging/` 留存，服务器为部署副本。
首次只读接管、配置漂移与服务器释放前的数据备份边界见 [本地事实源](operations.md#本地运行配置事实源)。

本目录是国内 `cn-staging` 的基础设施等价模拟。`compose.yaml` 提供 PostgreSQL、Redis、Mosquitto、coturn（及可选 MinIO）基础设施；`compose.single-node.yaml` 在同一 Docker host 上增加数据库 seed、Gateway API 和真实自建 signaling。当前 `domestic-dev` 窄运行时已覆盖 activation、development CA、signed-time、provider-neutral MQTT ingest 与 realtime session；它只用于 DELL/LAN staging，不等同于公网或量产配置。

## ExploreX signaling（Phase 10.1）

`explorex-signal` 是 DELL 上独立运行的 provider-owned control/data-plane。它提供短 TTL 内存 session、HTTP idempotency、viewer/device 单次 ticket、严格 offer/answer/ICE WebSocket relay 和 `/healthz`；服务重启会使未完成 session 失效，不写入 PostgreSQL，也不承载媒体或 TURN。

开发配置示例见 [explorex-signal-dev.env.example](./explorex-signal-dev.env.example)，systemd 形状见 [explorex-signal-dev.service.example](./explorex-signal-dev.service.example)。`ws://` 仅用于隔离 LAN contract smoke；App/生产验收必须改为受信任证书的 `wss://`，不能把本示例视为生产 TLS 配置。

Gateway 已通过 `RealtimeProvider` 接入 `/streams`，Anyka 国内 consumer 和 Demo App 已在 DELL LAN 完成真实媒体闭环。`explorex-signal` 仍保持独立进程/容器边界，不并入 Gateway 进程；它只转发 SDP/ICE，不承载媒体。

## DELL domestic-dev Gateway（V9.2）

该实例使用独立目录和端口 `192.168.11.131:14083`，不会停止或重启既有 `14081/14082` Gateway。运行配置必须同时满足：

- `GATEWAY_ENVIRONMENT=development`、`GATEWAY_PROVIDER_MODE=domestic-dev`；
- `GATEWAY_PROVIDER_COMPOSITION_PATH` 指向 `metadata.environment=development`、`metadata.region=cn` 的 operator-owned composition；
- CA 证书/私钥、signed-time local signer 和应用凭据位于 DELL ignored/operator-owned 路径；私钥为 `0600`；
- `DOMESTIC_MQTT_ENDPOINT` 仅接受非 AWS 的 `mqtts://` endpoint；当前 API 只返回该 provider-neutral endpoint，不输出 AWS IoT 字段。
- `DOMESTIC_MQTT_CLIENT_ID`、`DOMESTIC_MQTT_CLIENT_CERT_PATH` 和 `DOMESTIC_MQTT_CLIENT_KEY_PATH` 必须指向 operator-owned 的 mTLS 客户端材料；缺失或连接/订阅失败时 Gateway fail closed。

可参考 [gateway-domestic-dev.env.example](./gateway-domestic-dev.env.example) 生成 operator 配置。该示例不含真实密码、证书或 token，不能直接启动。

DELL 运行时 composition 可参考 [composition.dell-domestic-dev.example.yaml](./composition.dell-domestic-dev.example.yaml)。它显式绑定 `realtime.dell_signal`，endpoint 只描述 DELL LAN signaling；真实 auth token 仍只放在 ignored `gateway.env`/`signal.env`。

## 本地启动

真实 `.env.cn-staging.local` 必须位于仓库根目录并被 Git 忽略：

```sh
cp deploy/cn-staging/.env.example .env.cn-staging.local
# 只在本机 ignored 文件中替换 replace-with-local-secret
# state bind 保持 127.0.0.1；仅受控协议 harness 才设置 edge bind
docker compose --env-file .env.cn-staging.local \
  -f deploy/cn-staging/compose.yaml --profile harness config --quiet
docker compose --env-file .env.cn-staging.local \
  -f deploy/cn-staging/compose.yaml --profile harness up -d --wait postgres redis mqtt turn
```

可选启动 MinIO 只用于 ObjectStore adapter 的本地接口测试：

```sh
docker compose --env-file .env.cn-staging.local \
  -f deploy/cn-staging/compose.yaml --profile object up -d --wait
```

## 单 ECS / DELL 单机 application profile（Phase 11.1）

`single-node` profile 是从现有 systemd + Compose 联调形态迁移到“一台 ECS、一个 Compose project”的最小部署边界。它复用同一份 Gateway image 运行三个职责明确的容器：一次性 `gateway-seed`、长期 `gateway-api`、长期 `rtc-signaling`。PostgreSQL、Redis、MQTT mTLS 和 coturn 继续来自基础 Compose；匿名 `mqtt` harness 不会随该 profile 启动。

先在 operator-owned 目录准备配置，真实值不得提交：

```sh
cp deploy/cn-staging/single-node.stack.env.example .env.cn-single-node.local
install -d -m 0700 /opt/explorex-cn-single-node/config
install -d -o 10001 -g 10001 -m 0700 /opt/explorex-cn-single-node/runtime
cp deploy/cn-staging/gateway-single-node.env.example /opt/explorex-cn-single-node/config/gateway.env
cp deploy/cn-staging/signal-single-node.env.example /opt/explorex-cn-single-node/config/signal.env
cp deploy/cn-staging/composition.single-node.example.yaml /opt/explorex-cn-single-node/runtime/composition.yaml
chmod 0600 /opt/explorex-cn-single-node/config/*.env
# 再填入应用凭据、数据库/Redis URL、LAN/public endpoints、CA/signer 和 MQTT mTLS 文件。
```

`runtime` 内 Gateway 要读取的 composition、证书和私钥必须归 UID/GID `10001` 所有；私钥使用 `0400`，普通材料使用 `0444`。MQTT server certificate 的 SAN 必须同时覆盖容器内名称 `mqtt-mtls` 与 feeder 使用的 LAN/public hostname，否则 Gateway 内部连接或设备外部连接必有一侧证书校验失败。

渲染与启动必须同时加载 base 和 overlay；overlay 固定使用独立 Compose project `explorex-cn-single-node`。示例把数据库、Redis、MQTT、TURN/relay、Gateway 与 signaling 全部放在独立端口，不会占用 DELL 现有 project、volumes、`14081/14082/14083/18084` 或现有 TURN range：

```sh
docker compose --env-file .env.cn-single-node.local \
  -f deploy/cn-staging/compose.yaml \
  -f deploy/cn-staging/compose.single-node.yaml \
  --profile single-node config --quiet
docker compose --env-file .env.cn-single-node.local \
  -f deploy/cn-staging/compose.yaml \
  -f deploy/cn-staging/compose.single-node.yaml \
  --profile single-node up -d --build --wait
```

这个 profile 有意不启动 `iot-gateway-event-worker`：现有 worker 是 AWS SQS/IoT lifecycle consumer，国内 MQTT ingest 已由 provider-neutral runtime 承载；在有国内 outbox/queue contract 前启动它会重新引入 AWS，而不是“补齐服务”。MinIO 仍只在 `--profile object` 下提供接口验证，当前 Gateway 尚未把国内 ObjectStore 接入产品链路。

## 中国大陆 ECS cloud staging（Phase 12）

`compose.cloud-staging.yaml` 是 single-node profile 的公网 overlay。它不复制应用服务，只做四件事：

1. 用一个 Caddy edge proxy 按 `api-staging.livpet.top` / `rtc-staging.livpet.top` 转发 Gateway 和 signaling；
2. 对外发布 MQTT mTLS `8883`、TURN `3478` 与 UDP relay `49360-49400`；
3. 为 coturn 注入明确的 ECS 公网 IP，避免向 PeerConnection 发布 Docker 私网 relay candidate；
4. 把 `media_object` 显式绑定到 `aliyun_oss`，不启动 MinIO、不回退 AWS S3。

当前 Anyka ExploreX signaling transport 只支持 `ws://`，且本轮 ECS 验证已明确允许 HTTP/WS。因此 edge proxy 同时保留 HTTP/WS，并为 App 提供可选 HTTPS/WSS；这仍是 nonproduction staging，不是 production TLS 放行。

operator 目录使用 `/opt/explorex-cn-cloud-staging`。首次部署由准备脚本生成随机运行 secret、365 天 staging CA/证书、P-256 time signer、逐设备 MQTT ACL 和三个 `0600` 环境文件。脚本拒绝覆盖已有 operator 文件；OSS 凭据只从当前进程环境读入，不出现在命令行或 Git：

```sh
export CN_CLOUD_PUBLIC_IP=203.0.113.10
export CN_CLOUD_OSS_BUCKET=replace-with-media-bucket
export CN_CLOUD_OSS_ACCESS_KEY_ID=replace-with-access-key-id
export CN_CLOUD_OSS_ACCESS_KEY_SECRET=replace-with-access-key-secret
export CN_CLOUD_GATEWAY_IMAGE=explorex-cn-gateway:replace-with-immutable-revision
deploy/cn-staging/scripts/prepare_cloud_staging_operator.sh \
  /opt/explorex-cn-cloud-staging
```

`composition.yaml` 的 OSS endpoint 供 ECS 同地域访问；`GATEWAY_OBJECT_PUBLIC_ENDPOINT` 专用于生成 feeder/App 可访问的短期预签名 URL。AccessKey 只进入 `gateway.env`，不得进入 composition、App、固件或镜像。`stack.env` 中 `CN_STAGING_MINIO_ROOT_*` 仅满足 Compose 对未启用 profile 的预插值；cloud 命令既不启用 `object` profile，也不启动或回退到 MinIO。

渲染和启动必须同时加载三个 Compose 文件：

```sh
docker compose --env-file /opt/explorex-cn-cloud-staging/config/stack.env \
  -f compose.yaml -f compose.single-node.yaml -f compose.cloud-staging.yaml \
  --profile single-node config --quiet
docker compose --env-file /opt/explorex-cn-cloud-staging/config/stack.env \
  -f compose.yaml -f compose.single-node.yaml -f compose.cloud-staging.yaml \
  --profile single-node up -d --no-build --wait
```

发布前本地 contract：

```sh
bash deploy/cn-staging/test_cloud_staging_compose_contract.sh
bash deploy/cn-staging/test_prepare_cloud_staging_operator.sh
```

LAN 允许按用户要求继续使用 HTTP/WS；ECS overlay 已提供 Host reverse proxy，并可在 DNS 和 80/443 可达后自动签发 HTTPS/WSS 证书。Anyka 当前仍使用 WS，Demo App/API 可优先使用 HTTPS；这不改变本环境的 nonproduction 定位。实际 ECS 镜像必须把 `CN_STAGING_GATEWAY_IMAGE` 固定到不可变 revision 并用 `--no-build` 启动；本地 `:local + --build` 只用于 DELL 源码验收。

试产前置 contract（只验证边界，不把 NO-SHIP 变成可部署）：

```sh
bash deploy/cn-staging/test_trial_contract.sh
```

组合运行时入口（不启动 Gateway API，不访问 AWS）：

```sh
GATEWAY_ENVIRONMENT=staging \
GATEWAY_PROVIDER_COMPOSITION_PATH=deploy/cn-staging/composition.cn-staging.example.yaml \
go run ./cmd/provider-composition-preflight
bash deploy/cn-staging/test_provider_preflight_contract.sh
bash deploy/cn-staging/test_runtime_audit_contract.sh
```

preflight 只读取公开的 composition 形状，输出 instance 的 capability/driver；凭据引用、证书路径和
bucket/key 值不会被打印。缺少显式路径、环境不匹配、国内组合包含 AWS data-plane 或文件越界时会
fail closed。

runtime audit 是 preflight 之后的装配前检查，不启动 Gateway API，也不访问 AWS 或其他网络：

```sh
GATEWAY_ENVIRONMENT=staging \
GATEWAY_PROVIDER_COMPOSITION_PATH=deploy/cn-staging/composition.cn-staging.example.yaml \
go run ./cmd/provider-composition-runtime-audit
```

当前示例 composition 的预期结果是退出码 `2`、首行 `provider composition runtime audit: BLOCKED`：
`messaging/mqtt_broker` 会显示 `SUPPORTED`，身份、签名、实时和存储等尚未接入完整应用装配的
binding 会显示 `BLOCKED` 及确定原因。退出码 `2` 是“组合语法有效但尚未可启动”的机器可读信号，
不等于错误或试产放行；命令不输出 credential/bucket/key ref、endpoint 或任何秘密。

当前 identity/signer bridge 只有 host-side contract：它校验 provider instance、IdentityRef、
证书材料、endpoint 和 SHA-256/ECDSA P-256 签名边界，不实现 CA/KMS 网络协议。DELL 当前没有受审查的
CA/KMS 服务，因此不上传假证书、假私钥或 fake provider；`ca_mqtt_identity`、`domestic_kms` 仍由
runtime audit 标记为 `BLOCKED`。

真实 provider adapter 的进入条件、secret/trust-root ownership、生命周期、超时/重试、持久化、回滚和三维安全证据，统一见
[`docs/contracts/domestic-provider-adapter-intake-v1.md`](../../docs/contracts/domestic-provider-adapter-intake-v1.md)。没有官方协议和 sandbox 证据时，intake 保持 `INTAKE-BLOCKED`，不注册 fake provider、不启动完整国内 API。

该检查仍必须输出 `NO-SHIP`。真实自建 signaling 和 feeder LAN HIL 已有当前版本证据，但公网 HTTPS/WSS/TURN、持久固件/信任根、对象存储、备份恢复和量产 signer 尚未完成，不能用 LAN 结果替代生产门禁。

停止只影响本 Compose project：

```sh
docker compose --env-file .env.cn-staging.local \
  -f deploy/cn-staging/compose.yaml down
```

本地 MQTT listener 使用 loopback 发布端口和匿名测试配置，只能验证容器网络和协议 harness，不能用于 feeder HIL 或云 staging。国内设备接入必须在后续阶段切换到 MQTTS/mTLS、ACL 和受控证书。

Phase 7.2 增加 `mtls` profile：它使用独立的 8883 listener、客户端证书认证和按证书 CN
隔离的 ACL。证书、私钥和实际 ACL 文件只放在 operator-owned ignored 路径，不能提交到 Git。

```sh
# 先准备 CN_STAGING_MQTT_TLS_DIR/{ca.crt,server.crt,server.key,gateway-cn-staging.crt,gateway-cn-staging.key}
# 并从 mosquitto.mtls.acl.example 复制为 CN_STAGING_MQTT_ACL_FILE
# operator-owned composition copy 中 messaging endpoint 必须指向实际 mqtts://<host>:<port>
docker compose --env-file .env.cn-staging.local \
  -f deploy/cn-staging/compose.yaml --profile mtls up -d --wait mqtt-mtls

GATEWAY_ENVIRONMENT=staging \
GATEWAY_PROVIDER_COMPOSITION_PATH=deploy/cn-staging/composition.cn-staging.example.yaml \
GATEWAY_MQTT_CLIENT_ID=gateway-cn-staging \
GATEWAY_MQTT_TOPIC_PREFIX=cn-staging \
GATEWAY_MQTT_CA_FILE=.runtime/cn-staging/mqtt-tls/ca.crt \
GATEWAY_MQTT_CLIENT_CERT_FILE=.runtime/cn-staging/mqtt-tls/gateway-cn-staging.crt \
GATEWAY_MQTT_CLIENT_KEY_FILE=.runtime/cn-staging/mqtt-tls/gateway-cn-staging.key \
go run ./cmd/mqtt-provider-smoke
```

`mqtt-provider-smoke` 只通过 composition 绑定到 `mqtt_broker`，并验证 Gateway mTLS 客户端能连接；
它不会启动 Gateway API，也不会把 MQTT 连接成功夸大为设备命令 ACK 或 feeder HIL 通过。

Phase 7.5 可显式开启 bridge wait harness，验证同一 mTLS provider 收到 feeder 证书发布的第一条
ACK/status/event 消息。默认不等待消息，命令行为与旧 smoke 完全一致；开启后只输出收到消息的
topic 和 payload 字节数，不打印 payload：

```sh
GATEWAY_MQTT_SMOKE_WAIT_FOR_DEVICE_MESSAGE=1 \
GATEWAY_ENVIRONMENT=staging \
GATEWAY_PROVIDER_COMPOSITION_PATH=deploy/cn-staging/composition.cn-staging.example.yaml \
GATEWAY_MQTT_CLIENT_ID=gateway-cn-staging \
GATEWAY_MQTT_TOPIC_PREFIX=cn-staging \
GATEWAY_MQTT_CA_FILE=.runtime/cn-staging/mqtt-tls/ca.crt \
GATEWAY_MQTT_CLIENT_CERT_FILE=.runtime/cn-staging/mqtt-tls/gateway-cn-staging.crt \
GATEWAY_MQTT_CLIENT_KEY_FILE=.runtime/cn-staging/mqtt-tls/gateway-cn-staging.key \
go run ./cmd/mqtt-provider-smoke
```

等待期间由 operator-owned feeder certificate 向四类 canonical filter 之一发布测试消息；证书和
私钥必须留在 ignored runtime 路径。该 harness 只证明 mTLS adapter 到 bridge callback 的传输，
不替代 ACK store、event ingest、真实 feeder 或生产门禁。

## ExploreX 平台面与三门户（cloud staging 追加服务）

平台面是三门户的后端，与 `gateway-api` **同一镜像**（`CN_STAGING_GATEWAY_IMAGE`）的第二个进程：

| 服务 | 镜像 | 网络 | 对外入口 |
|---|---|---|---|
| `explorex-platform-api` | 同 `CN_STAGING_GATEWAY_IMAGE`，命令 `/usr/local/bin/explorex-platform-api` | `private` + `application` | 不发布端口；由门户 nginx 反代 |
| `admin-console` | `CN_STAGING_ADMIN_CONSOLE_IMAGE` | `application` | `https://admin-staging.livpet.top` |
| `developer-portal` | `CN_STAGING_DEVELOPER_PORTAL_IMAGE` | `application` | `https://developer-staging.livpet.top` |
| `manufacturer-portal` | `CN_STAGING_MANUFACTURER_PORTAL_IMAGE` | `application` | `https://manufacturer-staging.livpet.top` |

- 平台面环境在**独立** `platform.env`（示例 `platform.cloud-staging.env.example`，目标路径
  `/opt/explorex-cn-cloud-staging/config/platform.env`）：必须与 `gateway.env` 共用同一个
  `PLATFORM_CREDENTIAL_KEK`，否则 DB 凭证验签 401 / 平台面项目路由 503。
- 三门户是纯静态 SPA：镜像内 nginx 提供静态文件，并把 `/api/` 与 `/healthz` 反代到
  `explorex-platform-api:14081`（同 `application` 网络，不经过公网）。平台面自身**不**暴露公网路由。
- 平台面以项目自身凭证签名调用 `gateway-api:14080` 处理设备 I/O 与 OTA，因此它不需要
  IoT/S3/KVS 凭据；CORS 同源即可，示例里仍显式列出三个门户 origin。
- gateway 首次以平台面版本启动时会应用 **003–007 纯新增表**迁移；不涉及既有表改写。
- **国内 runtime 的 OTA 是 opt-in**：必须在 `gateway.env` 设置设备可达的
  `OTA_DEVICE_DOWNLOAD_BASE_URL`（例如 `https://api-staging.livpet.top`），gateway 才会用
  composition 里的 OSS `media_object` 绑定构造 OTA service；未设置时 developer 的 OTA 目录与
  单设备下发（以及平台面代理到它们的路由）返回 `503 ota_service_unavailable`，
  而 admin 的只读 OTA 视图（直接读数据库）不受影响。

## DELL Ubuntu 等价运行

DELL 可以作为单 ECS 的低并发 staging 模拟机。将 `.env.cn-staging.local` 放在 DELL 上的仓库根目录；只有在受控协议 harness 中才设置 `CN_STAGING_EDGE_BIND_ADDRESS` 为 LAN bind address。`mqtt` 仍是匿名协议 harness；feeder/App HIL 必须使用 `mqtt-mtls` profile、受控证书和 ACL。不要把 DELL 账号、密码、固定 LAN 地址或真实云凭据写进本目录。

在 DELL 上先做只读检查：

```sh
nproc
free -h
df -h
docker version
docker compose version
timedatectl status
ss -lntup
```

DELL 局域网协议 harness 只把 `CN_STAGING_EDGE_BIND_ADDRESS` 设为受控 LAN 地址；
`CN_STAGING_STATE_BIND_ADDRESS` 必须保持 `127.0.0.1`，避免把 PostgreSQL、Redis、MinIO 暴露到局域网。

LAN 运行与本机 loopback 的差异：公网 DNS、HTTPS CA、运营商 NAT、TURN UDP 可达性、云盘快照、KMS/HSM、OSS/COS 签名和安全组都必须单独验收。Compose 通过不等于阿里云网络或真实 WebRTC 通过。

## 服务边界

| 服务 | Phase 1 用途 | 对外暴露 |
|---|---|---|
| PostgreSQL | Gateway 权威库等价进程 | 默认 loopback；不公网暴露 |
| Redis | nonce/限流等短期协调 | 默认 loopback；不公网暴露 |
| Mosquitto | 国内 MQTT adapter 的本地 harness | `mqtt` 为匿名 loopback harness；`mqtt-mtls` 为证书认证/ACL profile，默认 loopback |
| coturn | TURN 进程/端口形状验证 | 默认 loopback；无公网 HIL 结论 |
| MinIO | 可选 ObjectStore 接口测试 | 仅 `object` profile、默认 loopback |
| Gateway API | `domestic-dev`：activation、local CA、signed-time、设备消息/状态与 realtime orchestration；AWS 组合仍走原入口 | 单机 profile 默认 `24083` loopback；LAN override 仅限受控联调 |
| RTC signaling | 真实 ExploreX signaling control/data plane，不承载媒体 | 单机 profile 默认 `28084` loopback；公网 WSS/TURN 另行验收 |

## 运行约束

- Compose 文件不得出现 AWS/SQS/KVS、长期 key、token、证书私钥或生产 endpoint。
- `private` 是 state 网络，`application` 是 Gateway/signaling 内部网络；二者均为 internal。`edge` 只承载显式发布端口；`host-access` 只挂 PostgreSQL/Redis 以提供 loopback 诊断。
- stateful volume 需要备份/恢复演练；单 ECS 仅承诺可恢复，不承诺 HA。
- `composition.cn-staging.example.yaml` 只描述 provider instance 形状，不是可部署 secrets 文件。
- 该目录的 contract test 只验证 Compose 形状和 fail-closed 边界，不替代真实云、真机或性能验收。
