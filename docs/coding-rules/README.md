# aws-iot-gateway AI 规则

本目录是 `aws-iot-gateway` 的项目规则源。开始修改前先读根目录 `README.md`，再按
[`rules/ai-项目规则索引.md`](rules/ai-项目规则索引.md) 加载命中的规则。

理解或修改现有业务状态流时，先读 [`../flows/目录说明.md`](../flows/目录说明.md) 及与目标场景对应的
单篇 flow；流程文档是对当前代码的导航，不替代本目录的契约与安全规则。

- `rules/Go开发规范.md`：包职责、错误处理和持久化边界。
- `rules/数据库设计规范.md`：窄域 schema、FK/索引/trigger 命名和 migration 管理门禁。
- `rules/HTTP与Activation契约.md`：HTTP 路由、认证和 activation 状态机。
- `rules/安全边界.md`：凭据、设备密钥、用户隔离和 AWS 权限边界。
- `rules/测试与验证.md`：Go 测试、静态检查和交付证据。

规则只描述本仓当前实现与已批准 Internal AWS API 边界。产品 DP payload 的 Gateway 权威目录位于
`db/migrations/007_product_dps.up.sql` 与 `db/seeds/002_product_dps.sql`；TXW82x 固件实现位于
`taixin-82x-workspace`，App 消费方位于 `aws-iot-demo-app/lib/aws_demo/`。设备 bootstrap 公共契约仍按部署文档管理，
不依赖 Anyka ExploreX 上线节奏。
