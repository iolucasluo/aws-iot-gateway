# Provider Composition 迁移与回滚契约 v1

## 目的与边界

本契约保护 `internal-aws` 海外组合在引入 `composition-cn-staging` 后仍可独立回归，并规定 provider composition 变更的可回滚边界。它不是新的部署平台，也不把国内 provider 映射成 AWS provider。

每个能力仍由命名的 provider instance 绑定：海外可以继续使用 AWS IoT、KVS、S3、SQS、KMS；国内可以组合自建或国内云实现。迁移只改变受服务端策略允许的 binding，不改变 App/设备自行选择云的权限。

## 发布不变量

1. Gateway 发布输入必须是 clean、可追溯的 commit；门禁和 build 都从同一个 pinned release source 重新导出。
2. 镜像只接受 staging ECR 的 immutable `@sha256` digest；发布记录保存 `previous_image`、当前 revision、API/worker 部署前后任务定义和新 digest。
3. CloudFormation change set 在执行前拒绝非预期的删除或 replacement；数据库 migration 只允许向前兼容，禁止把生产数据通过“回滚”降级或清空。
4. 新组合必须先通过本地 provider/fake 回归，再由显式授权的环境 owner 执行远端 AWS smoke。未拿到远端证据时，状态只能是 `UNVERIFIED`，不能标成 AWS staging 通过。

## 迁移顺序

1. 记录基线 commit、当前 image digest、数据库 schema 版本、`internal-aws` 的 IoT/KVS/S3/SQS/KMS 配置摘要（不记录 secret）。
2. 在 Gateway、Anyka、App 分别运行 AWS compatibility contract；确认 `internal-aws` profile、AWS KVS envelope 和旧 HTTP v1 仍可解析。
3. 对新组合只更新明确的 provider binding，并保留旧 AWS binding 和上一版 image，直到 health、命令 ACK、WebRTC、对象读写和签名时间检查完成。
4. 若新组合失败，停止继续切流，恢复到上一版 immutable image/旧 binding；保留数据库和审计记录，不执行 destructive migration rollback。
5. 重新验证旧组合的健康检查和关键业务 smoke，再决定是否继续迁移。

## 回滚判定

| 层级 | 可回滚动作 | 不能做的事 |
|---|---|---|
| Provider binding | 切回上一个已签名、已审核的 binding composition | 客户端传入任意 provider 或静默 fallback 到 AWS |
| Gateway image | 使用 release manifest 中的上一份 immutable digest | 使用 floating tag、现场重建未审计代码 |
| 数据库 | 保留向前兼容 schema；必要时停用新读写路径 | `DROP`、清空 staging 数据、逆向 migration 覆盖业务事实 |
| 设备 profile | 选择已验证的 `internal-aws`，按现有 boot/profile gate 验证并重启 | 通过环境变量临时注入 AWS credential 或跳过 profile 校验 |
| App signaling | 继续使用明确的 `aws-kvs-signaling-v1` envelope | 把 ExploreX/国内 JSON 当成 KVS envelope 解析 |

## 证据分层（2026-08-31）

- `PASS`：Gateway `test_aws_composition_regression_contract.sh`、Go fake/race tests、CloudFormation/release-source 静态门禁；Anyka 现有 `internal-aws` profile/boot/product-boundary contracts；App 的 legacy KVS model/service tests。
- `UNVERIFIED`：本契约未执行真实 AWS account/region smoke、ECS rollout、KVS/S3/SQS/KMS 真实读写或远端 rollback；这些动作需要对明确 staging account/region/stack 的单独授权。
- `UNVERIFIED`：本契约未执行 feeder 真机、OTA、公网 TURN/WebRTC 或 24 小时 soak；Phase5 的国内试产门禁仍保持 `NO-SHIP`。

## 本地验证入口

```text
bash deploy/aws-staging/test_aws_composition_regression_contract.sh
go test -race -count=1 ./...
go vet ./...
```

真实 AWS 验证必须继续使用 `deploy/aws-staging/deploy.sh` 的受控只读/验证模式，并由 owner 明确授权目标；本契约不授予云资源 mutation 权限。
