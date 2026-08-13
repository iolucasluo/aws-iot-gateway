# Go 开发规范

## 包职责

| 路径 | 职责 |
|---|---|
| `cmd/aws-iot-gateway/` | 读取配置、装配依赖、启动 HTTP server；不放领域逻辑 |
| `internal/runtime/` | 运行时配置解析和 fail-closed 校验 |
| `internal/httpapi/` | 路由、认证适配、请求解码、响应和错误映射 |
| `internal/activation/` | activation 状态机、installation proof、用户设备投影和状态存储 |
| `internal/awsiot/` | AWS IoT SDK adapter；不反向包含 HTTP 或 App 语义 |

新增行为放到拥有不变量的包中。`cmd` 只做 composition；HTTP handler 不直接调用 AWS SDK；
AWS adapter 不决定用户授权或 activation 状态。

## 实现约束

- 使用 `context.Context` 贯穿外部调用和持久化，不在领域层创建不可取消的后台任务。
- 对外部错误增加动作上下文并保留错误链；稳定的领域错误用 `errors.Is` 判别，不解析错误字符串。
- 依赖接口放在消费方附近并保持最小；测试使用 fake，不为未来场景提前扩展接口。
- 共享状态必须保持当前互斥与持久化原子性。持锁路径中的新增 I/O 要先评估阻塞、重试和部分失败语义。
- 文件状态继续使用同目录临时文件、`0600` 权限、`Sync`、关闭后原子替换；不得把 activation code
  明文写入持久化记录。
- 配置必须显式校验。缺少 AWS region、Thing prefix、device policy 或内部 token map 时启动失败；
  不增加静默默认 credential、账号或远端 endpoint。
- 保持 Go 格式和现有命名风格；不要引入与当前窄域 Gateway 无关的框架或平台抽象。

## 变更同步

- 配置 key 变化：同步 `.env.example`、`internal/runtime/config.go`、配置测试和 README。
- HTTP JSON 或状态机变化：同步 handler、activation service、两侧测试以及 App/Anyka 消费方。
- AWS adapter 变化：同步最小 `Client` 接口、fake 测试和 IAM 权限说明。
