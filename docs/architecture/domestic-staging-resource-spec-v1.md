# 国内 cn-staging 资源与 DELL/ECS 等价模拟清单 v1

状态：Draft（Phase 0；资源创建前必须验收）

## 1. 决策边界

一个 ECS 运行 Docker Compose 足以承载**低并发 staging/试产验证**，但不等价于生产高可用。它适合先验证 provider contract、状态机、网络连通和失败恢复；数据库备份、磁盘故障、单机重启和升级窗口必须作为已知风险记录。

国内 staging 的成功标准是“设备和 App 的数据面不需要 AWS”，不是“所有组件必须来自同一家国内云”。OSS、COS、自建 broker、自建 WebRTC、国内 KMS 可以按 composition 独立组合。

## 2. 单 ECS 起步规格（待压测确认）

| 项目 | 起步建议 | 必须核验 |
|---|---|---|
| ECS | 4 vCPU、8 GiB RAM、100 GiB SSD 起步 | Gateway、PostgreSQL、MQTT、RTC、TURN 并发压测后再调大 |
| 系统 | Ubuntu LTS、Docker Engine、Compose v2 | 内核 conntrack、UDP、时钟同步、自动安全更新 |
| 公网 | 1 个固定公网 IPv4；域名指向反向代理 | HTTPS 证书、国内备案/ICP、云安全组 |
| 数据盘 | PostgreSQL、broker、日志分别使用目录/卷 | 快照、异地备份、磁盘告警；不能把单盘当高可用 |
| 出口 | HTTPS、MQTTS、STUN/TURN 所需出站 | 云厂商出站策略、运营商链路、DNS |

这是容量起点，不是云厂商 SLA 或生产规格承诺。若 DELL 主机低于 4 vCPU/8 GiB，先降低并发做功能验证，不能据此推导 ECS 生产容量。

## 3. Docker Compose 服务边界

### 必需

```text
reverse-proxy       443/TCP，唯一公网 HTTP 入口
gateway-api         activation、binding、device API
gateway-worker      event/outbox/异步 lifecycle
postgres            权威业务数据，只绑定 localhost/内部网络
redis               nonce、限流、短期协调状态
mqtt-broker         国内 MQTT adapter 的本地等价实现
rtc-signaling       ExploreX signaling adapter
coturn              3478 UDP/TCP、5349 TLS（按需要开放）
```

### 可选

```text
minio               仅用于本地对象存储 adapter 测试；不能证明 OSS/COS 签名完全等价
otel/metrics        本地观测验证
pgbackrest           PostgreSQL 备份工具
```

PostgreSQL `5432`、Redis `6379`、broker 管理端口和内部 metrics 不得暴露公网。公网只允许 443、MQTTS `8883`（若设备直连）、TURN 所需端口。

## 4. 阿里云资源清单

### 必需（国内 staging 第一版）

- [ ] ECS：承载 Docker Compose；固定公网 IP；安全组最小入站规则。
- [ ] 云盘：系统盘 + 独立数据盘；开启磁盘监控和快照策略。
- [ ] 域名/DNS：Gateway API origin、MQTT broker hostname、RTC/TURN hostname；域名归属和证书 owner 明确。
- [ ] HTTPS 证书：Gateway API、RTC signaling、TURN TLS（如启用）；证书轮换负责人明确。
- [ ] PostgreSQL：如果暂时与 ECS 同机，必须配置每日备份和恢复演练；迁移托管 RDS 时只替换 DSN，不改领域代码。
- [ ] Redis：同机仅用于 staging；nonce/限流数据不作为业务权威，重启恢复策略写入 runbook。
- [ ] MQTT broker：自管 EMQX/标准 broker 或已验证的国内托管 MQTT；必须确定 mTLS/ACL/设备身份方案。
- [ ] WebRTC：信令服务、STUN/TURN 公网地址和 UDP 可达性；必须完成板端协议 spike。
- [ ] 对象存储：OSS bucket（媒体、OTA 可分桶或不同 instance）；开启最小权限、生命周期、服务端加密和审计。
- [ ] 签名服务：国内 KMS/HSM 或等价受控 signer；不能把 Gateway staging 的非 development signer 继续指向 AWS KMS。
- [ ] 日志/监控：ECS、Docker、PostgreSQL、broker、signaling、TURN 的 CPU/RAM/磁盘/连接数/错误率告警。

### 候选/后置

- [ ] 托管 RDS PostgreSQL：当出现单机重启、磁盘增长或多人共享 staging 时启用。
- [ ] 托管 Redis：当需要故障切换或多 ECS 时启用。
- [ ] 国内消息队列：替换当前 SQS-specific ingest worker；Phase 2 前必须完成 adapter contract。
- [ ] OSS CDN/私网访问：只有媒体量和下载带宽证明需要时再加入，不在 Phase 0 预埋。
- [ ] WAF/SLB/ACK：单 ECS 试产验证不默认引入；出现明确容量/可用性证据再拆分。

### 不应作为第一版前置

- [ ] 多地域双活、Kubernetes、服务网格、跨云自动故障切换。
- [ ] 为了“云原生”引入新的业务注册中心或通用插件运行时。
- [ ] 把 MinIO 作为 OSS/COS 生产替代品。

## 5. DELL Ubuntu 局域网等价模拟

用户提供的 DELL 信息只用于模拟环境判断；账号、密码、真实凭据和固定 LAN 地址不得写入 Git、spec、镜像或日志。

### 等价条件

DELL 可模拟单 ECS 的条件：

1. Ubuntu 能运行 Docker Engine 和 Compose v2。
2. `nproc`、可用内存、可用磁盘达到本清单起步规格，或明确降低并发目标。
3. 局域网设备能访问 DELL 上的 Gateway HTTPS、MQTT TLS、signaling 和 TURN 端口。
4. DELL 的本地 CA/测试域名能被 App 与 feeder trust store 验证；不能把 LAN HTTP 当成 staging HTTPS。
5. 所有本地 secrets 放在 ignored、`0600` 文件或环境注入；不复制真实云凭据。

### 建议本地检查（只读）

```sh
nproc
free -h
df -h
docker version
docker compose version
timedatectl status
ss -lntup
```

### 本地与 ECS 的明确差异

| 项目 | DELL 局域网 | ECS 国内 staging |
|---|---|---|
| 地址 | RFC1918 LAN，可能变化 | 公网固定 IP + 域名 |
| HTTPS | 本地测试 CA/受控 hostname | 公网 CA/备案域名 |
| MQTT | LAN broker，验证协议/ACL | 公网 MQTTS，验证运营商链路 |
| TURN | 局域网/公网映射可能不同 | 必须验证 UDP 对称 NAT 和带宽 |
| 数据盘 | 本地单盘 | 云盘快照/备份；仍非自动 HA |
| 供应商签名 | 本地 fake signer 或测试 KMS adapter | 国内 KMS/HSM 真实权限和轮换 |
| OSS/COS | fake/minio 只能做接口测试 | 必须用真实 presign/checksum contract test |

本地 Compose 通过不等于阿里云网络、域名、备案、云盘、运营商链路或真实 WebRTC 已通过。

## 6. 网络与安全组基线

```text
443/TCP       Gateway API、RTC signaling、健康检查
8883/TCP      MQTTS（仅设备确需直连时开放）
3478/UDP+TCP  TURN/STUN
5349/TCP      TURN over TLS（启用时）
5432/6379     禁止公网；仅 Docker 内网或 localhost
```

- Gateway 不从服务端拨号设备私网地址。
- 云安全组、Docker network、反向代理和 broker ACL 需要分别限制，不把“同一 ECS”当作信任边界。
- 设备 trust anchor、Gateway CA、MQTT CA、TURN 证书分别管理并记录 SHA-256；不要为了方便合并成一个无界 CA bundle。
- 日志必须脱敏 endpoint credential、activation code、token、Wi-Fi 和 private key。

## 7. 备份、升级和回滚

- PostgreSQL：至少每日逻辑备份 + 定期恢复演练；升级前保留备份和 migration 版本。
- Docker：镜像使用 immutable digest；Compose 变更先 `config`、再本地启动、再滚动替换。
- Provider composition：签名/校验后原子切换；旧 composition 保留到新组合健康确认。
- 设备：manifest stage → verify → select → reboot → health → rollback；不得只修改环境变量。
- 单 ECS 故障时的 staging 目标是可恢复，不承诺零停机；真实试产前必须决定是否升级托管数据库/第二 ECS。

## 8. 资源清单验收门

Phase 0 只验收“清单完整、owner 明确、无秘密”；不得以清单替代资源创建。

进入 Phase 1 前必须有：

- [ ] DELL 只读硬件/软件检查输出。
- [ ] Compose 服务图、端口、卷、备份和 secret 目录。
- [ ] 国内域名/HTTPS/备案 owner 决策（可暂用本地受控 hostname，但要标记差异）。
- [ ] MQTT 身份方案（自有 CA 或托管产品）和 ACL 样例。
- [ ] WebRTC signaling/TURN 实施 owner 与板端 spike 入口。
- [ ] 国内 signer 方案；禁止沿用 AWS KMS 隐性依赖。
- [ ] OSS/COS bucket、生命周期、checksum 和最小权限方案。

## 9. 单 ECS 退出条件

满足任一条件就停止堆叠服务并拆分：

- PostgreSQL 备份恢复无法在试产窗口内完成。
- CPU、内存、磁盘 IO 或 TURN 带宽持续超过压测阈值。
- Gateway、数据库、broker、RTC 任一升级需要同时停掉全部能力。
- 需要两个以上环境共享同一数据面，或需要真实高可用。
- 需要审计证明数据库和密钥服务与业务容器分离。
