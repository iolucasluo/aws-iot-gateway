# aws-iot-gateway 项目规则索引

## 固定入口

任何改动先读：

- [仓库根 README](../../../README.md)
- 本索引

再按任务信号读取下表。规则文件相对本目录解析。

| 任务信号 | 必读规则 | 主要事实源 |
|---|---|---|
| Go 实现、重构、配置加载、PostgreSQL/Redis | `Go开发规范.md`、`测试与验证.md` | `cmd/`、`internal/`、`migrations/`、`.env.example` |
| HTTP 路由、JSON、状态码、activation 或设备目录 | `HTTP与Activation契约.md`、`测试与验证.md` | `internal/httpapi/`、`internal/activation/` |
| token、CSR、certificate、installation proof、用户隔离或 AWS IAM | `安全边界.md`、`HTTP与Activation契约.md` | `internal/activation/`、`internal/awsiot/` |
| App / 固件跨仓协议变化 | 全部四份规则 | App consumer、Anyka commissioning 契约 |

## 仓库边界

- 本仓拥有 internal app 鉴权、用户 activation/binding、AWS IoT operational identity、
  受限设备控制/事件/媒体 adapter 以及自有 dev/staging IaC。
- 移动端 QR/AP/BLE 交互归 `aws-iot-demo-app`；设备 payload parser、私钥生成、证书安装
  和 installation proof 归 `anyka-workspace`。
- 本仓不承载移动 UI、固件实现、环境密钥或聚合工作区协作文件。
- AWS 资源变更、部署和真实 credential smoke test 需要针对目标环境的明确授权；普通代码
  验证使用 fake client，不触碰 AWS。
