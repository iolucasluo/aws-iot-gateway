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

规则只描述本仓当前实现与已批准 Internal AWS API 边界。跨仓 payload 的设备端权威契约位于
`anyka-av-workspace/docs/gateway-commissioning-v1.md`；App 消费方位于
`aws-iot-demo-app/lib/aws_demo/`。
