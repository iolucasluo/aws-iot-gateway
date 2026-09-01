# Provider Composition Contract v1

状态：Phase 7.7 implementation baseline；composition validator、provider-neutral ports、国内 MQTT adapter、messaging factory、设备消息桥接、DELL mTLS 集成 smoke、国内运行时能力审计门和 identity/signer compatibility bridge 已落地；完整 API runtime wiring、其他国内 provider 和跨仓 HIL 仍需后续阶段验收。

## 0. 当前代码锚点

- `cmd/aws-iot-gateway/main.go` 当前在单一启动路径中创建 AWS IoT、AWS KMS、AWS KVS、S3 和 AWS credential chain。
- `internal/runtime/config.go` 当前强制 AWS IoT/KVS/S3 配置，非 development 的时间签名只允许 `aws-kms`。
- `internal/activation/types.go` 和 `internal/httpapi/server.go` 当前把 AWS endpoint、role alias、region 和 KVS prefix 暴露在激活/运行时响应。
- `internal/provider/composition.go` 提供严格 YAML composition 解码和启动前 fail-closed 校验；`internal/provider/ports.go` 定义共享的 messaging、identity、realtime、queue、signer 端口类型。
- `internal/modules/media/stream.go` 的窄 media port 已改为 provider-neutral channel/playback 类型；`assets.go` 的 `ObjectStore` 和 OTA `ArtifactStore` 是可复用的现有切入点。
- `internal/worker/ingest.go` 的队列 port 已收敛为 `provider.EventQueue`，AWS SQS 细节位于 `internal/platform/awssqs/`；`db/migrations/008_provider_composition.up.sql` 保存设备 capability 到 provider instance 的绑定事实。

这些是需要逐阶段收敛的现状，不是 v1 契约允许继续扩散的模型。

## 1. 目的

本契约定义 ExploreX 运行时如何把独立能力绑定到 provider instance。它解决的是“同一套业务内核按能力组合 AWS、国内云或自建服务”，不是把 AWS 代码复制成另一套国内代码。

本契约的三个层次必须保持分离：

```text
Domain contract      设备激活、命令、媒体、OTA 的产品语义
Provider adapter      AWS IoT/KVS/S3、国内 MQTT/RTC/OSS/COS 等实现
Composition policy    某地域/环境允许哪些 instance 及其依赖
```

## 2. 非目标

- 不提供动态 Go `.so` 插件、运行时下载代码或用户自选云厂商。
- 不建设 tenant、Project、Factory、产线身份或通用 IoT 平台。
- 不把 AWS Shadow 作为所有 MQTT provider 的必需能力。
- 不把“所有笛卡尔积都可运行”当作任意组合；组合必须通过依赖和数据地域校验。

## 3. 术语和稳定标识

| 名称 | 语义 | 示例 |
|---|---|---|
| capability | 可独立替换、认证、失败和计费的能力 | `identity`、`messaging`、`realtime` |
| driver | 一个 provider SDK/协议适配实现 | `aws_iot_mqtt`、`aliyun_oss` |
| provider instance | 带环境、地域和凭据引用的 driver 实例 | `messaging.cn_staging` |
| composition | 一组通过校验的 capability → instance 绑定 | `cn-staging` |
| external ref | provider 侧资源引用；只能由 adapter 解释 | certificate id、channel arn、object key |

Instance ID、composition ID 和协议版本均使用小写 ASCII `[a-z0-9][a-z0-9._-]{0,63}`，不允许把秘密、完整 credential 或动态 URL 放入 ID。

## 4. Capability ports

### 4.1 Identity / Provisioning

输入为设备 CSR、PID、activation context；输出为 provider-neutral `IdentityRef`、证书材料和公开 endpoint。设备 private key 只能在设备侧生成；Gateway 不接受 private key。

`internal/runtime/identity_bridge.go` 的 `IdentityRuntime` 将一个 `DeviceIdentity` client 绑定到 composition 选定的
provider instance。它在调用 provider 前校验 `DeviceID`/`PID`/CSR 的边界，在返回值和后续
`Activate`/`Rotate`/`Revoke` 前要求 `ProviderInstance` 精确匹配绑定，并拒绝缺少 external/certificate
identity、证书 PEM、`mqtts://`/`https://` endpoint、控制字符或 private-key 材料的引用。该 wrapper 不生成
证书、不持有私钥、不定义 CA API；实际 `ca_mqtt_identity` adapter 必须由后续 owning provider 注入。

最小语义：

```text
Prepare → inactive identity
Activate → proof 验证后激活
Rotate → 新旧身份重叠窗口
Revoke → 撤销并记录原因
```

### 4.2 Messaging

领域层只使用产品协议消息：命令、ACK、状态、事件。topic namespace 由 composition 生成，AWS `$aws/.../shadow/...` 只能存在于 AWS adapter。

```go
type DeviceMessaging interface {
    Publish(context.Context, DeviceMessage) error
    Subscribe(context.Context, DeviceSubscription) (SubscriptionHandle, error)
    Health(context.Context) error
}
```

每条命令必须携带 `device_id`、`binding_id`、`request_id`、过期时间和幂等语义；provider 选择不能由设备或 App 请求体覆盖。

`mqtt_broker` 是 provider-neutral messaging port 的 MQTT 实现：运行时 endpoint 必须使用 `mqtts://`，并通过 operator-owned 的 CA、client certificate 和 private key 完成双向 TLS；这些路径不进入 composition 文件或 Git。client identity 由证书 subject 与 broker ACL 共同约束，禁止 anonymous listener。adapter 只允许发布/订阅其 composition 声明的 `cmd/<topic_prefix>/...` 与 `dt/<topic_prefix>/...` 命名空间，并拒绝无过期时间或已过期的消息。

Gateway runtime 通过 `internal/runtime/messaging_bridge.go` 将该 port 适配到既有设备模块的窄 `Publisher` 接口，并把设备入站消息交给通用 `MessageHandler`。`DevicePublisher` 为每条出站消息统一补齐 30 秒短 TTL、复制 payload；`SubscribeDeviceMessages` 只注册以下四类固定过滤器：

```text
cmd/<topic_prefix>/+/res
dt/<topic_prefix>/+/status
dt/<topic_prefix>/+/lwt
dt/<topic_prefix>/+/event
```

桥接层不解析 ACK、presence 或 event payload，也不导入 `modules/device`、`modules/events`；这些语义仍由业务模块负责。部分订阅失败会关闭已创建句柄并 fail closed，`SubscriptionSet.Close` 并发幂等。这样同一桥接可复用于国内 MQTT、自建 broker 或其他实现 `DeviceMessaging` 的 provider，AWS-only 主装配路径保持不变。

### 4.3 Device State / Twin

Twin 是可选能力。Gateway-owned 状态投影是跨 provider 的权威事实；AWS Shadow、国内 broker retained message 或其他 twin 均为 adapter 细节。没有 twin instance 时，设备仍必须能完成命令和事件协议。

### 4.4 Realtime

```go
type RealtimeProvider interface {
    PrepareSession(context.Context, SessionRequest) (SessionDescriptor, error)
    CloseSession(context.Context, SessionRef) error
    Capabilities() RealtimeCapabilities
    Health(context.Context) error
}
```

`SessionDescriptor` 必须包含：

```json
{
  "session_id": "opaque-id",
  "provider_instance": "realtime.cn_staging",
  "protocol": "explorex-signal-v1",
  "client_config": {},
  "capabilities": {},
  "expires_at": "RFC3339"
}
```

`client_config` 是 provider-owned opaque object。KVS WebSocket envelope、ExploreX 自建信令、Alibaba RTS HTTP SDP 流程不能在领域层互相伪装。

### 4.5 Object / Artifact storage

媒体和 OTA 分开绑定，允许不同 provider。核心模型使用：

```json
{
  "storage_instance": "object.media_cn",
  "object_key": "staging/snapshots/device/id.h264",
  "checksum": {"algorithm": "sha256", "value": "lowercase-hex"}
}
```

数据库不得只保存裸 `object_key`，也不得把 presigned URL 持久化为权威事实。URL 只在授权窗口内生成。

### 4.6 Event queue

SQS-specific worker 必须收敛为 `EventQueue` port。Phase 2 的最小 port 只暴露 provider-neutral 的 `Receive`、`Ack` 和 lease token；visibility/lease 时长、失败重试和 poison-message 观测由具体 adapter/队列策略实现，不能泄漏到领域层。国内 staging 不得因 Gateway 进程启动而隐式访问 AWS SQS。

### 4.7 Signer / Time bootstrap

时间 bootstrap 使用 `crypto.Signer` 语义。AWS KMS、国内 KMS/HSM 和 development local signer 是不同 adapter；staging 不得为了绕过配置校验使用长期本地私钥。

`internal/runtime/signer_bridge.go` 的 `CryptoSigner` 将 provider `Signer` 的 digest-only 端口适配为
`crypto.Signer`：只接受 32-byte `SHA-256` digest，要求注入的 public key 为 ECDSA P-256，并验证 provider
返回的 ASN.1 ECDSA signature 能通过该公钥校验后才交给 time bootstrap。返回值会复制，provider 错误和
invalid signature 均 fail closed。该 bridge 不选择或实现任何 KMS/HSM 协议；`domestic_kms` 仍需真实
provider adapter、公开公钥获取和 health/timeout 契约后才能注册。

## 5. Composition 文档形状

部署只提供一个已审查的 composition 文件，示例：

```yaml
api_version: explorex.io/v1
kind: ProviderComposition
metadata:
  id: cn-staging
  environment: staging
  region: cn
instances:
  identity.cn_staging:
    capability: identity
    driver: ca_mqtt_identity
    credential_ref: secret://cn-staging/identity
  messaging.cn_staging:
    capability: messaging
    driver: mqtt_broker
    endpoint: mqtts://mqtt.example.invalid:8883
    trust_anchor_ref: config://cn-staging/mqtt-ca
  realtime.cn_staging:
    capability: realtime
    driver: explorex_signal
    endpoint: https://rtc.example.invalid
  object.media_cn:
    capability: object
    driver: aliyun_oss
    bucket_ref: secret://cn-staging/media-bucket
  object.ota_cn:
    capability: artifact
    driver: tencent_cos
    bucket_ref: secret://cn-staging/ota-bucket
  signer.cn_staging:
    capability: signer
    driver: domestic_kms
    key_ref: secret://cn-staging/time-key
bindings:
  identity: identity.cn_staging
  messaging: messaging.cn_staging
  realtime: realtime.cn_staging
  media_object: object.media_cn
  ota_artifact: object.ota_cn
  signer: signer.cn_staging
policy:
  forbid_regions: [aws]
  allow_mixed_provider: false
```

以上 endpoint、bucket、secret ref 只是形状示例，不是可部署值；真实值只能进入 child 的 ignored local config 或批准的 secret provider。

运行时装配顺序固定为：先由 `LoadProviderComposition` 完成 bounded、regular、non-symlink 和环境匹配校验，再由 `BuildMessaging` 解析 `bindings.messaging` 并构造对应 adapter，最后由调用方显式执行 provider `Health`。factory 不在未知 driver 上 fallback，也不在构造阶段隐式建立网络连接；当前已注册的 `mqtt_broker` 继续执行本节的 `mqtts`/mTLS 约束。AWS 主 API 的既有装配路径保持兼容，待 identity、signer、realtime 和 storage 等 provider 具备后再按同一顺序接入完整 composition runtime。

`BuildDomesticRuntimePlan` 是完整应用装配前的离线审计门：它再次校验 composition，要求 `metadata.region` 以 `cn` 开头，并按 binding 的稳定字典序报告当前已注册 adapter 或阻塞原因。该函数只返回非敏感的 capability/driver/状态，不读取运行配置、不创建 SDK client、不解析 endpoint/credential/bucket/key ref，也不执行网络 I/O。`cmd/provider-composition-runtime-audit` 将结果格式化为可审计文本：退出码 `0` 表示所有显式 binding 都有应用 adapter，退出码 `2` 表示 composition 合法但仍有未接入 binding，退出码 `1` 表示 composition/环境无效。当前国内注册表只有 `messaging/mqtt_broker`；`ca_mqtt_identity`、`domestic_kms`、`explorex_signal` 及 OSS/COS、queue、twin 等绑定在完整 application wiring 前保持 `BLOCKED`。Identity/Signer bridge 只提供端口边界校验和兼容类型，不改变该注册表状态。该命令不能替代 API server，也不会改变 AWS-only 主装配。

`cmd/mqtt-provider-smoke` 的 `GATEWAY_MQTT_SMOKE_WAIT_FOR_DEVICE_MESSAGE=1` 是仅供 operator 使用的集成 harness：它复用同一 factory 和 `SubscribeDeviceMessages`，等待一条四类 canonical filter 消息后只输出 topic/长度，并在退出时关闭订阅。默认值关闭等待；该模式不写 ACK store、不解析业务 payload，也不改变 AWS-only 主装配。

## 6. 依赖和 fail-closed 校验

Gateway 启动时必须校验：

1. 每个必需 capability 恰好绑定一个 instance；可选 capability 明确标记 optional。
2. instance 的 driver 与 capability 匹配，所有依赖 instance 存在且地域策略允许。
3. `realtime.aws_kvs` 必须声明 AWS IoT credential provider/身份依赖；没有依赖时拒绝组合，不运行到网络超时。
4. domestic composition 的 required data-plane、signer、queue 和 storage 不能解析到 AWS instance。
5. 配置缺失、未知字段、重复 instance、过期 endpoint、trust anchor 不匹配均 fail closed。

Phase 2 的最小实现把 `identity`、`messaging`、`signer` 作为所有设备组合的核心绑定；
`realtime`、`object`、`artifact`、`twin`、`queue` 通过是否出现在 `bindings` 中显式选择，
未绑定即保持可选，不为未启用能力创建假 provider。后续产品 profile 可再声明更窄的 required set。

## 7. HTTP/API 兼容

新增 `installation.v2` 和 `realtime session v2` provider-neutral 响应；现有 AWS v1 响应保留至所有旧设备/App 完成迁移。

v2 不再输出 `aws_iot_endpoint`、`aws_region`、`aws_iot_role_alias`、`aws_kvs_channel_prefix` 等核心字段，而输出 `component_bindings` 和 provider-owned opaque config。v1/v2 必须通过明确版本或 `Accept` 选择，不做字段猜测和自动 fallback。

## 8. 持久化和生命周期

新增 `device_component_bindings` 记录设备、binding、capability、provider instance、external ref、版本、状态和撤销原因；数据库 partial unique index 保证同一设备每项 capability 只有一个 active instance。原有 AWS 列保留迁移期，采用双读/双写和回填，不做破坏性 rename。

生命周期必须覆盖：

```text
factory-new → commissioning → activated → online
            → rotating → revoked/reset
```

recommission/reset 需要遍历该设备全部 active bindings，而不只撤销 AWS certificate。Stream、asset、OTA 的 provider external ref 都必须绑定 device/binding/session/TTL。

## 9. 错误、超时、重试和重放

- 每个 adapter 设置独立 connect/request/close timeout；不继承一个无限 context。
- 只对已知幂等操作重试；创建 identity、创建 stream、上传 artifact 必须使用 idempotency key。
- 外部错误映射为 capability-scoped stable error code，不泄露 SDK、SQL、endpoint 或 secret。
- outbox/worker 记录 attempt、provider instance、request id 和最终状态；重放必须受 binding、device、session、TTL 约束。
- readiness 只检查 required instances；optional provider 的故障不能使无关能力假性健康。

## 10. 验收组合

至少具备以下 contract test 组合：

| 组合 | 用途 |
|---|---|
| AWS IoT + AWS KVS + S3 | 海外现有基线 |
| AWS IoT + AWS KVS + OSS | 证明对象存储独立替换 |
| 国内 MQTT + 国内 realtime + OSS/COS | 国内无 AWS 目标 |
| 国内 MQTT + AWS KVS | 仅在显式 AWS credential dependency 成立时允许 |
| AWS MQTT + 国内 realtime | 证明 realtime 可独立替换 |

每个组合都要覆盖激活、命令/ACK、在线状态、媒体能力、快照、OTA、rotate/revoke、过期、重试和 rollback；fake adapter 不能替代真机或真实云证据。

## 11. 版本和 owner

- Gateway：本契约、activation/realtime/storage/queue API 是生产者。
- Anyka：runtime manifest、MQTT/realtime 设备行为和固件产物是生产者/消费者边界。
- App：realtime client adapter 和 v2 消费是消费者。
- 修改顺序：Gateway → Anyka → App → 根仓 gitlink。
- breaking change 必须升主版本；新增 provider 只增加 instance/adapter，不修改旧 AWS v1 语义。
