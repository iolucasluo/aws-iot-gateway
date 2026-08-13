# aws-iot-gateway

`aws-iot-gateway` 是 `aws-iot-demo-app` 的内部 IoT 中间层。它只处理 internal user subject、一次性激活、设备 operational identity 和固定设备操作边界；不实现 Developer、Project、Product、Factory、tenant 或 IDevID/产线初始化模型。

## 激活闭环

1. 已登录 App 用户创建一个绑定 QR、AP 或 BLE 方法的 256-bit 一次性 activation code。
2. App 通过对应本地配网通道把 activation id、code 和 Wi-Fi 凭据交给设备。
3. 设备联网后本地生成 operational key/CSR，向设备端 `prepare` 端点领取 AWS IoT certificate、Thing 和 ATS endpoint。
4. 设备使用 CSR 对应私钥签署 installation nonce；Gateway 验证证明后才激活 certificate 并标记 activation 为 `active`。

Gateway、App、日志和 Git 不接触设备 operational private key。没有 IDevID 时，BLE/AP 不具备出生身份认证或抵抗同链路主动 MITM 的能力；该 nonproduction demo 的信任边界是短时用户授权票、物理配网窗口和 operational-key installation proof。

## 本地运行

```sh
cp .env.example .env.local
# 将 .env.local 的变量作为运行时环境注入；不要提交任何 AWS 或 user token。
go run ./cmd/aws-iot-gateway
```

运行需要标准 AWS credential provider chain。Gateway IAM role 的最小权限应限于指定 prefix 的 `CreateCertificateFromCsr`、`CreateThing`、`AttachThingPrincipal`、`AttachPolicy`、`UpdateCertificate` 和 `DescribeEndpoint`。本仓库不创建或修改 AWS 资源；实际部署需使用独立、受控的 AWS 变更流程。

`GATEWAY_INTERNAL_TOKENS_JSON` 仅为非生产 demo 的 runtime 注入认证映射，不能放进镜像、Git 或移动 App。生产集成应由内部认证网关验证用户 token，并以同一 `Authenticator` 边界提供可信 subject。

## HTTP 契约

App 端点需要 `Authorization: Bearer <internal-token>`：

| Endpoint | 用途 |
|---|---|
| `POST /v1/activations` | 创建 method-bound activation；唯一一次返回 `activation_code` |
| `GET /v1/activations/{id}` | App 查询当前用户的状态；绝不返回 code |
| `POST /v1/device-activations/{id}/prepare` | 设备提交 `activation_code` 与 `csr_pem`，领取 install payload |
| `POST /v1/device-activations/{id}/confirm` | 设备提交 `activation_code` 与 installation proof，完成 certificate activation |
| `GET /healthz` | 存活检查 |

运行前检查：

```sh
go test ./...
go vet ./...
```
