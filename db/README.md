# Gateway database

`db/` 集中存放 Gateway 的数据库资产；`products` 激活目录和 `product_dps` 产品 DP 合同目录是 Gateway 的产品事实，
不扩展为 Product/DP CRUD、Project、Factory 或其他平台域。

| 目录 | 职责 | 执行方式 |
|---|---|---|
| `migrations/` | PostgreSQL schema、约束和 schema contract test | 编译进 API/worker；服务启动时由 `ApplyMigrations` 执行 |
| `seeds/` | 仅 development/staging 的显式初始化数据 | `aws-iot-gateway-db-seed` 一次性执行；服务启动不自动执行 |

显式 seed 命令会先幂等完成 schema migration，再按文件名顺序在单个事务中执行全部 seed；空环境或
`production` 会被拒绝。不得在 seed 中放入真实 AWS 凭据、设备私钥、activation code 或生产数据。
