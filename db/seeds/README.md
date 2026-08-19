# Gateway development seeds

本目录只放 dev/staging 环境**显式执行**的初始化数据 SQL；API 和 event worker 不会扫描或自动执行这些文件。

当前 Gateway 没有可安全预置的业务数据：`device_registry` 必须由 activation 流程创建，并与 AWS IoT runtime identity 一致。因此本目录暂不提供伪造设备、certificate、binding 或 token 的 seed。

后续确有稳定的非生产初始化前提时，新增 `NNN_<name>.sql`，并满足：

- 先完成 `db/migrations/` 的 schema migration；
- 仅使用脱敏、非生产数据，且重复执行保持幂等；
- 明确写出适用环境、执行顺序和所需数据库角色；
- 不以 seed 绕过 activation、binding 或 AWS IoT 身份生命周期。
