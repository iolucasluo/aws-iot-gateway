# Gateway development seeds

本目录只放 dev/staging 环境**显式执行**的初始化数据 SQL；API 和 event worker 不会扫描或自动执行这些文件。

## 当前 seeds

| 编号 | 文件 | 说明 |
|---|---|---|
| 001 | `001_products.sql` | active 产品 PID：`feeder-x1`、`minion-cam-x1` |

先完成 migration，再由获授权的非生产数据库操作者显式执行：

```sh
psql "$DATABASE_URL" -X -v ON_ERROR_STOP=1 -f db/seeds/001_products.sql
```

该 seed 只初始化 Gateway 的只读 activation 产品目录，不创建设备实例。`device_registry` 仍必须由 activation 流程创建，并与 AWS IoT runtime identity 一致；本目录不得伪造 certificate、binding 或 token。

后续确有稳定的非生产初始化前提时，新增 `NNN_<name>.sql`，并满足：

- 先完成 `db/migrations/` 的 schema migration；
- 仅使用脱敏、非生产数据，且重复执行保持幂等；
- 明确写出适用环境、执行顺序和所需数据库角色；
- 不以 seed 绕过 activation、binding 或 AWS IoT 身份生命周期。
