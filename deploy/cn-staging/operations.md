# CN 单机 staging 操作边界

本目录原名 `deploy/domestic-staging`。只迁移源码路径；已有服务器上的旧 release、
operator 根、Compose project/volume、`domestic-dev`、`CN_STAGING_*`、`DOMESTIC_*`、topic 均不改名。
本文描述人工受控步骤，不提供一键发布/自动回滚命令。

## 本地运行配置事实源

维护电脑的 Gateway `deploy/environments/aliyun-staging/` 是国内 ECS 的期望运行配置事实源，
不是临时构建目录；真实文件被 Git 和 Docker context 排除，不能 force-add、清理或嵌入镜像。
其 `config/` 保存 stack/gateway/signal env 与 MQTT ACL，`runtime/` 保存 composition、MQTT CA/证书
及 signer；服务器 `/opt/explorex-cn-cloud-staging/` 是部署副本。DELL 等其他环境须独立目录。

- 首次接管已有实例：固定 SSH 目标、project 和实际 mounts，从远端只读导入新目录；比对完整文件集
  及传输前后 SHA-256，远端变化或本地已有同名源时停止，不覆盖。不得重跑 prepare 生成另一套身份。
- 从此先改本地期望值，再在明确授权下部署；远端热修复先作为候选回收并审查差异，不双向自动同步。
  部署前比较远端与上次已部署指纹，漂移不明则停止，不用本地旧副本直接覆盖。
- `manifest.json` 记录首次导入文件集/hash；`deployment.json` 记录源主机、operator 根、实际镜像 ID、
  source revision、Compose release 路径及 mounts。它们属于本地受限证据，不是新的线上配置服务。
  按实际容器 labels/mounts 留存正在使用的 release Compose、镜像覆盖 env 和单文件挂载到本地
  `releases/` 历史基线，不能只复制 operator/config 而遗漏真实生效的 image.env。
  后续本地编辑不改写旧指纹为“已部署”；完成获授权的部署并核验后再记录新的已部署基线。
- 原样保留 env 中的 Linux 绝对路径，它们用于目标服务器，不应直接 source 后在 Mac 启动 Compose。
  新服务器路径/IP 改动先生成部署候选并审查端点、MQTT SAN、DNS 和 mounts，不能批量替换或轮换 CA。
- 文件目录权限分别为 0600/0700；部署材料留在本地受控目录，SSH 私钥/密码仍使用批准的认证来源。
  本地磁盘备份应另外加密保存到独立介质；Git ignored 不等于已有备份或加密。
- 配置不是业务数据备份。释放 ECS 前还需独立保存 PostgreSQL 一致性备份和版本/schema 信息，并验证
  恢复；核对 Redis 持久状态、MQTT 持久数据、Caddy 证书状态、镜像可获取性与 OSS 保留范围。
  未做数据库备份/恢复验证不能承诺保留绑定重建，也不能把本地配置留存报告为“可释放服务器”。

## 先判定操作类型

1. 确认用户本次授权的主机、Compose project、operator 根、当前 release 和允许修改的服务。
   检查 Docker context/host，不能误用本机 Docker 对远端路径执行命令。
2. 读取 [配置/profile 说明](README.md)、[schema 说明](../../db/migrations/README.md) 和
   [seed 说明](../../db/seeds/README.md)。cloud 用 base + single-node + cloud 三份；DELL 用前两份，
   不把旧 systemd 实例视为 Compose。
3. 在目标机只读核对运行镜像、资源、project、volume 和配置文件路径，不输出秘密。
4. 对比候选版本 schema/seed/配置。当前 `001_initial.sql` 仅支持新空库；同名 ledger 不代表同一 schema。
   旧库或无法证实兼容时停止，另行审查迁移或新空库，不能删除 ledger 重跑，也不能搭车重绑设备。
5. 日常更新保留 CA、签名密钥、App 凭据、数据库、Redis 和绑定；备份涉及私钥/业务数据，
   留在 owner 认可的受限路径，不下载到聚合根/Git。确认上个镜像仍可用，但不把旧镜像当作已验证 rollback。

## 旧库不兼容时的重建决策

这是更新前的决策清单，不是已验证的自动重置/迁移实现。数据库说明要求空库时，停止 image-only：

- 只读确认实际 `DATABASE_URL` 指向的库（不打印凭据）、关键表/列、migration ledger、有效绑定数量；
  同名 ledger 和 API healthy 都不证明候选 schema 兼容。
- 向用户明确旧记录不兼容搬迁、需要重新激活。优先建议保留旧库并另建具名空库，不删除旧库或卷；
  旧库仍在同一 ECS 不等于独立备份，切换前另行确认一致性备份、受限保存位置和恢复验证范围。
- 执行前取得具体新库或重置目标、停写/切库、seed、API 服务和设备重新激活范围的授权；
  “是不是重置一下”仅是方案询问，部署授权不自动包含数据丢弃。
- 先完成候选镜像门禁和备份前提，再停止旧 API 的写入/ingest；仅修改本地期望配置中已确认的
  数据库选择并部署副本，使用同一候选版本执行空库 migration + seed，核对 exit code 和产品/profile 后启动 API。
  同时检查 stack 的数据库默认值与 Gateway/seed URL，不能只改 `POSTGRES_DB` 就假定旧卷已切库。
- 保留 CA/signer/App secret、PG/Redis/MQTT/TURN/OSS；审查 signaling 兼容性，只变更必要且获授权的服务。
  换库不撤销旧 MQTT 证书，也不清除 Redis grant、设备运行身份和 App 缓存；旧 `X-Device-Token`
  在新空库没有对应记录，会被数据库鉴权拒绝。重新激活前须审查
  对应租约/授权失效及原设备身份处置，不能伪造绑定、只重启 App 或无差别 flush Redis。
- 旧库与旧镜像仅为候选恢复点：设备重新激活或外部状态变化后，不能承诺仅切回 URL 就恢复。
  失败保留现场，恢复按本次独立授权和兼容性证据执行。

## 首次初始化

只对新且明确批准的 operator 根调用 `scripts/prepare_cloud_staging_operator.sh`。
它生成随机秘密、staging CA、MQTT 证书、签名密钥和三个环境文件；拒绝覆盖不是允许在旧实例旁
新建另一套身份的理由。OSS 凭据由批准的秘密来源注入当前进程，不放 argv 或文档。
检查配置/profile/公开端点、文件权限与 MQTT SAN；确认新空数据库后按 README 的三文件命令启动。
`gateway-seed` 为一次性依赖，必须 exit 0，随后检查 API/signaling；首次启动可能创建 volumes、
签发 Caddy 证书并使用 OSS，均需在授权计划中列明。

## 已有实例 image-only 更新

在主机外准备固定源码和镜像：

- Gateway 工作树 clean、非 detached；核验精确 SHA 在预期发布分支可达，不自动 commit/push。
- 同一 SHA 先导出独立 gate source 跑测试，再导出全新 build source；两者都不含 ignored `.env`/runtime。
- 从 build source 的根 Dockerfile 构建 linux/amd64，绑定 revision label，使用唯一 release/tag。
- registry 方式记录不可变 digest；离线 docker save/load 方式记录 image ID、revision 和 tar SHA-256，
  上传前后核对 hash，并核对目标加载镜像 ID 与来源一致。二者的摘要语义不同。
- 新 release 目录不得覆盖已有目录。部署源码来自该 release 的 `deploy/cn-staging/`，
  不从 live checkout 或旧 release 混搭 Compose；远端旧源码目录保持不动。

以下是**目标 Docker 主机上**的 cloud 命令形状，不是预先授权。操作者先将四个非秘密变量设为
已经核验的绝对路径/镜像引用：`RELEASE_SOURCE`（新导出源码根）、`STACK_ENV`（原 stack.env）、
`IMAGE_ENV`（新 release 的只含 `CN_STAGING_GATEWAY_IMAGE` 的文件）、`EXPECTED_IMAGE`（已核验引用）。
环境文件用安全文件编辑器创建；拒绝 placeholders、latest、同名覆盖。不要输出渲染配置全文。

```bash
cn_compose() {
  docker compose --project-name explorex-cn-cloud-staging \
    --env-file "${STACK_ENV:?verified existing stack.env required}" \
    --env-file "${IMAGE_ENV:?verified image-only overlay required}" \
    -f "${RELEASE_SOURCE:?verified release source required}/deploy/cn-staging/compose.yaml" \
    -f "$RELEASE_SOURCE/deploy/cn-staging/compose.single-node.yaml" \
    -f "$RELEASE_SOURCE/deploy/cn-staging/compose.cloud-staging.yaml" \
    --profile single-node "$@"
}
cn_compose config --quiet
docker image inspect "${EXPECTED_IMAGE:?verified loaded image required}" \
  --format '{{.Id}} {{index .Config.Labels "org.opencontainers.image.revision"}}'
cn_compose ps
```

先在进程内核验渲染后的 `gateway-api`、`rtc-signaling` image 都等于 EXPECTED_IMAGE，
runtime mounts 仍为原 operator 路径，project/volumes 没变；不要把完整 JSON 输出到日志。
schema/seed/配置兼容且获得 image-only 更新授权后，逐条执行并检查退出码：

```bash
cn_compose up -d --no-deps --no-build --wait --wait-timeout 60 rtc-signaling
cn_compose up -d --no-deps --no-build --wait --wait-timeout 60 gateway-api
cn_compose ps
```

signaling 重启会断开观看，需重新进入视频；不能承诺零中断。只更新 API 时须有版本兼容证据，
涉及 signaling lease 协议的更新按 signaling → API → App 顺序。不要无选择 `up` 全栈、重跑 prepare，
不要启动国内不使用的 AWS event-worker、匿名 MQTT 或 MinIO。显式 `--no-deps` 不会执行 seed；
需要 schema/seed 变化则退出 image-only 流程，另行审查和授权。

平台面与三门户随同一 release 更新，但仍是**逐服务**执行，不整体 `up`：

- 需要加载四个镜像：`CN_STAGING_GATEWAY_IMAGE`（同时提供 `explorex-platform-api`）、
  `CN_STAGING_ADMIN_CONSOLE_IMAGE`、`CN_STAGING_DEVELOPER_PORTAL_IMAGE`、
  `CN_STAGING_MANUFACTURER_PORTAL_IMAGE`；记录每个 image ID 与 tar SHA-256。
- 首次启用平台面时先放置 `config/platform.env`（0600，与 `gateway.env` 共用同一个
  `PLATFORM_CREDENTIAL_KEK`）。KEK 一旦用于写入凭证就不可更换，否则既有项目凭证无法解密。
- 顺序：`gateway-api`（若同时更新）→ `explorex-platform-api` → 三个门户。
  平台面依赖 gateway 的 openapi 面。
- 平台面首次启动会应用 003–007 纯新增迁移。若操作范围不允许 schema 变化，必须先退出
  image-only 流程单独审查，不能靠 `--no-deps` 回避。
- 验收：`explorex-platform-api` 容器健康 + 门户容器健康 + 经门户 `/healthz` 200；
  业务验收按 `explorex-portals/docs/api-contract-v1.md` 的会话/项目/凭证链路。

DELL single-node 使用 project `explorex-cn-single-node` 和前两份 Compose，保持独立端口；
本地 build 与公网验证不是同一验收，不能套用 cloud 的 project 或路径。

## 验收与恢复

- 按实际镜像 ID/版本、容器健康、API/signaling 路由、必要的 seed exit code 分层验收。
- 业务验收需要明确目标设备：MQTT ACK、系统信息、WebRTC 渲染/清晰度/长时续期/退出、
  新 OSS 对象及授权下载。签 token、消费 nonce、设备 command 和 snapshot 都有副作用。
- 部署失败先只读诊断并保留现场，不自动回滚。旧镜像恢复需要 schema/配置兼容证据和本次授权。
  不 `down -v`、清库、删 ledger、flush Redis 或重建 CA；备份恢复也不属于 image-only。
- 实际执行记录归本次 owner 验收文档：目标、source、release、image/传输 hash、变更前后服务、
  结果和未验证项。单机不承诺 HA，真实跨网 TURN/OTA/量产长稳未验证时不报 production 放行。
