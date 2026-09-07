# DP profiles v1

Gateway 将设备命令合同与产品目录解耦。`products` 仍负责 active PID、名称和 typed service capabilities；
通用 commands/schedules 从设备登记的 `dp_profile_id` 加载合同，不查询 PID，不存在 product→profile 关系。

## 数据与所有权

- `dp_profiles`：UUID 主键、稳定版本 code（如 `feeder-v1`）、名称、`active|retired|disabled`。
- `dp_definitions`：可复用的 `code/type/direction/spec`；相同 code 的不同约束必须使用不同 definition ID。
- `dp_profile_members`：profile 与 definition 的关联；每个 profile 的 code 唯一，复合 FK 保证成员 code 与 definition 一致。
  `status=disabled` 只禁用该 profile 的成员，不影响复用该 definition 的其他 profile。
- `device_activations.dp_profile_id`、`device_registry.dp_profile_id`：NOT NULL + RESTRICT FK；每台设备一个明确版本。
- 正式合同由 reviewed contract seed 显式发布。已有版本的成员和 definition 内容不能原地改写；协议变化新增 profile
  版本和有差异的 definition，完整合同相同则复用旧 definition。允许受控调整 profile/member 启停状态。
  当前没有运行时 CRUD、用户自行修改 profile 或 OTA 自动切换 profile 的入口。

Gateway 拥有 HTTP 选择和参数验证；固件 owner 负责实现相应协议。profile 是 backend 对设备实现的受控声明，
不能证明 MCU 厂商、真实硬件能力或出生身份。不同 MCU 厂家可以共用合同，但须统一字段语义、范围、单位、方向及 ACK 行为；
只同名但范围不同的 DP 不共享定义。当前校验器只校验下行输入，ACK 继续由各服务状态机校验。

## HTTP

`GET /api/v1/openapi/dp-profiles` 使用现有 HMAC v1，独立于产品目录，不接受 PID 作为筛选或授权依据：

```json
{"profiles":[{"code":"feeder-v1","name":"Feeder v1"},{"code":"camera-v1","name":"Camera v1"},{"code":"no-commands-v1","name":"No commands v1"}]}
```

只返回 active profile，按 code 排序。示例展示字段形状，不承诺示例顺序。`no-commands-v1` 明确不开放通用命令；
它不代表台灯固件已实现任何开关/亮度功能。新增台灯 profile 必须先核对实际协议，不能按产品名猜测 DP。

创建 activation 必须包含 `dp_profile`；字段由内部 backend 选择并纳入 HMAC body 和 activation 幂等摘要：

```json
{"method":"qr","pid":"lamp-x1","dp_profile":"feeder-v1","binding_external_user_id":"backend-derived-user","idempotency_key":"caller-key"}
```

这是跨 PID 选择的结构示例，不表示实际 Lamp 固件兼容 feeder-v1。Gateway 验证 active PID 和 active profile，
不验证二者组合；集成方承担真实固件兼容确认。Demo App 分别显示产品与协议选择，不按 PID 默认选择 profile。
QR/AP/BLE 和目标环境迁移后的 BLE activation 都传递所选值。

- `dp_profile` 缺失、超过 96 字符或不满足 `^[a-z0-9][a-z0-9.-]*-v[1-9][0-9]*$`：`400 invalid_request`。
- 不存在、retired 或 disabled：`400 dp_profile_unavailable`。
- 同一幂等 key 改 profile：`409 idempotency_conflict`；相同请求重试：`409 activation_in_progress`。
- 创建/轮询响应回显 `dp_profile`；App 创建时核对返回 PID/profile 与选择相同，发现替换立即拒绝。
- prepare 在数据库事务内重新检查 profile 状态，再创建带相同 profile 的设备；禁用后不会创建新设备。
- 设备 prepare/confirm 请求、安装回执、QR/AP/BLE 本地 payload 和 installation proof 格式不变；设备不能自行换组。
- commands/schedules 请求不能覆盖 profile；缺失设备或不可用 profile fail closed，未知/disabled/只读 DP 和非法参数在
  command 落库与 MQTT 发布前拒绝。`retired` 允许已有设备继续使用，但目录不向新 activation 提供它。
- 超时、MQTT ACK、重试、binding/device-token 授权沿用原合同；typed local media/OTA/recommission 仍走专用路径。

## 发布、迁移与回滚

`010_dp_profiles` 将旧库真实 `product_dps` 去重为 definition，并为每个旧产品创建 retired legacy 快照
（原产品 disabled 时快照也 disabled）。仅迁移这一次按旧 PID 为已存在的 activation/设备赋值，包括无 DP 的空合同。
快照 code 为 `legacy-<原产品 UUID>-v1`，运行期没有 PID fallback。旧 `product_dps` 表随后删除。

`db/seeds/002_dp_profiles.sql` 在所有环境显式初始化源代码审阅过的 `feeder-v1`（25 项）、`camera-v1`（28 项）和
`no-commands-v1`（0 项），复用完全相同的 definition。它不创建设备、产品、binding、凭据或样例身份。
旧设备继续使用实际数据库快照，避免被当前源码合同覆盖；已有但未完成的 activation 可以继续使用 retired 快照安装。
migration 不填入新的正式 DP 目录；必须显式执行 seed 命令。production 只执行 DP seed，
development/staging 额外执行演示产品 seed；产品目录依然由受控操作者发布。与产品 seed 一样，重复执行通过 UPSERT
将记录更新为文件声明的值（包括状态和 spec）。新协议版本仍应使用新的 profile/definition，避免影响已有设备。

这是需要协调的版本切换：先停止旧 API/worker，再使用新版本执行 schema migration 和显式 contract seed，随后启动新服务，配套更新内部 backend/App 后恢复流量。
不能混跑依赖旧 product_dps 的二进制；旧 backend 缺少 dp_profile 的创建请求会拒绝。固件 wire 协议不变。

`010_dp_profiles.down.sql` 仅支持能无损还原为旧产品关系的 legacy 快照；已采用新 profile 时拒绝回滚。
执行前停止服务，schema down 与删除对应 migration ledger 记录放在同一事务中。

## 安全与验证边界

- 主体：HMAC backend 选择 profile；activation code 仅授权现有安装流程，profile 不扩展为硬件身份。
- 通道：选择包含在已有签名/nonce 防重放中；commands 不接受额外 profile 字段；固件无法通过准备请求覆盖选择。
- 状态：PostgreSQL 事务复制 activation profile；幂等请求包含 profile；解绑/重配不自动升级合同；新 activation 重新明确选择。
- 当前集成测试覆盖空库、旧库真实数据迁移、同合同去重/异合同分离、同 profile 跨 PID、禁用、幂等、登记、回滚拒绝与回滚往返。
- 本任务只运行隔离本地 PostgreSQL、Go 和 Flutter 测试；AWS、线上发布及不同 MCU 台灯的真机兼容未执行。
