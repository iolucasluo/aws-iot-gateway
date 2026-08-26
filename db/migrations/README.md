# aws-iot-gateway PostgreSQL migrations

本目录是 Gateway schema 的唯一权威源。这里只保留用户 activation、全局设备绑定、运行身份、设备控制、
事件、媒体和 outbox。`products` 是 Gateway schema 自有的只读激活目录，不是旧 IoT 平台的 Product 域；没有
Developer、Project、Product CRUD/owner、Factory、IDevID、RMA、tenant 或平台 OTA 表。

## 文件与执行约定

- 当前尚未上线、没有量产数据，原 `001-004` 已压成唯一的 `001_initial.sql`；它直接描述最终 schema，
  不包含旧宽泛表名、`ALTER TABLE` 过渡步骤或兼容债务。
- 后续变更使用连续的 `NNN_<name>.up.sql` / `NNN_<name>.down.sql`，不得再修改已发布 migration。
- 服务启动执行 `.sql` 与 `.up.sql`，按文件名排序并在同一 advisory-lock transaction 中登记到
  `gateway_schema_migrations`；`.down.sql` 永不自动执行。
- `001_initial.sql` 必须保持 Gateway 窄域业务边界，同时继承 explorex-api 的精确表/FK/索引命名、
  `updated_at` trigger、数据库硬约束与 schema contract test 规范。
- migration 通常只定义 schema，不写产品数据；development/staging 的 active PID 由
  `aws-iot-gateway-db-seed` 显式执行 `../seeds/001_products.sql` 初始化。唯一例外是 `002` 在识别到旧版
  pre-production `001_initial.sql` 时回填其历史上唯一合法的 `feeder-x1`，以便在不删除旧 activation/registry
  数据的前提下建立 PID 外键；fresh schema 不执行该数据回填。

## 当前 schema 快照（17 张业务表）

| 表 | 用途 | PK | 主要 FK / 删除语义 |
|---|---|---|---|
| `products` | 只读激活产品目录（PID、名称、状态、App 可消费的能力 ID） | `id` | 被 registry/activation 以 PID RESTRICT 引用 |
| `device_registry` | 设备运行权威记录、ThingName、证书与 presence | `id` | — |
| `device_activations` | internal app 用户激活 saga | `id` | `device_registry_id → device_registry`（nullable, RESTRICT） |
| `device_activation_sessions` | activation 安装 session | `id` | `device_activation_id → device_activations`（CASCADE） |
| `device_bindings` | 全局唯一设备归属；用户字段含不可变 trigger | `id` | `device_registry_id → device_registry`（RESTRICT） |
| `device_tokens` | binding-scoped token hash | `id` | `device_binding_id → device_bindings`（RESTRICT） |
| `device_commands` | MQTT command/ACK 状态 | `id` | `device_registry_id → device_registry`（RESTRICT） |
| `device_events` | append-only 设备事件 | `id` | `device_registry_id → device_registry`（RESTRICT） |
| `device_schedules` | 每设备一份已确认 schedule 投影 | `device_registry_id` | `device_registry_id → device_registry`（RESTRICT） |
| `device_streams` | KVS stream grant 生命周期 | `id` | registry + binding（RESTRICT） |
| `device_snapshots` | snapshot 对象状态 | `id` | registry + binding（RESTRICT） |
| `device_recordings` | cloud/local recording 状态 | `id` | registry + binding（RESTRICT） |
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
