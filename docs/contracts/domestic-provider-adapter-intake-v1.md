# Domestic Provider Adapter Intake Contract v1

状态：Phase 7.8 design gate；仅定义真实国内 provider adapter 进入实现前的证据准入，不代表任何 Alibaba、Tencent、自建 CA/KMS 或 HSM 服务已经可用。

## 1. 目的与边界

本契约是 Gateway provider adapter 的统一 intake gate。它不选择云厂商，也不要求身份、MQTT、实时音视频、对象存储和签名必须来自同一家服务；每个 capability 仍由 composition 独立绑定 provider instance。

本契约适用于：

- Alibaba/Tencent 等托管服务的 adapter；
- DELL/ECS 上自建 CA、KMS/HSM、MQTT 或其他受控服务的 adapter；
- 兼容 AWS 语义但不属于 AWS 数据面的第三方实现。

本契约不做以下事情：

- 不凭经验发明 CA、KMS 或 HSM 的 HTTP/RPC wire protocol；
- 不在 `aws-iot-gateway` 中复制一套 domestic main；
- 不把 fake provider、MinIO、软件私钥或 mock 响应当作真实 provider 证据；
- 不创建云资源、证书、密钥、DNS、备案或公网入口。

## 2. 当前代码事实

实现者必须以当前代码为准，不得从旧文档推断已经存在的 wiring：

| 事实 | 当前 owner / 位置 | 约束 |
|---|---|---|
| Identity port | `internal/provider/ports.go` 的 `DeviceIdentity` | `Prepare`、`Activate`、`Rotate`、`Revoke`、`Health`；调用方传入 `context.Context` |
| Identity boundary | `internal/runtime/identity_bridge.go` | 绑定 composition instance；校验请求、返回引用、证书材料和 endpoint；provider 仍拥有网络/凭据 |
| Signer port | `internal/provider/ports.go` 的 `Signer` | 输入是待签名 digest；输出签名 bytes；调用方传入 `context.Context` |
| Signer boundary | `internal/runtime/signer_bridge.go` | 当前只接受 SHA-256 digest、ECDSA P-256 public key 和可验证 ASN.1 signature |
| Composition | `internal/provider/composition.go` | `identity`、`messaging`、`signer` 是核心 binding；国内 region 禁止 AWS data-plane |
| Runtime audit | `internal/runtime/runtime_plan.go`、`cmd/provider-composition-runtime-audit` | 当前只把 `messaging/mqtt_broker` 标记为 `SUPPORTED`；identity/signer 等仍是 `BLOCKED` |
| API main | `cmd/aws-iot-gateway/main.go` | 当前无条件创建 AWS IoT/KMS/KVS/S3 client；不得在 intake 阶段添加国内条件分支 |
| Persistence | `db/migrations/008_provider_composition.up.sql` 与现有 activation 表 | provider external ref 必须绑定 device/capability/instance；迁移不得破坏旧 AWS v1 |

## 3. Provider evidence package

每个 provider instance 在进入 adapter TDD 前，必须提供一份不含秘密的 evidence package。缺一项即保持 `INTAKE-BLOCKED`。

### 3.1 通用字段

| 必需证据 | 最小内容 | 不接受 |
|---|---|---|
| 官方来源 | 产品/协议名称、版本、官方文档或受控内部规范、发布日期 | 口头描述、搜索摘要、未经审核的博客或 SDK 猜测 |
| capability 映射 | 明确实现哪个 provider port，未实现的语义列为 unsupported | 用一个“大而全” client 冒充多个 capability |
| instance 事实 | `capability`、`driver`、region、endpoint 形状、依赖和 provider family | 在代码中硬编码厂商或由客户端请求选择 provider |
| 认证模型 | workload identity、RAM/服务账号、mTLS 或其他方式；只提供 secret/trust-root 引用 | 将 AK/SK、token、私钥写入 Git、composition、日志或测试 fixture |
| 网络边界 | DNS/TLS、端口、证书链、代理、超时、出站限制和服务端来源校验 | `InsecureSkipVerify`、任意 URL、把 LAN HTTP 当 staging HTTPS |
| 运行限制 | QPS、并发、payload/证书大小、配额、操作耗时和维护窗口 | 只证明 happy path，不提供限流/超时数据 |
| 失败语义 | HTTP/RPC 错误到 capability-scoped stable error 的映射 | 把 SDK error、endpoint 或 secret 原样返回 App/设备 |
| 观测 | request id、provider instance、attempt、最终状态；日志脱敏规则 | 记录 CSR、证书私钥、token、完整 URL query 或凭据 |

### 3.2 Identity provider (`DeviceIdentity`)

必须逐项给出 provider API 到以下语义的映射，并提供可重复的 sandbox 或离线签名回执：

| Port 操作 | 必须证明 |
|---|---|
| `Prepare` | 接受设备 CSR、PID 和设备标识；是否异步；如何生成/传递 idempotency key；返回的 certificate chain、certificate id、external id 和公开 MQTT/HTTPS endpoint 如何映射到 `IdentityRef` |
| `Activate` | 激活前置条件、重复调用语义、成功回执和“provider 成功但 DB 失败”时的补偿动作 |
| `Rotate` | 新旧证书重叠窗口、旧证书撤销时机、重复/并发 rotate 的结果、设备尚未安装新证书时的恢复方式 |
| `Revoke` | reason 约束、撤销传播时延、重复撤销、设备离线和恢复出厂后的访问收敛 |
| `Health` | 只检查 provider readiness 的最小调用、超时、依赖失败和是否允许 optional capability 降级 |

Identity evidence 还必须证明：

1. 设备 operational private key 只在设备侧生成和保存；Gateway 不接收、不转发、不持久化 private key。
2. 返回证书是可验证的 PEM/链，明确 leaf 与 CA chain 顺序、有效期和轮换规则；provider 不得把私钥材料混入返回字段。
3. `ProviderInstance` 由 server-side composition 绑定，设备/App 不能在请求体覆盖。
4. external id、certificate id 和 endpoint 是 provider-owned opaque ref；adapter 只做长度、控制字符、协议和 instance 边界校验，不把某一家厂商格式扩散到领域层。
5. `device_component_bindings` 的 active/rotating/revoked 状态、版本和撤销原因有单一 owner，并能在断电、重试和 DB 事务失败后恢复。

### 3.3 Signer provider (`Signer`)

必须明确 provider 的签名原语和 key lifecycle，至少覆盖：

| 必需证据 | 验收条件 |
|---|---|
| 输入语义 | 明确接收 pre-hashed 32-byte SHA-256 digest，还是接收原文；若不是 digest-only，不能直接注册到当前 port |
| 算法/编码 | 明确 ECDSA P-256/ES256 和 ASN.1 signature 编码；拒绝曲线或编码漂移 |
| 公钥来源 | `GetPublicKey` 或等价受控来源、SPKI 格式、fingerprint、key id/version；公钥进入公开 bootstrap Contract，不含私钥 |
| 轮换 | current + next key 的发布顺序、双 key 验证窗口、撤回旧 key 和设备/服务回滚 |
| 授权 | signer API 的最小服务权限、网络 allow-list、审计事件和密钥不可导出证明 |
| 失败/重试 | timeout、限流、服务不可用、重复请求、签名结果不可验证时的稳定错误和 fail-closed 行为 |
| 健康检查 | 不泄露签名材料的 readiness 调用；不能用“能读配置”冒充 KMS/HSM 可签名 |

只有当 adapter 能返回与注入 public key 匹配的 ASN.1 ECDSA signature，并通过 `CryptoSigner` 校验时，才能进入 time bootstrap wiring。不得因为国内 staging 缺少 signer 而将长期 software private key 作为 release/staging fallback。

## 4. 配置与秘密 ownership

provider 配置分三类，必须各自归属明确：

| 类别 | 可进入 Git 的内容 | 真实值位置 |
|---|---|---|
| Composition contract | capability、driver、instance id、region、endpoint 形状、非秘密 ref | Gateway child 的受审查 composition 文件 |
| Public trust material | CA certificate、signer SPKI、fingerprint、版本和有效期 | owning provider / device bootstrap Contract；只提交公开材料 |
| Secret/runtime value | access token、服务账号、mTLS private key、KMS key authority、数据库密码 | ignored local file、Docker secret、云 secret manager 或受批准注入机制 |

禁止在 root、Gateway 文档、日志、镜像层、测试 fixture、设备 manifest 或 App 参数中保存 secret。配置校验必须在任何 SDK/client 初始化前拒绝缺失、空值、越界、错误环境、错误 region、symlink 和不安全 endpoint。

## 5. 超时、重试、幂等与重放

adapter 进入实现前必须提交操作矩阵：

| 类别 | 最低要求 |
|---|---|
| timeout | connect/request/close 分离；每次调用有有限 deadline，不继承无限 context |
| retry | 只重试 provider 明确声明幂等的操作；未知结果不得盲目重试 `Prepare`/`Rotate` |
| idempotency | `Prepare`、`Activate`、`Rotate`、`Revoke` 的 key/operation id、保存位置和重复返回语义明确 |
| replay | request id、binding、device、TTL 和 provider instance 绑定；过期/跨实例/跨设备请求拒绝 |
| rate limit | provider 配额、Gateway limiter、退避和 operator 告警阈值可测量 |
| error mapping | 对外只返回 capability-scoped stable code；内部日志保留脱敏 cause 和 request id |

## 6. 三维安全审查门

### Who：主体与身份

- 列明设备、Gateway、provider service、operator 和 App 的主体及权限。
- 证明设备 operational key 的生成、安装、轮换、撤销和恢复出厂边界；没有 IDevID 证据时，不得声称出生身份或抗主动 MITM。
- 证明旧证书、旧 token 和被撤销 ref 在 rotate/revoke/reset 后无法继续访问。

### How：通道与消息

- 对 provider HTTPS/RPC、MQTTS、signed-time 和设备 activation 分别证明机密性、完整性、端点认证、授权和抗重放。
- 提供错误证书、错误公钥、坏签名、超时、乱序、重复和跨 composition instance 的负向测试。
- 任何日志、错误响应、metrics label 和 evidence artifact 都必须脱敏。

### What/When：状态、供应链与恢复

- 绘制 `factory-new → commissioning → activated → online → rotating → revoked/reset`，指定每个状态的 owner、持久化和原子性。
- 证明 provider 成功而 DB/设备失败、服务重启、网络分区和恢复出厂后的补偿与回滚。
- 公钥、CA、固件 manifest 和 composition 变更必须可审计、可回滚、不可降级；不把 DELL 单机运行当作 HA 或生产证据。

## 7. Intake 状态机与验收

```text
INTAKE-BLOCKED
      │  官方协议 + sandbox + 安全/生命周期证据齐全
      ▼
CONTRACT-REVIEW
      │  Gateway owner 审核 port 映射、错误/超时/持久化和三维威胁模型
      ▼
IMPLEMENTATION-READY
      │  adapter TDD、provider contract test、secret/trust-root 注入完成
      ▼
RUNTIME-READY
      │  DELL/ECS 集成、Anyka/App consumer、故障/轮换/回滚和必要 HIL 证据齐全
```

每次状态转换都必须附：

- provider 文档版本和 evidence package 摘要；
- 精确的 composition instance/driver/region；
- 测试命令、退出码、关键拒绝输出和产物 fingerprint；
- 未验证项、owner、过期时间和 rollback plan。

本阶段只固化 intake gate。当前 workspace 的真实状态仍为：`ca_mqtt_identity=BLOCKED`、`domestic_kms=BLOCKED`，没有任何 DELL fake provider 或真实云调用可作为准入证据。

## 8. 下一阶段进入条件

在实现真实 adapter 前，provider owner 必须补齐：

1. 官方协议/SDK 版本和 sandbox endpoint；
2. 非生产测试账号、最小权限策略和 secret/trust-root 注入方案；
3. Identity 或 Signer 的完整生命周期/签名语义与已知错误码；
4. 公钥/CA、证书轮换、撤销传播和备份恢复证据；
5. 可重复的 contract test 输入、预期输出和负向场景；
6. 明确 Gateway、Anyka、App、DELL operator 的 owner 与授权范围。

缺少上述输入时，下一步只能继续做文档/静态审计，不能把 adapter 注册为 `SUPPORTED`，不能启动完整国内 API，也不能进入 feeder 试产放行。
