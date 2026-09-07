# aws-iot-gateway PostgreSQL migrations

本目录是 Gateway schema 的唯一权威源。这里只保留用户 activation、全局设备绑定、运行身份、设备控制、
事件、媒体和 outbox。`products` 与独立的 DP profile/definition/member 是 Gateway schema 自有的只读激活/设备合同目录，不是旧 IoT 平台的 Product 域；没有
Developer、Project、Product CRUD/owner、Factory、IDevID、RMA、tenant 或 OTA campaign/platform 表。Gateway
只保留签名固件版本与单设备 deployment 执行事实。

## 文件与执行约定

- 原 `001-004` 的 pre-production schema 曾压成 `001_initial.sql`；当前最终 schema 由它与后续 up migrations
  共同描述。010 将已部署的产品 DP 目录迁移为设备直接引用的 profile。
- 后续变更使用连续的 `NNN_<name>.up.sql` / `NNN_<name>.down.sql`，不得再修改已发布 migration。
- 服务启动执行 `.sql` 与 `.up.sql`，按文件名排序并在同一 advisory-lock transaction 中登记到
  `gateway_schema_migrations`；`.down.sql` 永不自动执行。
- `001_initial.sql` 必须保持 Gateway 窄域业务边界，同时继承 explorex-api 的精确表/FK/索引命名、
  `updated_at` trigger、数据库硬约束与 schema contract test 规范。
- migration 通常只定义 schema，不写产品数据；development/staging 的 active PID 由
  `aws-iot-gateway-db-seed` 显式执行 `../seeds/001_products.sql` 初始化非生产产品。正式 DP 初始化数据由 `../seeds/002_dp_profiles.sql` 在各环境显式执行。唯一例外是 `002` 在识别到旧版
  pre-production `001_initial.sql` 时回填其历史上唯一合法的 `feeder-x1`，以便在不删除旧 activation/registry
  数据的前提下建立 PID 外键；fresh schema 不执行该数据回填。

## 当前 schema 快照

| 表 | 用途 | PK | 主要 FK / 删除语义 |
|---|---|---|---|
| `products` | 只读激活产品目录（PID、名称、状态、App 可消费的能力 ID） | `id` | 被 registry/activation 以 PID RESTRICT 引用 |
| `dp_profiles` | 版本化命令组合，active/retired/disabled | `id` | `code` 唯一，无产品 FK |
| `dp_definitions` | 共享 code/type/direction/spec | `id` | `(id, code)` 唯一供成员复合 FK 使用 |
| `dp_profile_members` | profile 成员及单组启停状态 | `id` | profile/definition RESTRICT；`(dp_profile_id, code)` 唯一 |
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

## 版本记录

- `001_initial`：原 `001-004` 的 pre-production squash。包含窄域 Internal AWS API、设备 MQTT
  heartbeat/LWT presence、运行证书撤销补偿 outbox、精确表/FK/索引命名、统一 `set_updated_at()`
  trigger，以及 `device_bindings.binding_external_user_id` 的 23514 不可变 trigger。
- `002_legacy_product_catalog`：只对已登记旧版 `001_initial.sql` 且缺少 `device_activations.pid` 的 schema
  生效；创建最小产品目录、回填旧 activation PID、移除 registry 的隐式默认 PID，并建立 registry/activation
  到 `products(pid)` 的 RESTRICT 外键。fresh schema 为 no-op；为避免丢失历史数据，不提供自动 down。
- `003_product_capabilities`：为已经应用旧 001/002 的产品目录补充只读 JSON capability ID 数组；fresh schema
  已由 001 直接包含同一字段与约束。
- `004_single_device_ota`：增加签名固件版本和单设备 deployment；不增加 campaign、调度器或 AWS IoT Jobs。
- `005_local_recording_projection`：新增 SD v1 metadata projection、binding-scoped opaque cursor 和 private thumbnail outbox；不上传录像主体。
- `006_local_thumbnail_status_text`：将 thumbnail outbox 的 `status` 从定长 `varchar(16)` 改为 `text`，继续由 CHECK 约束限定合法状态，避免状态名长度与字段宽度漂移。
- `007_product_dps`（历史结构，010 已替代）：增加 Gateway-owned `product_dps` 合同目录和按产品/code 唯一约束；通用 commands 在发布前按设备 PID
  加载 active DP 并校验存在性、状态、方向和 JSON 形状。产品/DP 仍只通过 reviewed seed/migration 维护。
- `008_provider_composition`：增加 provider-neutral `device_component_bindings` 绑定事实；每台设备每项 capability 只允许一个 active provider instance，external ref 保持 opaque，不引入 tenant/Project/Factory 域。

- `010_dp_profiles`：旧产品合同快照迁移为独立 profile，设备和 activation 直接引用；删除旧 product_dps，保留真实旧合同。
- 正式 DP 初始目录位于 `../seeds/002_dp_profiles.sql`，不属于 migration；发布前显式执行 seed。

设备和 activation 的 `dp_profile_id` 为 NOT NULL + RESTRICT FK；无产品到 profile 的关联。
010 schema 与 contract seeds 必须协调停止旧 API/worker 后发布，down 不自动执行且会拒绝无法无损表示为旧产品合同的状态。
详见 [DP profiles v1](../../docs/contracts/dp-profiles-v1.md)。
