# CN Cloud Staging Deployment Spec v1

## 目标

在一台中国大陆 ECS 上部署独立的 `cn-cloud-staging`，完成 `feeder-x1` 与 Demo App 的真实公网闭环：

- Gateway API、ExploreX signaling、MQTT mTLS、TURN、PostgreSQL 和 Redis 运行在同一 Compose project；
- `media_object` 通过 provider composition 绑定到阿里云 OSS；
- App 可激活、列出、控制设备并读取系统信息；
- App 可观看真实 WebRTC，媒体仍由设备和 App 的 PeerConnection 传输，signaling 只转发 SDP/ICE；
- 快照对象由设备通过 OSS 预签名 URL 上传，Gateway 转码并返回可下载 URL；
- AWS IoT/KVS/S3 组合和 DELL single-node 环境不被替换或修改。

本环境是验证国内最小云端拓扑的 nonproduction staging。当前允许 HTTP/WS 和软件 CA/signer，不能据此提升 production ship verdict。

## 第一性原理边界

1. 一台 ECS 是进程放置策略，不是能力耦合：每个能力仍由 composition 的独立 binding 选择。
2. 公网入口只负责按 Host 转发 API 与 signaling；它不代理 WebRTC 媒体。
3. MQTT 使用独立 mTLS listener；设备 operational private key 仍只在设备生成和保存。
4. OSS 凭据只注入 Gateway，App 和设备只接触短期预签名 URL。
5. PostgreSQL/Redis 不开放公网；DELL 的 project、端口、volume 和证书保持原样，作为回滚基线。

## 最小拓扑

| 能力 | 实例 | 外部入口 | 组合 binding |
|---|---|---|---|
| API | `gateway-api` | `api-staging.livpet.top:80` | n/a |
| Realtime signaling | `rtc-signaling` | `rtc-staging.livpet.top:80` WebSocket | `realtime.local_signal` |
| Messaging | `mqtt-mtls` | `mqtt-staging.livpet.top:8883` | `messaging.local_mqtt` |
| ICE relay | `turn` | `turn-staging.livpet.top:3478` + UDP relay `49360-49400` | signaling ICE config |
| Media object | 阿里云 OSS | `oss-cn-shenzhen` 内网 endpoint（Gateway）/预签名公网 URL（设备与 App） | `object.aliyun_oss` |
| Identity | 软件 CA（staging） | 仅 Gateway 内部 | `identity.local_ca` |
| Signed time | 软件 P-256 signer（staging） | API 窄路由 | `signer.local_file` |
| State | PostgreSQL + Redis | Docker internal/host loopback | n/a |

公网 Host 路由由单个 edge proxy 处理，因此无需为 API 和 signaling 再开放任意高端口。MQTT 与 TURN 使用各自协议端口，不穿过 HTTP proxy。

## Provider composition

`media_object` 必须显式绑定 `aliyun_oss` 实例。OSS region、endpoint 和 bucket 是部署配置；AccessKey 只来自进程环境。缺少 binding、bucket、region、endpoint 或 credential 时 Gateway 启动失败，不回退到 MinIO、磁盘或 AWS S3。

AWS 运行时继续使用既有 `awss3` adapter；国内运行时只根据当前 composition 装配 `media_object`。不存在全局 `CLOUD_PROVIDER` 开关，也不存在“阿里云替代 AWS”的隐式映射。

## 可观察成功标准

1. ECS Compose 中 PostgreSQL、Redis、MQTT、TURN、Gateway、signaling、edge proxy 全部健康；seed 退出码为 0。
2. `api-staging.livpet.top/healthz` 和 `rtc-staging.livpet.top/healthz` 返回 200；Gateway 容器没有 AWS data-plane 配置。
3. MQTT 匿名连接失败，Gateway mTLS smoke 成功；feeder 首激活后在线并可完成至少一个有 ACK 的控制命令。
4. App 的环境 ID 为 `cn-cloud-staging`，PID 为 `feeder-x1`；设备列表、详情和系统信息均来自 ECS。
5. WebRTC 至少一次进入 connected 并持续渲染视频；ICE 证据能区分 direct 与 relay，TURN 分配日志或 candidate pair 证明 relay 路径可用时记为 PASS。
6. App 发起快照后，OSS 出现限定前缀对象；Gateway 能读回、生成 JPEG，并向 App 返回可访问 URL。测试对象只位于 `development/snapshots/` 或专用 smoke 前缀。
7. DELL 既有健康端点仍返回 200；AWS provider 回归测试保持通过。

## 失败与回滚

- ECS 部署失败：只停止 `explorex-cn-cloud-staging` project，不删除 volume；DELL 不受影响。
- feeder 切换失败：清除本轮未完成 activation 状态并恢复 DELL runtime profile；不改写 FACTORY 分区。
- OSS 失败：媒体 API fail closed；设备控制、MQTT 和 WebRTC 不回退到其他 ObjectStore。
- TURN 失败：保留 direct P2P 证据但 WebRTC 公网容灾项标记失败，不把直连成功等同于 TURN 成功。
- 任何真实 secret、私钥和设备凭据均不得进入 Git、镜像 layer、Compose 渲染输出或验收日志。

## 分阶段验收

1. `12.1` spec + RED/GREEN：OSS adapter、composition binding、国内媒体装配和 cloud Compose contract。
2. `12.2` ECS server：独立 project、DNS Host 路由、MQTT/TURN/OSS smoke。
3. `12.3` App/feeder：云 profile、BLE 激活、MQTT/控制/系统信息。
4. `12.4` WebRTC/OSS：真实视频、ICE/TURN、快照对象与 App 展示。
5. `12.5` 回归/审计：AWS 与 DELL 不回归、提交和证据固化。
