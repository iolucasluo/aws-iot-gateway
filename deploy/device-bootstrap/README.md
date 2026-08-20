# Device Service Bootstrap Contract：Gateway producer 手册

这是 Gateway 对 Anyka、Taixin 和后续硬件交付的唯一公开 bootstrap 输入。它解决的是设备尚未拥有可信墙上时钟时，如何安全地知道：

- 应连接哪个 Gateway；
- HTTPS Gateway 信任哪个 CA；
- 哪个固定 HTTP time-bootstrap URL 可以发起首次时间请求；以及
- 哪一把（或轮换期间两把）P-256 公钥可验证返回的 COSE ES256 签名。

它不是服务器 `.env`，不是 QR/AP/BLE commissioning payload，也不是 Demo App HMAC 配置。Contract 中禁止存在私钥、AWS credentials、activation code、设备证书、App secret 或 device token。

完整格式在 [docs/contracts/device-service-bootstrap-v1.md](../../docs/contracts/device-service-bootstrap-v1.md)。

## 关键原则：Gateway 生成一次，硬件只复制

不要让 Taixin、Anyka、Dell build host 或每台设备重新从 AWS、KMS 或本地私钥推导配置。每个环境的 Gateway 部署拥有者在 Gateway 仓内生成一次公开 Contract，然后将**整个目录**复制给消费者。消费者只校验 SHA-256 和格式并嵌入/打包它。

```text
Gateway deployment owner
  └─ generates one public Contract
       ├─ taixin: TXW82X_SERVICE_BOOTSTRAP_CONTRACT=<copied directory>
       └─ anyka:  GATEWAY_DEVICE_BOOTSTRAP_CONTRACT=<copied directory>
```

当前尚未量产，`local` 和 `staging` 都是供多仓库硬件联调的**共享环境**。因此这两个环境的
Contract 是经过评审的环境输入，发布位置固定为
`deploy/device-bootstrap/contracts/<environment>/`；硬件只从该 Git 资产复制。个人临时 LAN 地址、个人私钥和临时候选输出不属于它们。环境尚未具备稳定 endpoint
和 signer 时，不创建伪造的 Contract；见 `deploy/local/README.md`。

生成脚本统一位于 `deploy/device-bootstrap/scripts/`，且只能写入调用者明确指定的、尚不存在的绝对
候选目录。发布者检查候选的 endpoint、CA、`kid` 和 fingerprint 后，**人工**替换对应环境目录并提交。
脚本不拥有 Git 工作树写入权限，也不会静默覆盖已经审核的 Contract。未来 production 改为受控 release
artifact，而不是把生产信任根提交到开发仓。

| 环境 | Gateway 生成命令 | 默认输出目录 | 签名私钥/Cloud authority |
| --- | --- | --- | --- |
| `development` | `deploy/device-bootstrap/scripts/render-local-contract.sh --out <new-dir>` | `deploy/device-bootstrap/contracts/development/`（Git 提交） | 仅共享 local 环境 owner 的 owner-only PEM |
| `staging` | `deploy/device-bootstrap/scripts/render-staging-contract.sh --out <new-dir>` | `deploy/device-bootstrap/contracts/staging/`（Git 提交） | 仅 Gateway release owner 的 AWS profile；只读取 CF Outputs 与 KMS 公钥 |
| `production` | 使用同一 CLI 的 `aws-release --environment production`，由正式生产发布流程封装 | 正式 release artifact store | 生产 release authority |

候选输出目录一旦存在，脚本会拒绝覆盖。这是为了避免在不知道谁正在使用旧 Contract 时静默替换信任根。需要新值时，发布者创建新的候选目录，验证后人工更新 Git 中的环境目录；不要让硬件 build 或生成脚本自动覆盖它。

## Development：本机/LAN Contract

development 可以使用 HTTP，因为 time response 仍受 pinned COSE ES256、nonce 和 chip UID 验证；但 HTTP 仅允许明确启用了 firmware development gate 的镜像。它不是 release shortcut。

### 一次性准备本地 signer

私钥只属于运行 Gateway 的开发者机器；它不复制到 Taixin、Anyka 或 Dell。

```sh
cd /Volumes/Expansion/Documents/taixin-multirepo-workspace/aws-iot-gateway
umask 077
mkdir -p .local/device-bootstrap/$USER
openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 \
  -out .local/device-bootstrap/$USER/time-bootstrap-private.pem
chmod 600 .local/device-bootstrap/$USER/time-bootstrap-private.pem
```

Gateway 开发环境必须同时配置：

```sh
GATEWAY_ENVIRONMENT=development
TIME_BOOTSTRAP_SIGNER=local
TIME_BOOTSTRAP_LOCAL_PRIVATE_KEY_PATH=.local/device-bootstrap/$USER/time-bootstrap-private.pem
```

然后给 Contract 指定**设备能够访问的** LAN 地址；`127.0.0.1` 只能用于同机测试，不能烧入硬件。

```sh
export DEVICE_SERVICE_BOOTSTRAP_SERVICE_URL=http://<shared-local-host>:14080
export DEVICE_SERVICE_BOOTSTRAP_TIME_URL=http://<shared-local-host>:14080/api/v1/device-time/bootstrap
export DEVICE_SERVICE_BOOTSTRAP_KEY_ID=time-local-shared-g1
export DEVICE_SERVICE_BOOTSTRAP_PRIVATE_KEY="$PWD/.local/device-bootstrap/$USER/time-bootstrap-private.pem"
./deploy/device-bootstrap/scripts/render-local-contract.sh \
  --out /tmp/device-bootstrap-contract-local-next
```

检查候选目录后，由发布者人工更新 `deploy/device-bootstrap/contracts/development/` 并与 Gateway 变更一同提交。这里的 host 必须是团队设备均可达的稳定地址；不要把某台 Mac 的临时 DHCP IP 提交进去。Contract 只含 public SPKI 和 endpoint；复制不泄露该 PEM private key。

## Staging：CloudFormation + KMS Contract

staging 的 CloudFormation stack 已输出 `APIBaseURL`、`TimeBootstrapURL`、`TimeBootstrapKeyArn` 和 `TimeBootstrapKeyID`。发布脚本只读取这些 Outputs 并调用 `kms:GetPublicKey`；不会调用 `kms:Sign`、不会读取 secret、不会传输 AWS credentials。

在 stack 已更新且 API/ECS 健康检查完成后，由获得 staging AWS authority 的 Gateway 发布者执行：

```sh
cd /Volumes/Expansion/Documents/taixin-multirepo-workspace/aws-iot-gateway
AWS_PROFILE=explorex-staging-hk \
AWS_REGION=ap-east-1 \
./deploy/device-bootstrap/scripts/render-staging-contract.sh \
  --out /tmp/device-bootstrap-contract-staging-next
```

检查候选目录后，由发布者人工更新 `deploy/device-bootstrap/contracts/staging/` 并提交。Taixin/Anyka/Dell 只复制该公开目录，绝不需要 AWS profile、CloudFormation access 或 KMS access。

当前 staging API Gateway execute endpoint 使用 `trust-roots/AmazonRootCA1.pem`。其 SHA-256 fingerprint 是 `8E:CD:E6:88:4F:3D:87:B1:12:5B:A3:1A:C3:FC:B1:3D:70:16:DE:7F:57:CC:90:4F:E1:CB:97:C6:AE:98:19:6E`。生产若改用自定义域名/CA，必须先提交并评审新的信任根资产和产物路径；禁止从临时网络连接捕获 CA。

## 硬件消费者的固定入口

| Consumer | 只接收的输入 | 生成后动作 | 明确不接收 |
| --- | --- | --- | --- |
| Taixin / TXW82x | Contract directory | renderer 生成 Git 忽略的 `explorex_service_bootstrap.generated.h`，嵌入 CA/keyring | 本地 signer private key、AWS credentials |
| Anyka / Linux | Contract directory | pack script 写入 immutable release env、CA 和每个 `<kid>.der` | KMS key、CloudFormation access |
| aws-iot-demo-app | 无 | 保持自己的 Gateway origin + HMAC 配置 | CA/SPKI bootstrap trust Contract |

Dell 是 Taixin 的受控 build host，不是第四个配置 producer。正式 build 必须显式指定已发布的
公开目录：

```sh
docs/coding-rules/skills/txw82x-firmware-flash/scripts/txw82x_remote_clean_package.sh \
  build-import --contract /absolute/path/to/device-service-bootstrap-contract
```

wrapper 在 Mac 与 Dell 双重校验 Contract，只在远端本次 build 内渲染 generated header，退出时
删除 checkout 中的生成文件；远端 job 保留公开 Contract 与 receipt，receipt 记录
`contract.json` SHA-256。不要在 Dell 预置 header，也不要传输 AWS profile、KMS authority 或
local signer PEM。

## Publisher test

Contract publisher 的静态边界只有一个测试入口：

```sh
sh deploy/device-bootstrap/tests/test_contract_publishers.sh
```

`deploy/local/test_local_compose_contract.sh` 仍只验证 local PostgreSQL/Redis compose；
`deploy/staging/test_*.sh` 仍只验证 staging stack、policy 与 smoke 行为，它们不是 Contract
publisher 的重复测试。

消费者的校验必须拒绝：未知/secret 字段、文件 SHA 不匹配、目录逃逸、非 P-256 SPKI、重复 `kid`、release 环境 HTTP，以及 development Contract 被用于 release build。

## 轮换与回滚

正常 Contract 只有一把 active key。轮换时可含两把：current + next。顺序固定为：

1. Gateway 发布者设置 current 以外的 `DEVICE_SERVICE_BOOTSTRAP_NEXT_KEY_ID`；local 同时设置公开 DER 路径 `DEVICE_SERVICE_BOOTSTRAP_NEXT_PUBLIC_KEY`，staging 同时设置新 KMS key id `DEVICE_SERVICE_BOOTSTRAP_NEXT_KMS_KEY_ID`，然后导出带 current + next 的 Contract。
2. 先发布能携带两把 key 的硬件镜像。
3. 切换 Gateway signer 的 active `kid`。
4. 等所有受支持镜像都含新 key 后，再导出/发布只保留新 key 的 Contract。

不能先切 signer；否则旧设备无法建立可信时间。若新 Contract 验证失败，保持旧输出目录和旧发布物，修复 Gateway 部署后生成新的目录；不要篡改已分发 Contract。
