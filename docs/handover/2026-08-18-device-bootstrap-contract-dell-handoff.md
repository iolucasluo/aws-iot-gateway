# Device Bootstrap Contract / Dell 构建交接（2026-08-18）

## 2026-08-18 审计快照

本节记录当日观测，不代表 Dell 的实时 checkout 状态。任何远端构建前都必须重新核对
Dell checkout 的 commit、clean 状态与下面列出的当前 Contract 事实源。

`Gateway Device Bootstrap Contract v1` 的唯一 producer 是 `aws-iot-gateway`。硬件
消费者只读取公开 Contract；它们不得读取 Gateway runtime `.env`、AWS profile、KMS
权限或 local signer private key。

- staging 已发布并提交：`deploy/device-bootstrap/contracts/staging/`。
- 2026-08-18 已以 Gateway `.env.cwj` 中的非敏感 profile/region 运行 staging exporter 的只读候选导出；候选
  与已提交的完整 Contract 相同。因此没有覆盖 `gateway-ca.pem`、`time-staging-g1.der` 或 `contract.json`，也没有
  发生 AWS resource mutation。
- local **尚未发布**：当前没有团队设备可达的稳定 Gateway hostname/reserved address，且
  Gateway 仓也没有已配置的 shared local signer。不能提交开发者笔记本 DHCP 地址或空
  Contract。
- 2026-08-18 观测到 Dell 选定的 firmware root 是旧 detached commit `ee34a9b47682ea594cd7e10d130a33ba1446f30e`；
  它没有 tracked loader、generated header 或 Contract，不能作为 Gateway 已接入构建的
  证据。
- 本文档只交接代码、文档和验证边界；**未执行** Dell clean build、Contract 传输、CKLink
  烧录、复位或串口验证。

## 实施目标

将 Dell 的正式构建输入从“远端机器上碰巧存在 Git 忽略 header”改为显式的公开 Contract：

```text
Gateway Git environment directory
  -> Mac local validation
  -> authenticated SSH single build job
  -> remote validation + generated header
  -> clean package + receipt with Contract SHA-256
```

正式入口必须从 **Taixin 聚合仓**运行，因为 `txw82x_remote_clean_package.sh` 属于该仓；Gateway 聚合仓
不包含此 build-host skill。传入的 Contract 则是已 checkout 的 Gateway public directory：

```bash
cd /Volumes/Expansion/Documents/taixin-multirepo-workspace
docs/coding-rules/skills/txw82x-firmware-flash/scripts/txw82x_remote_clean_package.sh \
  build-import --contract /absolute/path/to/aws-iot-gateway/deploy/device-bootstrap/contracts/staging
```

`--contract` 必须是公开 Contract 目录，不接受 JSON 单文件、header、私钥或 AWS
credentials。脚本在本机和 Dell 都通过固件 renderer 校验 Contract；Dell 在本次 job 内生成
`project/cert_provision/explorex_gateway_release_config.generated.h`，构建结束时删除该
checkout 中的生成文件。公开 Contract 和 build log 保留在 job 目录用于审计，receipt 记录
`contract.json` 的 SHA-256。`contract.json` 已包含 CA 与每个 SPKI 的 SHA-256，因此其摘要
绑定了完整的已校验 Contract。

## 已知 Dell/R730 构建路径（2026-08-18）

本次由操作者提供的 Dell firmware root 是：

```text
/mnt/taixin-build-benchmark/taixin-clean-build-20260806-e0134d0/taixin-82x-workspace/TXW82x_FPV-v2.7.0.7
```

成功的受控构建应从该 firmware root 的 canonical output 读取 APP，而不是根目录临时 bin：

```text
/mnt/taixin-build-benchmark/taixin-clean-build-20260806-e0134d0/taixin-82x-workspace/TXW82x_FPV-v2.7.0.7/project/output/APP.bin
```

路径存在不等于该 checkout 已可作为证据。远端 build 前仍必须重新确认该 root 的 commit、clean 状态、
preflight、输入 Contract digest 和生成的 build receipt；本次没有执行 SSH build、读取该远端 APP、烧录或串口验证。

## 实施切片

- [x] 收敛 Gateway 两份重复的 Contract publisher 静态测试到
  `deploy/device-bootstrap/tests/`；保留 `deploy/local` 的 compose 验证和
  `deploy/staging` 的基础设施 smoke/策略测试。
- [x] 为 Dell wrapper 添加 `--contract` 必填参数、仅公开 regular-file Contract 的安全传输、
  本地/远端双重 renderer 验证和 checkout generated header cleanup。
- [x] 将 Contract SHA-256 写入 `build-receipt.json`，并用固件单元测试拒绝非 SHA-256 输入。
- [x] 更新 Gateway Contract/部署文档与 TXW82x build-host skill，删除“Dell 预置 ignored
  header”的过时描述。
- [x] 运行 Gateway、firmware 和 skill 对应的回归测试；本次不运行 Dell build。

## 不变量与未决输入

- local Contract 的前置输入是共享 Gateway endpoint 与 owner-only P-256 signer；没有二者
  不生成或提交伪造 Contract。
- staging Contract 只能用于明确选择 staging 的构建，不能改名为 local。
- Dell 可以接收公开 Contract，但不能得到 AWS profile、KMS authority、Gateway private key、
  device credential、activation code、Wi-Fi password 或 token。
- 正式远端构建仍要求本机与 Dell checkout 为同一 clean commit；具体远端状态以本次重新
  核对结果为准，必须由用户另行授权 build，并先将 Dell checkout 同步到该 commit。
- 烧录、擦除、复位和运行验证都是独立授权，本文档不构成硬件操作授权。

## 验证记录

- Gateway：`go test -race -count=1 ./...`、`go vet ./...`、
  `sh deploy/device-bootstrap/tests/test_contract_publishers.sh`、local compose 与全部
  `deploy/staging/test_*.sh` 静态契约检查通过。
- Taixin：`python3 -m unittest discover -s tools/tests -p 'test_*.py'` 共 332 项通过；
  receipt 与 Gateway Contract renderer 的目标单元测试均通过。
- Shared build skill：`test_remote_clean_package.py` 8 项、
  `test_macos_direct_flash_skill_contract.py` 9 项通过；两个 shell wrapper 均通过
  `bash -n`。
- `Dell build: NOT_RUN`；没有 SSH Contract 传输、Docker build、canonical package 导入、
  CKLink、烧录、复位或串口验证。
