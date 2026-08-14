# Go 开发规范

## 包职责

| 路径 | 职责 |
|---|---|
| `cmd/api/` / 迁移期 `cmd/aws-iot-gateway/` | 读取配置、装配依赖、启动 HTTP server；不放领域逻辑 |
| `cmd/worker/` | SQS 事件摄取、Webhook/异步任务 worker；不放领域逻辑 |
| `internal/runtime/` | 运行时配置解析和 fail-closed 校验 |
| `internal/httpapi/` | 路由、认证适配、请求解码、响应和错误映射 |
| `internal/modules/` | activation、binding、device、event、media 等 bounded context |
| `internal/platform/postgres/` | PostgreSQL adapter 与 migration runner |
| `internal/platform/redis/` | nonce/限流等非权威协调状态 |
| `internal/awsiot/` | AWS IoT SDK adapter；不反向包含 HTTP 或 App 语义 |

新增行为放到拥有不变量的包中。`cmd` 只做 composition；HTTP handler 不直接调用 AWS SDK；
AWS adapter 不决定用户授权或 activation 状态。

## 实现约束

- 使用 `context.Context` 贯穿外部调用和持久化，不在领域层创建不可取消的后台任务。
- 对外部错误增加动作上下文并保留错误链；稳定的领域错误用 `errors.Is` 判别，不解析错误字符串。
- 依赖接口放在消费方附近并保持最小；测试使用 fake，不为未来场景提前扩展接口。
- 共享状态必须保持当前互斥与持久化原子性。持锁路径中的新增 I/O 要先评估阻塞、重试和部分失败语义。
- PostgreSQL 是业务权威存储；不得新增 JSON 文件或 Redis 业务状态。activation code/device token 只存 hash。
- 配置必须显式校验。缺少 PostgreSQL、Redis、AWS region、device policy 或 internal app map 时启动失败；
  不增加静默默认 credential、账号或远端 endpoint。
- application 不依赖 HTTP/AWS/PostgreSQL driver；保持 Go 格式，不引入 tenant/Project/Product/Factory 平台抽象。

## 变更同步

- 配置 key 变化：同步 `.env.example`、`internal/runtime/config.go`、配置测试和 README；真实值只进 Secret provider。
- HTTP JSON 或状态机变化：同步 handler、activation service、两侧测试以及 App/Anyka 消费方。
- AWS adapter 变化：同步最小 `Client` 接口、fake 测试和 IAM 权限说明。
