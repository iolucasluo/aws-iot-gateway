# Gateway development seeds

本目录只放 development/staging 环境**显式执行**的初始化数据 SQL；API 和 event worker 不会扫描或自动执行这些文件。

## 当前 seeds

| 编号 | 文件 | 说明 |
|---|---|---|
| 001 | `001_products.sql` | active 产品 PID：`feeder-x1`、`minion-cam-x1` |

由获授权的非生产数据库操作者显式执行仓库命令；命令会先幂等完成 migration，再按文件名顺序在单个事务中
执行所有 seed：

```sh
GATEWAY_ENVIRONMENT=development DATABASE_URL='postgres://...' \
  go run ./cmd/aws-iot-gateway-db-seed
```

容器镜像中的等价命令是 `/usr/local/bin/aws-iot-gateway-db-seed`，供 staging 一次性 ECS task 使用；完整命令见
[`deploy/staging/README.md`](../../deploy/staging/README.md)。执行器只接受 `development` 或 `staging`，空环境和
`production` 均硬拒绝。不要把它改成 API/worker 启动时自动执行。

该 seed 只初始化 Gateway 的只读 activation 产品目录，不创建设备实例。`device_registry` 仍必须由 activation 流程创建，并与 AWS IoT runtime identity 一致；本目录不得伪造 certificate、binding 或 token。

后续确有稳定的非生产初始化前提时，新增 `NNN_<name>.sql`，并满足：

- 可依赖显式 seed 命令先幂等完成 `db/migrations/` 的 schema migration；
- 仅使用脱敏、非生产数据，且重复执行保持幂等；
- 明确写出适用环境、执行顺序和所需数据库角色；
- 不以 seed 绕过 activation、binding 或 AWS IoT 身份生命周期。
