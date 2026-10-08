# DP profiles v1

Gateway 将设备命令合同与产品目录解耦。`products` 仍负责 active PID、名称和 typed service capabilities；
`products.dp_profile_code` 指定新 activation 的默认合同，多个产品可共享同一 profile。
通用 commands/schedules 仍从设备登记的 `dp_profile_id` 快照加载合同，产品默认值变化不修改已有设备。

## 数据与所有权

- `dp_profiles`：UUID 主键、稳定版本 code（如 `feeder-v1`）、名称、`active|retired|disabled`。
- `dp_definitions`：可复用的 `code/type/direction/spec`；相同 code 的不同约束必须使用不同 definition ID。
- `products.dp_profile_code`：可空的 code 外键；未配置时禁止新 activation，不为旧产品猜测合同。
- `dp_profile_members.dp_profile_code`：通过稳定 code 外键关联 profile；每个 profile 的 code 唯一，复合 FK 保证成员 code 与 definition 一致。
  `status=disabled` 只禁用该 profile 的成员，不影响复用该 definition 的其他 profile。
- `device_activations.dp_profile_id`、`device_registry.dp_profile_id`：NOT NULL + RESTRICT FK；每台设备一个明确版本。
- 正式合同由 reviewed contract seed 显式发布。已有版本的成员和 definition 内容不能原地改写；协议变化新增 profile
  版本和有差异的 definition，完整合同相同则复用旧 definition。允许受控调整 profile/member 启停状态。
  当前没有运行时 CRUD、用户自行修改 profile 或 OTA 自动切换 profile 的入口。

Gateway 拥有产品与协议映射、HTTP 选择和参数验证；固件 owner 负责实现相应协议。profile 是 backend 对设备实现的受控声明，
不能证明 MCU 厂商、真实硬件能力或出生身份。不同 MCU 厂家可以共用合同，但须统一字段语义、范围、单位、方向及 ACK 行为；
只同名但范围不同的 DP 不共享定义。当前校验器只校验下行输入，ACK 继续由各服务状态机校验。

## HTTP

`GET /api/v1/openapi/dp-profiles` 使用现有 HMAC v1，独立于产品目录，不接受 PID 作为筛选或授权依据：

```json
{"profiles":[{"code":"camera-v1","name":"Taixin Minion Cam X1 v1"},{"code":"feeder-v1","name":"Anyka Feeder X1 v1"},{"code":"lamp-v1","name":"Anyka Lamp X1 v1"}]}
```

只返回 active profile，按 code 排序。初始化目录包含喂食器、台灯和小黄人三个经过源码核对的组合。

创建 activation 只提交 PID，Gateway 根据受控产品目录解析 profile；App 不选择、不提交 `dp_profile`：

```json
{"method":"qr","pid":"lamp-x1","binding_external_user_id":"backend-derived-user","idempotency_key":"caller-key"}
```

服务端读取 `products.dp_profile_code`，检查 profile 为 active，并在 INSERT 时锁定产品/profile、再次核对映射和状态。
绑定页只选择产品；目标环境迁移后重新读取目标产品目录，创建时仍由目标 Gateway 解析协议。

- 请求包含 `dp_profile`（包括正确值、空值或 null）：`400 invalid_request`，不静默忽略旧字段。
- 产品未配置 profile，或 profile retired/disabled：`400 dp_profile_unavailable`。
- 同一幂等 key 改 PID/method/user：`409 idempotency_conflict`；相同请求重试：`409 activation_in_progress`。
  幂等摘要不再含服务端默认 profile；比较持久化的原请求字段。code 仍只在首次创建时返回。
- 创建/轮询响应返回服务端确定的 `dp_profile`；App 创建时校验 PID 一致、profile code 合法。
- 改产品默认值只影响新 activation；已发放 activation 和设备保留原 profile，重试不重新选择。
- prepare 在数据库事务内重新检查 profile 状态，再创建带相同 profile 的设备；禁用后不会创建新设备。
- 设备 prepare/confirm 请求、安装回执、QR/AP/BLE 本地 payload 和 installation proof 格式不变；设备不能自行换组。
- commands/schedules 请求不能覆盖 profile；缺失设备或不可用 profile fail closed，未知/disabled/只读 DP 和非法参数在
  command 落库与 MQTT 发布前拒绝。`retired` 允许已有设备继续使用，但目录不向新 activation 提供它。
- 超时、MQTT ACK、重试、binding/device-token 授权沿用原合同；typed local media/OTA/recommission 仍走专用路径。

## 初始化与开发基线

当前未上线、未量产，schema 已统一为 `db/migrations/001_initial.sql`，只支持空数据库初始化。
旧开发数据库须明确选择后重建，不保留旧 product_dps、历史数据搬迁或 down 兼容路径。
具体重建边界见 [migrations README](../../db/migrations/README.md)。

`db/seeds/002_dp_profiles.sql` 显式初始化 `feeder-v1`（37 个命令及 2 个喂食事件）、`lamp-v1`（28 个命令）、
`camera-v1`（28 个命令），复用完全相同的 definition，并设置三个已存在产品的默认协议。
本次 v1 数据作为当前开发基线，不额外为未上线历史版本保留兼容目录。
migration 只建结构；seed 命令在事务中写数据。production 只执行 DP seed，development/staging 额外执行演示产品 seed。
重复执行 UPSERT 会按文件更新状态和 spec；UUID 引用不是成员/spec 的历史副本。
未来正式发布后，协议变更使用新 profile/definition 版本，产品默认值变化只影响新 activation。

App/backend 需使用 PID-only 创建请求；携带 dp_profile 会被拒绝。设备 QR/AP/BLE wire 格式不变。

## 安全与验证边界

- 主体：HMAC backend 提交 PID，Gateway 按受控产品映射选择 profile；activation code 仅授权现有安装流程，profile 不扩展为硬件身份。
- 通道：PID 包含在已有签名/nonce 防重放中；commands 不接受额外 profile 字段；固件无法通过准备请求覆盖选择。
- 状态：PostgreSQL 事务复制 activation profile；幂等请求只绑定原始调用字段；解绑/重配不自动升级合同；新 activation 使用产品当前默认协议。
- 当前集成测试覆盖空库初始化及幂等、共享 definition、同 profile 跨 PID、禁用、幂等、登记和产品默认切换。
- 本任务只运行隔离本地 PostgreSQL、Go 和 Flutter 测试；AWS、线上发布及不同 MCU 台灯的真机兼容未执行。

## 三款产品的初始化合同

| 产品 PID / 设备实现 | profile code | 目录内容 |
|---|---|---|
| Anyka 喂食器 `feeder-x1` | `feeder-v1` | 37 个注册命令、feed_done/feed_error 两个上行事件 |
| Anyka 台灯 `lamp-x1` | `lamp-v1` | 28 个注册命令，复用一致的 Anyka 定义 |
| Taixin 小黄人 `minion-cam-x1` | `camera-v1` | 28 个公开 DP，保留既有 code |

台灯没有喂食电机、喂食计划/日志、提示音更新，
不加入 feed、clear_jam、set_schedule、get_schedule、get_feed_log、audio_update、audio_reset。
Anyka 的 led 是指示灯，没有亮度或色温参数；pet_det 是 motion_track 的旧别名，不是宠物分类。 新的 `pet_detection_v1` 是 feeder 独立 PET 模型 get/set 命令，旧固件对其返回不支持；App 必须以设备 ACK 能力判定展示。

目录覆盖注册命令，不把遥测字段、诊断命令和旧文档中的 sd_playback 当作 Anyka DP。
sd_recordings_v1、sd_thumbnail_v1、local_media、ota_install 记录为 disabled 普通命令成员，
仍由现有专用 API 执行；factory_reset/recommission/reset_wifi 继续由生命周期门禁阻止普通命令调用。
上行 event 成员不能作为下行命令。查询动作 query_status/status/device_info 则仍是请求/ACK。

Anyka 保留原有 record_start.mode/duration_s、reboot.delay_ms、snapshot.format 兼容字段，
但 spec 明确标记为固件不读取；不宣称定时录像或可配置重启延迟。Taixin record_mode 仅支持
60 秒分段、85/75 水位和至少 131072 KiB 保留空间，定时录像 duration_s 必须大于零。
复杂路径/URL、计划 cron、LED state-or-blink 等语义仍由固件和专用服务校验；spec 说明字段不等于 Gateway 已实现对应验证器。

核对源码快照（2026-09-07；未构建、上板或验收）：

- Anyka `f48dc45f1b35fa2f2e1b5b783a114e48850c4050`：`ExplorexAV130_FW/feeder/firmware/app/unified_handlers.c` 注册表及 handler、
  `ExplorexAV130_FW/lamp/firmware/app/unified_handlers.c`、两款 app Makefile 与 `lamp/README.md`。
- Taixin `1926ee9c134a8aca2b388e7a79ebdb6b11464165`：`TXW82x_FPV-v2.7.0.7/project/product/product_cmd.c` 的 identify_dp_action/handler，
  `project/storage/recorder.c`、`project/storage/recorder.h`；工程位于同级 `taixin-multirepo-workspace/taixin-82x-workspace`。
