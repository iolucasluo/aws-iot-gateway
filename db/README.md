# Gateway database

`db/` 保存 Gateway 数据库资产。产品激活目录与设备 DP profile 相互独立，不提供 Product/DP 管理平台。

| 路径 | 职责 | 执行方式 |
|---|---|---|
| `migrations/` | schema、约束及既有数据搬迁 | API/worker 的 `ApplyMigrations`；不写入全新产品/DP 初始目录 |
| `seeds/002_dp_profiles.sql` | 各环境共用的正式 DP 初始化数据 | 显式 `aws-iot-gateway-db-seed` |
| `seeds/001_products.sql` | development/staging 演示产品 | seed 命令仅在非生产环境执行 |

生产 seed 命令只执行 DP seed，development/staging 额外执行演示产品。服务启动只跑 migrations，不自动 seed。
数据模型、激活字段、发布与回滚见 [DP profiles v1](../docs/contracts/dp-profiles-v1.md)。
