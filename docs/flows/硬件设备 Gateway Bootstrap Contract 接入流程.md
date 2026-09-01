# 硬件设备 Gateway Bootstrap Contract 接入流程

> **当前实现与发布边界说明。** 本文说明 Taixin、Anyka 和后续硬件如何接入已发布的
> `Gateway Device Bootstrap Contract v1`；它不定义新的 IoT 平台、Factory/IDevID、OTA 或设备出生身份。
>
> 最后按 Gateway exporter、已发布 staging Contract、Taixin renderer 与 Anyka pack adapter 核对：2026-08-18。

## 第一性原理：一个环境只有一份公开启动信任输入

设备冷启动时没有可信墙上时钟，不能先依赖 HTTPS 证书有效期建立 TLS。它仍必须知道：正常 Gateway
origin、其 HTTPS CA、唯一的 HTTP signed-time 路由，以及验证 COSE ES256 响应所需的 P-256 SPKI keyring。

这些是**环境事实**，不是板型事实。因此每个环境由 `aws-iot-gateway` 的部署 owner 生成并审核一份完整
公开 Contract；硬件只校验、嵌入或打包。重复让 Taixin、Anyka、Dell 或每台设备从 AWS/KMS/私钥推导同一组
值，会增加权限、漂移和不可审计的重烧录风险。

Contract 目录只有以下公开内容：

```text
device-bootstrap-contract/
  contract.json                         # canonical manifest + 文件 SHA-256
  service-ca.pem                        # HTTPS service CA（适用时）
  time-bootstrap/<kid>.der              # 一到两把 P-256 SPKI
```

它绝不含 private signer key、AWS credentials、activation code、App HMAC、device token、运行证书或设备私钥。
格式、字段验证和文件隔离以 [Device Service Bootstrap Contract v1](../contracts/device-service-bootstrap-v1.md)
为准。

## Owner 与固定边界

| 主体 | 拥有/执行 | 明确不拥有 |
| --- | --- | --- |
| Gateway 环境 deployment owner | 生成候选、审核并发布每个环境的一份 Contract；保留 AWS authority 或 local signer private key | 板端 renderer、设备私钥、Dell 长期配置 |
| `aws-iot-gateway` | Contract schema、exporter、受限 signed-time 路由和 Gateway runtime | 每块板的 build/pack 实现 |
| Taixin / TXW82x adapter | 验证 Contract，渲染 Git 忽略 generated header，并将 CA/keyring 编入固件 | AWS/KMS、local signer PEM、App HMAC |
| Anyka / Linux adapter | 验证 Contract，写入 immutable release env、只读 CA 和 keyring 文件 | AWS/KMS、local signer PEM、App HMAC |
| 后续硬件 adapter | 实现等价的完整目录校验、只读嵌入和启动时 keyring 使用 | 自定义环境事实或从 sibling/AWS 推导配置 |
| Dell/R730 | Taixin 的一次性受控构建机；校验公开输入并记录 Contract 摘要 | 第四个 Contract producer、AWS authority、预置 generated header |
| `aws-iot-demo-app` | 自己的 Gateway origin 与 HMAC 配置 | 此启动信任 Contract |

## 环境发布：候选先于替换，整包先于单文件

`development` 和 `staging` 是 Contract 的规范环境名。口语中的“local”只指 Gateway 部署位置；其可复制的
团队 Contract 目录仍是 `deploy/device-bootstrap/contracts/development/`，不是 `deploy/local/`。当前已发布的
共享 development endpoint 是 `http://192.168.11.131:14081`，时间响应由 `time-development-g1` 对应的 owner-only
signer 签名；Git 目录只保存公开 SPKI，不保存 signer 私钥。

| Contract 环境 | Producer 输入 | 已发布位置 | 硬件规则 |
| --- | --- | --- | --- |
| `development` | 稳定可达的本地/LAN Gateway + owner-only P-256 signer | `deploy/device-bootstrap/contracts/development/` | 仅显式 development firmware gate 可用 HTTP |
| `staging` | CloudFormation public Outputs + KMS `GetPublicKey` + 受跟踪 CA | `deploy/device-bootstrap/contracts/staging/` | HTTPS、CA 与 `aws-kms` provenance 必须齐全 |
| `production` | 正式 release authority | 受控 release artifact store | 不把 production 信任根当开发仓临时配置 |

发布顺序固定如下：

1. 获授权的 Gateway owner 以脚本导出一个**不存在的新绝对候选目录**；staging 只读取
   CloudFormation Outputs 与 KMS 公钥，不调用 `kms:Sign`，也不读取 secret。
2. 校验候选的 environment、URL、CA、`kid`、SPKI 类型、所有 SHA-256 与消费者兼容性。
3. 若候选与已发布目录不同，人工以**整个目录**替换对应环境目录，并和事实源变更一同提交；不能只复制
   `service-ca.pem` 或某个 `.der`，否则 `contract.json` 的完整性声明失效。
4. 硬件仓只复制 Git 中已审核的完整目录；Dell 仅接收其公开副本。

候选脚本与命令见 [部署者手册](../../deploy/device-bootstrap/README.md)。环境 `.env` 仅保存 Gateway
运行或 AWS profile 配置；它不是 DER/CA 的文件来源。staging DER 的可验证来源是 exporter 对已部署 KMS
公钥的读取，CA 的来源是受跟踪并审核的 trust root。

## 所有板共用的启动闭环

```mermaid
sequenceDiagram
    autonumber
    participant O as "Gateway deployment owner"
    participant C as "Published public Contract"
    participant A as "Board-specific build adapter"
    participant D as "Device with no trusted wall clock"
    participant T as "Public time-bootstrap route"
    participant G as "HTTPS Gateway"
    participant I as "AWS IoT Core"

    O->>C: Generate candidate, validate, publish full directory
    C->>A: Explicit Contract directory input
    A->>A: Validate schema, paths, SHA-256, environment and P-256 keyring
    A->>D: Embed/package public CA + keyring only
    D->>T: POST exact HTTP time-bootstrap URL with fresh nonce + chip UID
    T-->>D: COSE_Sign1 time statement
    D->>D: Match kid; verify ES256, nonce, chip UID and bounded RTT
    D->>D: Set trusted UTC
    D->>G: Strict HTTPS Gateway request
    D->>G: Activation / installation proof when required
    G->>I: Create and activate operational identity after proof
```

HTTP 被刻意限制为精确的 signed-time path。真实性来自 pinned COSE keyring、fresh nonce、chip UID binding
和有界 monotonic RTT；它不是通用的未认证 API。时钟建立后的 activation 在内的其他 Gateway 操作，均遵守
生命周期 flow 中的 HTTPS 与授权规则。

## Board adapter is where implementations diverge

Contract flow 是共用的，但板级代码不可互换：

| Adapter | Build-time result | Runtime trust location | Board-specific verification |
| --- | --- | --- | --- |
| Taixin / FreeRTOS | `gateway_release_config.py` renders a temporary ignored C header | firmware image embeds CA/keyring | renderer unit tests; formal package receipt binds `contract.json` SHA-256 |
| Anyka / Linux | pack script writes immutable release configuration | `/etc/backend_tls_roots.pem` and `config/time-bootstrap/<kid>.der` | pack contract tests and signed-time negative cases |
| New board | must implement the same consumer rules before claiming Gateway support | immutable/read-only image or equivalent protected partition | parser negative tests, cold-start time verification, then board HIL |

新板子不能仅因复制 endpoint 字符串就宣称“已连接 Gateway”。最低验收条件为：

1. 拒绝 unknown fields、unsafe path/symlink、无效 SHA-256、非 P-256 SPKI、重复 `kid`、跨环境 provenance 和
   HTTP release Contract；
2. 将 CA/keyring 保留在不可写 runtime configuration 之外，且绝不接收 private signer material；
3. 证明 cold boot 在 strict TLS 前验证有效 COSE time，并拒绝错误 `kid`、signature、nonce、chip UID、
   context、expiry、canonical encoding 或 excessive RTT；
4. 只在 trusted time 建立后进入现有 activation/operational-identity flow；以及
5. 在 board build/release evidence 中记录所消费的 `contract.json` digest。

QR/AP/BLE transport、chip SDK、build host 和 flashing method 仍是板级职责；不得重新定义 Gateway environment
data 或弱化共用 trust check。

## Fail closed, rotation and recovery

| Situation | Required behavior |
| --- | --- |
| Contract file or manifest hash differs | consumer rejects the complete input; no partial CA/key use |
| No published Contract for requested environment | selected hardware build fails rather than synthesizing values |
| signed-time response fails any verification | device remains without trusted time; do not bypass TLS or continue activation |
| candidate publication validation fails | retain the existing published Contract and create a new candidate after repair |
| time signer rotation | publish current + next public keys → release compatible hardware → switch signer → retire old key only after supported releases contain the new key |

两把 key 的上限只服务于明确的 rotation。先切换 signer 再分发 next public key 会使旧 firmware 无法启动；
为避免重烧录而分发 private signing material 则是破坏 trust boundary，而不是解决生命周期问题。

## Evidence boundary

Gateway publisher 的静态检查是：

```sh
sh deploy/device-bootstrap/tests/test_contract_publishers.sh
```

它验证 publisher layout 与边界，不等于真实板子证据。Gateway unit test、Taixin/Anyka consumer test、获授权的
remote build 和 board runtime validation 是相互独立的 evidence layer。flow 文档或 local parser test 不得被描述为
已部署 AWS 或 hardware proof。
