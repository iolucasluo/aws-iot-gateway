# DELL domestic-dev 自建 provider 契约 v1

## 目标

在 DELL Ubuntu 上提供一套不访问 AWS 数据面的 `domestic-dev` 组合，用于 feeder staging/HIL。该组合只覆盖当前可验证的最小设备闭环：

`App → Gateway activation API → 自建 Identity/CA → 设备运行证书 → 自建 MQTT/时间服务`

WebRTC、对象存储、队列和生产 HA 不在本契约中自动宣称完成。

## 组合与隔离

| capability | provider instance | DELL 实现 | 密钥/状态 owner |
|---|---|---|---|
| identity | `identity.dell_dev_ca` | Gateway provider port；使用 operator-owned CA 目录签发设备 leaf certificate | DELL ignored volume；CA private key 不进 Git/日志 |
| signer | `signer.dell_dev_local` | Gateway provider port；development-only 签名 key，供 signed-time | DELL ignored volume；不可用于 production |
| messaging | `messaging.dell_mqtt` | 已验证 Mosquitto mTLS profile | DELL operator-owned cert/ACL 目录 |
| time | `signer.dell_dev_local` | 复用 `timebootstrap.Service`，development local signer 由运行配置注入 | Gateway 只持有 provider handle，不导出 private key |
| realtime/object/queue | 未绑定 | 启动前报告 `BLOCKED`，不初始化 AWS SDK | N/A |

国内组合启动前必须满足：

1. `GATEWAY_ENVIRONMENT=development`、`GATEWAY_PROVIDER_MODE=domestic-dev`，且 composition `metadata.environment=development`、`metadata.region` 以 `cn` 开头；
2. 不存在 AWS data-plane provider、AWS endpoint、KVS/Shadow/credential ref；
3. identity、signer、messaging、time 的 provider instance 均已注册；
4. CA 目录为 regular directory，private key 权限为 `0600`，且不在工作区 Git 跟踪路径；
5. 缺失或非法配置在任何网络连接和设备写入前 fail closed。

## Identity contract

`DeviceIdentity.Prepare` 输入：设备生成的 CSR、设备 ID、PID；输出只包含 provider-neutral 字段：

- `external_id`：自建 CA serial/opaque ID；
- `certificate_id`：leaf serial；
- `certificate_pem`：设备 leaf certificate；
- `endpoint`：国内 MQTT endpoint（不得是 `*.amazonaws.com`）。

禁止输出或持久化 AWS IoT credential endpoint、role alias、KVS channel prefix、Shadow name。设备 private key 始终由设备生成，Gateway/CA 不接收。

当前 development adapter 在证书中绑定 device ID 和 CSR public key；activation ID、owner、TTL、幂等和安装 proof 由 Gateway activation 状态机/数据库绑定。把 activation ID 写入证书扩展属于后续 production CA adapter 契约，当前不宣称已实现。过期、错 owner、重复激活和已撤销身份仍必须由状态机拒绝。

## Signer/time contract

Signer 只接受 time bootstrap 规定的 SHA-256 digest，返回可由公开公钥验证的签名；key rotation 必须产生新 fingerprint 并保留旧 key 的验证窗口。development local signer 仅限 DELL `domestic-dev`，禁止被 `cn-staging`/production composition 复用。

## App/设备协议

App 继续使用 provider-neutral activation API；设备 runtime manifest 明确声明 `composition-cn-staging`，不得根据字段缺失回退 `internal-aws`。BLE/AP/QR 只传 activation contract 和 Wi-Fi，不能携带 CA private key、Gateway secret 或 AWS credential。

## 三维威胁审查摘要

- **Who**：App internal credential、设备 IDevID/运行证书、Gateway、CA/Signer 分开；CA/Signer 私钥仅 DELL volume，设备 key 不离开设备。
- **How**：BLE/HTTP development 通道不宣称抗主动 MITM；activation code、HMAC、nonce/TTL、CSR digest 和 owner binding 必须同时校验；MQTT 使用 mTLS/ACL。
- **What/When**：`factory-new → commissioning → activated → online`；签发、Wi-Fi 写入和 DB binding 需可恢复；失败不得回退 AWS；CA/Signer 轮换和恢复必须有审计记录。

## 验收边界

- host contract：composition 无 AWS data-plane、provider registry 完整、配置权限/路径 fail-closed；
- Gateway integration：本地 CA 签发 CSR、重复/过期/错 owner 负向用例、signed-time 公钥验证；
- DELL HIL：无 AWS 网络依赖下 App→Gateway→设备 activation、MQTT mTLS、设备状态；
- 不在本阶段宣称 production CA/HSM、公网 TLS、国内 WebRTC 或阿里云托管服务可用。
