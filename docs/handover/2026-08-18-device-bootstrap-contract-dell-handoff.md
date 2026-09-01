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
- 上述旧 commit 是初次审计时的历史状态；同日后续的受控同步、Dell build 与 Mac 4 KiB
  烧录证据见“实际执行记录”。

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

路径存在不等于该 checkout 已可作为证据。未来每次远端 build 前仍必须重新确认该 root 的 commit、clean 状态、
preflight、输入 Contract digest 和生成的 build receipt；本次实际执行结果见下节。

## 实际执行记录（2026-08-18）

- Dell checkout 经受控 Git 同步后为 detached、clean 的
  `0fc3a1ead0836fea492638677e1928feff603d96`。Dell 上有可用的自定义命名 GitLab key，
  但默认 SSH config/agent 不会选择它；本次仅以临时 `-i` 完成同步，未复制、打印或提交密钥。
  下次需要 Dell 自主 `git fetch` 时，应由 owner 配置已审批的只读 deploy key 或 SSH agent。
- 首次 build 在 vendor 完整性门禁停止：生成 build view 的
  `lib/bluetooth/uble/ble_demo.c` 存在未声明内容漂移。经用户本轮明确授权，使用
  `bootstrap_firmware_assets.py --profile source --materialize --reset-third-party-views
  --confirm-delete-third-party-views` 重建两个 source build view；未删除 Git checkout、locked ZIP、
  `.objects` 原厂事实或工程内旧 third-party path。随后
  `check_vendor_overlays.py` 输出 `VENDOR_OVERLAYS_READY`，Dell checkout 仍 clean。
- 受控 Dell job `20260818T131030Z-0fc3a1ead083-35e6d2f8` 使用公开 staging Contract，
  `contract.json` SHA-256 为
  `2938d8cf557850334afadd5d49f99d8fbfed39c0496ffc6336c43c8f1d959afc`。它完成 clean package、
  bundle、receipt 与 Mac canonical import：APP 为 1,840,144 B，SHA-256 为
  `84d696310a0b76e12ce0fe543b349397f379453bada8d92fe3d215eb4658bcbc`；receipt 位于
  Taixin firmware 的
  `project/output/remote-build-receipts/20260818T131030Z-0fc3a1ead083-35e6d2f8/build-receipt.json`。
- Mac 已执行保留持久化数据的 4 KiB 双周期烧录：`erase_end=0x1C2000`、
  `persistent_boundary=0x1E8000`、450 sectors。CKLink 为 normal mode `0x32bf:0xb210`，目标识别为
  `CK804FG`，请求 1600 kHz、官方实际 2526.316 kHz。两轮各完成 450/450 sector erase 与
  57/57 target-side chunk verify，随后 marker cleanup、APP 尾部复验与最终 reset 均通过，终态为
  `TXW82X_CURRENT_APP_4K_RANGE_EXPERIMENT_PASS`。
- 未执行 CH340 串口启动采集、Gateway HTTPS/time-bootstrap/activation/MQTT 联调或 Demo App
  端到端验证；不得将本次 build/烧录成功描述为设备业务已运行。

## 实施切片

- [x] 收敛 Gateway 两份重复的 Contract publisher 静态测试到
  `deploy/device-bootstrap/tests/`；保留 `deploy/local` 的 compose 验证和
  `deploy/staging` 的基础设施 smoke/策略测试。
- [x] 为 Dell wrapper 添加 `--contract` 必填参数、仅公开 regular-file Contract 的安全传输、
  本地/远端双重 renderer 验证和 checkout generated header cleanup。
- [x] 将 Contract SHA-256 写入 `build-receipt.json`，并用固件单元测试拒绝非 SHA-256 输入。
- [x] 更新 Gateway Contract/部署文档与 TXW82x build-host skill，删除“Dell 预置 ignored
  header”的过时描述。
- [x] 运行 Gateway、firmware 和 skill 对应的回归测试，并完成受 staging Contract 约束的 Dell
  clean build、receipt/canonical import 和 Mac 4 KiB 双周期烧录；运行时验证仍未执行。

## 不变量与未决输入

- local Contract 的前置输入是共享 Gateway endpoint 与 owner-only P-256 signer；没有二者
  不生成或提交伪造 Contract。
- staging Contract 只能用于明确选择 staging 的构建，不能改名为 local。
- Dell 可以接收公开 Contract，但不能得到 AWS profile、KMS authority、Gateway private key、
  device credential、activation code、Wi-Fi password 或 token。
- 正式远端构建仍要求本机与 Dell checkout 为同一 clean commit；每次都必须重新核对状态、由用户
  另行授权 build，并在 Dell checkout 同步到目标 commit 后再运行入口。本次的具体证据仅适用于
  `0fc3a1ead0836fea492638677e1928feff603d96` 与记录的 staging Contract digest。
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
- Dell：`TXW82X_REMOTE_BUILD_IMPORTED` 与 `TXW82X_REMOTE_CLEAN_PACKAGE_READY` 已出现；
  receipt 绑定 staging Contract SHA-256、APP SHA-256 与 clean commit。
- Mac hardware：`TXW82X_CURRENT_APP_4K_RANGE_EXPERIMENT_PASS` 已出现；它证明两周期
  4 KiB 下载、target-side verify、marker cleanup 与事务内 reset，不证明应用启动或业务功能。
- 串口、Gateway HTTPS/time-bootstrap/activation/MQTT 以及 Demo App 联调：`NOT_RUN`。
