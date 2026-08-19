# Gateway database

`db/` 集中存放 Gateway 的数据库资产；只读 `products` 激活目录是唯一产品事实，不扩展为 Product CRUD、Project、Factory 或其他平台域。

| 目录 | 职责 | 执行方式 |
|---|---|---|
| `migrations/` | PostgreSQL schema、约束和 schema contract test | 编译进 API/worker；服务启动时由 `ApplyMigrations` 执行 |
| `seeds/` | 仅 dev/staging 的显式初始化数据 | 不会被服务启动自动执行 |

先完成 schema migration，才可以按需执行适用于目标非生产环境的 seed。不得在 seed 中放入真实 AWS 凭据、设备私钥、activation code 或生产数据。
