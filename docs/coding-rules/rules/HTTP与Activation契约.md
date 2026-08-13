# HTTP 与 Activation 契约

## 端口与调用方

Gateway 默认监听 `:14080`，由 `GATEWAY_LISTEN_ADDRESS` 覆盖。该端口服务 App 与设备的
Gateway HTTP 请求，不是设备临时 AP 的本地 endpoint。App 的 AP handoff 固定访问设备侧
`http://192.168.4.1:8080/v1/gateway-commissioning`；两者不得共用 base URL、端口或客户端。

## 路由

| Endpoint | 调用方 | 认证 | 契约 |
|---|---|---|---|
| `GET /healthz` | 运行检查 | 无 | 只证明进程存活 |
| `POST /v1/activations` | App | Bearer internal user token | 创建 `qr`、`ap` 或 `ble` activation；code 仅本响应返回 |
| `GET /v1/activations/{id}` | App | Bearer internal user token | 只读当前用户的 activation metadata，不返回 code |
| `GET /v1/devices` | App | Bearer internal user token | 只返回当前用户已 active 的受限设备投影 |
| `POST /v1/device-activations/{id}/prepare` | 设备 | activation id + code | 提交本地 CSR，取得 inactive certificate、endpoint 和 nonce |
| `POST /v1/device-activations/{id}/confirm` | 设备 | activation id + code + proof | 验证本地私钥安装证明后激活 certificate |

App token 只用于 App 路由；设备 prepare/confirm 不接受 App token 代替 activation code。App
也不替设备调用 prepare/confirm。

## 状态机不变量

```text
created -> installing -> active
```

- create 生成 16-byte 随机 id、32-byte 随机 code，固定五分钟有效，并绑定 internal user subject
  与单一 local method。
- store 只保存 code 的 SHA-256；任何读取或持久化 projection 都清空明文 code。
- prepare 对同一 CSR 可重试并返回同一 installation；同一 activation 换 CSR 必须拒绝。
- AWS certificate 由 prepare 以 inactive 状态创建。confirm 必须验证 CSR 公钥对固定 domain message
  的签名，验证成功后才激活 certificate 并进入 `active`。
- 用户查询不能跨 subject 泄露 activation；设备目录只包含 `id`、`activation_id` 和
  `aws_iot_endpoint`，不得加入 certificate、proof、code 或私钥材料。

## HTTP 防线

- JSON body 上限保持 32 KiB，拒绝未知字段和格式错误。
- 领域错误继续稳定映射为 `400/403/404/410/500`；不要把内部 AWS 或解析错误直接返回给调用方。
- 新路径必须精确匹配 method 和 path segment，避免宽泛 prefix 路由吞掉未知 endpoint。
- 更改字段、状态、错误码或重试语义前，同时核对 App consumer 与
  `anyka-workspace/docs/gateway-commissioning-v1.md`。
