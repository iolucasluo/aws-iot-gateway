# aws-iot-gateway 项目规则索引

## 固定入口

任何改动先读：

- [仓库根 README](../../../README.md)
- 本索引

再按任务信号读取下表。规则文件相对本目录解析。

| 任务信号 | 必读规则 | 主要事实源 |
|---|---|---|
| Go 实现、重构、配置加载、Redis | `Go开发规范.md`、`测试与验证.md` | `cmd/`、`internal/`、`.env.example` |
| PostgreSQL、repository SQL、表/FK/索引/trigger 或 migration | `数据库设计规范.md`、`Go开发规范.md`、`测试与验证.md` | `db/migrations/`、`internal/platform/postgres/` |
| HTTP 路由、JSON、状态码、activation 或设备目录 | `HTTP与Activation契约.md`、`测试与验证.md` | `internal/httpapi/`、`internal/activation/` |
| token、CSR、certificate、installation proof、用户隔离或 AWS IAM | `安全边界.md`、`HTTP与Activation契约.md` | `internal/activation/`、`internal/awsiot/` |
| 既有 activation、binding、reset、MQTT、事件或媒体流程 | [`../../flows/目录说明.md`](../../flows/目录说明.md) 与对应单篇 flow；再读本表命中的契约规则 | `internal/`、`db/migrations/`、`cmd/event-worker/` |
| App / 固件跨仓协议变化 | 全部五份规则、[`../../contracts/device-service-bootstrap-v1.md`](../../contracts/device-service-bootstrap-v1.md) | App consumer、Anyka commissioning 契约、`deploy/device-bootstrap/README.md` |

## 仓库边界

- 本仓拥有 internal app 鉴权、用户 activation/binding、AWS IoT operational identity、
  受限设备控制/事件/媒体 adapter 以及自有 dev/staging IaC。
- 移动端 QR/AP/BLE 交互归 `aws-iot-demo-app`；设备 payload parser、私钥生成、证书安装
  和 installation proof 归 `anyka-av-workspace`。
- 本仓不承载移动 UI、固件实现、环境密钥或聚合工作区协作文件。
- Gateway Device Bootstrap Contract 由本仓部署者按环境一次生成、硬件仓只复制消费；其输出仅含公开 endpoint、CA 和 SPKI，私钥/AWS authority 不得离开 Gateway 部署边界。
- AWS 资源变更、部署和真实 credential smoke test 需要针对目标环境的明确授权；普通代码
  验证使用 fake client，不触碰 AWS。
