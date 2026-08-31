# 国内 `cn-staging` 本地/ECS 模拟

本目录是 Phase 1 的**基础设施等价模拟**，不是已经完成 provider 化的 Gateway 部署。当前 Gateway 二进制仍在启动时要求 AWS IoT/KVS/S3/KMS 配置，因此本阶段不会把它伪装成国内 ready；Gateway provider adapter 和国内 signaling 在后续 Phase 2/4 加入。

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

停止只影响本 Compose project：

```sh
docker compose --env-file .env.cn-staging.local \
  -f deploy/domestic-staging/compose.yaml down
```

本地 MQTT listener 使用 loopback 发布端口和匿名测试配置，只能验证容器网络和协议 harness，不能用于 feeder HIL 或云 staging。国内设备接入必须在后续阶段切换到 MQTTS/mTLS、ACL 和受控证书。

## DELL Ubuntu 等价运行

DELL 可以作为单 ECS 的低并发 staging 模拟机。将 `.env.cn-staging.local` 放在 DELL 上的仓库根目录；只有在受控协议 harness 中才设置 `CN_STAGING_EDGE_BIND_ADDRESS` 为 LAN bind address。当前 Mosquitto 是匿名本地测试配置，不能用于 feeder/App HIL；真实 HIL 必须等待 Phase 3 的 MQTTS/mTLS/ACL profile。不要把 DELL 账号、密码、固定 LAN 地址或真实云凭据写进本目录。

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
| Mosquitto | 国内 MQTT adapter 的本地 harness | 默认 loopback 的测试端口；匿名仅限本地 |
| coturn | TURN 进程/端口形状验证 | 默认 loopback；无公网 HIL 结论 |
| MinIO | 可选 ObjectStore 接口测试 | 仅 `object` profile、默认 loopback |
| Gateway API | Phase 1 不启动 | 等 Phase 2 消除 AWS 强制依赖 |
| RTC signaling | Phase 1 不伪造 | 等 Phase 4 实现并验收协议 |

## 运行约束

- Compose 文件不得出现 AWS/SQS/KVS、长期 key、token、证书私钥或生产 endpoint。
- `private` 是容器内部网络；`edge` 仅承载需要发布的 MQTT/TURN 端口；`host-access` 只挂 PostgreSQL/Redis 以提供 loopback 诊断。
- stateful volume 需要备份/恢复演练；单 ECS 仅承诺可恢复，不承诺 HA。
- `composition.cn-staging.example.yaml` 只描述 provider instance 形状，不是可部署 secrets 文件。
- 该目录的 contract test 只验证 Compose 形状和 fail-closed 边界，不替代真实云、真机或性能验收。
