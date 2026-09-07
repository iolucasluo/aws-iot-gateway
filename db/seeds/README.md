# Gateway seeds

seed 是显式初始化数据，不等于示例数据。服务启动不自动执行 seed。

| 文件 | 内容 | 环境 |
|---|---|---|
| `001_products.sql` | 演示产品目录 | development / staging |
| `002_dp_profiles.sql` | DP profile、共享 DP 定义和组合关系 | development / staging / production |

两个文件都使用 `INSERT ... VALUES ... ON CONFLICT ... DO UPDATE`：重复执行会按文件更新已有记录，
包括状态和 DP spec。相同合同直接引用同一个 definition ID，不在执行时计算去重。

```sh
# DATABASE_URL 由目标环境提供。
GATEWAY_ENVIRONMENT=production go run ./cmd/aws-iot-gateway-db-seed
```

命令先执行 migrations，再在一个事务中执行 seed。production 只执行 `002_dp_profiles.sql`，
development/staging 按编号执行两个文件；空环境和未知环境不执行。
生产产品使用单独审核的数据；seed 不保存设备实例或真实凭据。
