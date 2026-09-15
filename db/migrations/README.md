# aws-iot-gateway PostgreSQL migrations

本目录是 Gateway schema 的唯一权威源，包含两部分：**设备域**（用户 activation、全局设备绑定、运行身份、设备控制、
事件、媒体和 outbox）与 `003` 起的**平台面**（门户账号与会话、`manufacturers`/`projects`/`project_products`、
项目级 `api_credentials`/`project_webhooks`、产线工位与质量证据）。`products` 与独立的 DP profile/definition/member
是 Gateway schema 自有的受控激活/设备合同目录；DP 定义只由 reviewed contract seed 发布，门户不提供 DP CRUD。
仍然有意的非目标：IDevID、RMA、tenant 隔离维度、OTA campaign/platform，以及第二套设备域
（不新建 `devices`/`bindings`/`device_tokens`/`device_certificates`）。Gateway 只保留签名固件版本与单设备 deployment 执行事实。

## 文件与执行约定

- `001_initial.sql` 是基线：直接声明最终表、外键、索引、函数和触发器，没有历史升级、回填、down 或示例数据。
- `002_device_identity.sql` 是首个顺序增量迁移。设备身份归并所需的 `device_registry.chip_uid` /
  `sn_source`、`device_activations.requested_device_sn` / `requested_chip_uid` 与
  `uniq_device_registry_chip_uid` 曾一度直接写进 `001_initial.sql`，但 runner 按**文件名**登记迁移，
  已部署库登记过 `001_initial.sql` 就永远不会重跑，这些列因此缺失并让 activation 在
  `column chip_uid does not exist` 处失败。故以增量补齐，不改写 001 的历史。
- 增量迁移必须保持纯 additive 且可重复执行（`ADD COLUMN IF NOT EXISTS`、`CREATE INDEX IF NOT EXISTS`、
  命名约束用 `DO` 块按 `pg_constraint` 条件补），不得删除或改写既有结构。
- PostgreSQL runner 保留事务、advisory lock 和 migration ledger，重复启动不会重复建表或重复应用。
- 产品与 DP 数据单独由 `../seeds/001_products.sql`、`../seeds/002_dp_profiles.sql` 显式初始化。
- 后续真正需要增量升级时继续添加顺序 migration；不提前保留当前已删除的兼容路径。

## 本地数据库重建

- 已登记过 `001_initial.sql` 的旧库靠顺序迁移补齐，不需要重建：runner 会跳过已登记文件并按序应用
  `002` 及以后的文件；`002` 对既有行保留原值，`sn_source` 取历史正确的默认 `generated`。
- 比 `001_initial.sql` 更早、结构差异更大的历史库仍只支持空库。旧开发库可能已登记同名
  `001_initial.sql` 而结构不同，启动不会自动覆盖或补齐。
  停止对应 API/worker，明确确认目标开发数据库后重建空库，再执行 owning child 的 seed 入口并启动服务。
  也可以为该环境配置全新的空数据库。不得仅删除 migration ledger 后在旧业务表上重跑。
- 本仓不自动 DROP 现有数据库，不操作其他开发者的共享 PostgreSQL/Redis；现有设备测试记录不做兼容搬迁。

## 当前 schema 快照

| 表 | 用途 | PK | 主要 FK / 删除语义 |
|---|---|---|---|
| `products` | 只读激活产品目录（PID、名称、状态、App 可消费的能力 ID） | `id` | dp_profile_code → dp_profiles(code) RESTRICT；被 registry/activation 以 PID RESTRICT 引用 |
| `dp_profiles` | 版本化命令组合，active/retired/disabled | `id` | `code` 唯一，无产品 FK |
| `dp_definitions` | 共享 code/type/direction/spec | `id` | `(id, code)` 唯一供成员复合 FK 使用 |
| `dp_profile_members` | profile 成员及单组启停状态 | `id` | profile code/definition ID RESTRICT；`(dp_profile_code, code)` 唯一 |
| `device_registry` | 设备运行权威记录、ThingName、证书与 presence | `id` | `dp_profile_id → dp_profiles`（NOT NULL, RESTRICT）；PID → products |
| `device_activations` | internal app 用户激活 saga | `id` | `device_registry_id → device_registry`（nullable, RESTRICT）；`dp_profile_id → dp_profiles`（NOT NULL, RESTRICT） |
| `device_activation_sessions` | activation 安装 session | `id` | `device_activation_id → device_activations`（CASCADE） |
| `device_bindings` | 全局唯一设备归属；用户字段含不可变 trigger | `id` | `device_registry_id → device_registry`（RESTRICT） |
| `device_component_bindings` | 设备 capability 到 provider instance 的当前绑定事实 | `id` | registry + binding（RESTRICT）；每 capability 一个 active |
| `device_tokens` | binding-scoped token hash | `id` | `device_binding_id → device_bindings`（RESTRICT） |
| `device_commands` | MQTT command/ACK 状态 | `id` | `device_registry_id → device_registry`（RESTRICT） |
| `device_events` | append-only 设备事件 | `id` | `device_registry_id → device_registry`（RESTRICT） |
| `ota_firmware_versions` | 下游发布的签名 app OTA 版本；不保存签名私钥 | `id` | `pid → products`（RESTRICT） |
| `device_ota_deployments` | 单设备 OTA 执行记录与重启确认结果 | `id` | registry + firmware version（RESTRICT） |
| `device_schedules` | 每设备一份已确认 schedule 投影 | `device_registry_id` | `device_registry_id → device_registry`（RESTRICT） |
| `device_streams` | KVS stream grant 生命周期 | `id` | registry + binding（RESTRICT） |
| `device_snapshots` | snapshot 对象状态 | `id` | registry + binding（RESTRICT） |
| `device_recordings` | cloud/local recording 状态 | `id` | registry + binding（RESTRICT） |
| `device_local_recordings` | SD v1 metadata/thumbnail projection | `id` | registry + source event（RESTRICT） |
| `device_local_recording_tombstones` | 防止乱序 finalized 复活已删录像 | `id` | registry + source event（RESTRICT） |
| `local_recording_cursors` | binding-scoped opaque pagination cursor | `token` | binding + registry（CASCADE） |
| `local_recording_reconciliations` | 每设备 catalog 对账游标/代次 | `device_registry_id` | registry（CASCADE） |
| `local_recording_reconciliation_sightings` | 完整扫描期间看到的 recording_id | composite | registry（CASCADE） |
| `local_recording_thumbnail_outbox` | private thumbnail upload/reconcile intent | `id` | local recording（CASCADE） |
| `webhook_outbox` | durable webhook delivery intent | `id` | `device_event_id → device_events`（CASCADE） |
| `webhook_deliveries` | append-only webhook attempt | `id` | `webhook_outbox_id → webhook_outbox`（CASCADE） |
| `device_lifecycle_outbox` | 运行证书回收补偿任务 | `id` | `device_registry_id → device_registry`（RESTRICT） |
| `idempotency_records` | append-only internal operation 幂等事实 | `id` | — |
| `audit_logs` | append-only Internal API 操作审计 | `id` | —（保留源对象删除后的审计） |

`device_schedules` 使用自然主键是有意例外：业务不变量就是“一台设备最多一行当前投影”，新增 UUID
只会产生无消费方的第二身份。`device_events`、`webhook_deliveries`、`idempotency_records` 和
`audit_logs` 是 append-only 事实，不需要 `updated_at`。

## 当前基线

`001_initial.sql` 完整包含 activation/binding、产品与 DP profiles、OTA 环境切换、provider bindings、
本地录像投影和 thumbnail outbox。所有状态约束、唯一索引、RESTRICT/CASCADE 外键与更新时间触发器保留。
`local_recording_thumbnail_outbox.status` 直接使用 text，无历史 varchar 扩列步骤。

profile code 外键是明确命名例外：稳定版本 code 唯一，直接引用使 seeds 可读；表主键仍保留 UUID。
设备/activation 保存 profile 引用，后续修改产品默认值不切换已有设备。
初始化流程见 [seeds](../seeds/README.md)，DP 合同见 [DP profiles v1](../../docs/contracts/dp-profiles-v1.md)。
