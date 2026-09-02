# 国内 `cn-staging` 本地/ECS 模拟

本目录是国内 `cn-staging` 的基础设施等价模拟。基础 Compose 启动 PostgreSQL、Redis、Mosquitto、coturn（及可选 MinIO）；自建 signaling 由独立 `explorex-signal` 进程承载。Gateway 的 `domestic-dev` 窄运行时另有明确的 operator 配置，覆盖 activation、development CA 签发、signed-time 和 provider-neutral MQTT ingest；在 Gateway composition wiring 与 Anyka consumer 完成前，媒体/WebRTC API 仍保持不可用并在 API 层显式返回 `503`。

## ExploreX signaling（Phase 10.1）

`explorex-signal` 是 DELL 上独立运行的 provider-owned control/data-plane。它提供短 TTL 内存 session、HTTP idempotency、viewer/device 单次 ticket、严格 offer/answer/ICE WebSocket relay 和 `/healthz`；服务重启会使未完成 session 失效，不写入 PostgreSQL，也不承载媒体或 TURN。

开发配置示例见 [explorex-signal-dev.env.example](./explorex-signal-dev.env.example)，systemd 形状见 [explorex-signal-dev.service.example](./explorex-signal-dev.service.example)。`ws://` 仅用于隔离 LAN contract smoke；App/生产验收必须改为受信任证书的 `wss://`，不能把本示例视为生产 TLS 配置。

本阶段只允许 standalone contract smoke。Gateway 尚未把 `RealtimeProvider` 接入 `/streams`，Anyka 尚未消费 `device_wss_url`，所以启动该进程不会改变现有国内 API 或设备行为。

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
cp deploy/domestic-staging/.env.example .env.cn-staging.local
# 只在本机 ignored 文件中替换 replace-with-local-secret
# state bind 保持 127.0.0.1；仅受控协议 harness 才设置 edge bind
docker compose --env-file .env.cn-staging.local \
  -f deploy/domestic-staging/compose.yaml config --quiet
docker compose --env-file .env.cn-staging.local \
  -f deploy/domestic-staging/compose.yaml up -d --wait postgres redis mqtt turn
```

可选启动 MinIO 只用于 ObjectStore adapter 的本地接口测试：

```sh
docker compose --env-file .env.cn-staging.local \
  -f deploy/domestic-staging/compose.yaml --profile object up -d --wait
```

试产前置 contract（只验证边界，不把 NO-SHIP 变成可部署）：

```sh
bash deploy/domestic-staging/test_trial_contract.sh
```

组合运行时入口（不启动 Gateway API，不访问 AWS）：

```sh
GATEWAY_ENVIRONMENT=staging \
GATEWAY_PROVIDER_COMPOSITION_PATH=deploy/domestic-staging/composition.cn-staging.example.yaml \
go run ./cmd/provider-composition-preflight
bash deploy/domestic-staging/test_provider_preflight_contract.sh
bash deploy/domestic-staging/test_runtime_audit_contract.sh
```

preflight 只读取公开的 composition 形状，输出 instance 的 capability/driver；凭据引用、证书路径和
bucket/key 值不会被打印。缺少显式路径、环境不匹配、国内组合包含 AWS data-plane 或文件越界时会
fail closed。

runtime audit 是 preflight 之后的装配前检查，不启动 Gateway API，也不访问 AWS 或其他网络：

```sh
GATEWAY_ENVIRONMENT=staging \
GATEWAY_PROVIDER_COMPOSITION_PATH=deploy/domestic-staging/composition.cn-staging.example.yaml \
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

该检查必须输出 `NO-SHIP`，直到真实 signaling service 和 feeder HIL 均有当前版本证据；MQTT ingest wiring 本身由 Gateway 启动时完成健康检查和四类 canonical topic 订阅。

停止只影响本 Compose project：

```sh
docker compose --env-file .env.cn-staging.local \
  -f deploy/domestic-staging/compose.yaml down
```

本地 MQTT listener 使用 loopback 发布端口和匿名测试配置，只能验证容器网络和协议 harness，不能用于 feeder HIL 或云 staging。国内设备接入必须在后续阶段切换到 MQTTS/mTLS、ACL 和受控证书。

Phase 7.2 增加 `mtls` profile：它使用独立的 8883 listener、客户端证书认证和按证书 CN
隔离的 ACL。证书、私钥和实际 ACL 文件只放在 operator-owned ignored 路径，不能提交到 Git。

```sh
# 先准备 CN_STAGING_MQTT_TLS_DIR/{ca.crt,server.crt,server.key,gateway-cn-staging.crt,gateway-cn-staging.key}
# 并从 mosquitto.mtls.acl.example 复制为 CN_STAGING_MQTT_ACL_FILE
# operator-owned composition copy 中 messaging endpoint 必须指向实际 mqtts://<host>:<port>
docker compose --env-file .env.cn-staging.local \
  -f deploy/domestic-staging/compose.yaml --profile mtls up -d --wait mqtt-mtls

GATEWAY_ENVIRONMENT=staging \
GATEWAY_PROVIDER_COMPOSITION_PATH=deploy/domestic-staging/composition.cn-staging.example.yaml \
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
GATEWAY_PROVIDER_COMPOSITION_PATH=deploy/domestic-staging/composition.cn-staging.example.yaml \
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
| Gateway API | `domestic-dev` 独立窄运行时：activation、local CA、signed-time；AWS 组合仍走原入口 | 设备消息/状态、媒体、WebRTC、对象存储未接入时必须显式不可用；不开放公网 |
| RTC signaling | Phase 10.1 DELL 独立 ExploreX signaling control/data plane | `18084` 仅 LAN staging；Gateway composition、Anyka consumer 和公网 TURN 另行验收 |

## 运行约束

- Compose 文件不得出现 AWS/SQS/KVS、长期 key、token、证书私钥或生产 endpoint。
- `private` 是容器内部网络；`edge` 仅承载需要发布的 MQTT/TURN 端口；`host-access` 只挂 PostgreSQL/Redis 以提供 loopback 诊断。
- stateful volume 需要备份/恢复演练；单 ECS 仅承诺可恢复，不承诺 HA。
- `composition.cn-staging.example.yaml` 只描述 provider instance 形状，不是可部署 secrets 文件。
- 该目录的 contract test 只验证 Compose 形状和 fail-closed 边界，不替代真实云、真机或性能验收。
