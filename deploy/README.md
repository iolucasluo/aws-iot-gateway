# Gateway 部署事实源

部署脚本、基础设施定义、配置模板与本机真实部署配置都归本目录。
`aws-deploy` / `cn-deploy` skill 只提供约束、导航和权限边界，不携带真实配置，也不建立另一套部署包。

| 目录 | 职责 | Git |
| --- | --- | --- |
| [aws-staging](aws-staging/README.md) | AWS staging 发布、验证和测试；CloudFormation 在其 `cloudformation/` 子目录 | 提交 |
| [cn-staging](cn-staging/README.md) | 国内 ECS / DELL 单机 Compose 部署，配置模板与验证脚本 | 提交 |
| [local](local/README.md) | 普通本机 Gateway 开发的依赖；保留 `tool/dev.sh` 及根 `.env.<name>` 合同 | 提交模板 |
| [device-bootstrap](device-bootstrap/README.md) | 跨环境设备公开接入契约、生成器及公开信任材料，不是部署运行秘密 | 提交审核后的公开资产 |
| `environments/<environment-id>/` | 本机真实部署配置事实源、身份材料与已部署记录 | 不提交，不进入 Docker context |

## 真实环境配置

当前已留存 `environments/aliyun-staging/`：

```text
config/             # stack.env、gateway.env、signal.env、MQTT ACL
runtime/            # composition、CA/证书、signer
releases/           # 已部署 release 的 Compose/覆盖文件历史证据
manifest.json       # 首次导入的文件指纹
deployment.json     # 目标、实际镜像与挂载信息
```

`.gitignore` 明确排除整个 `deploy/environments/`；可见目录不等于可提交目录。
复制给获授权的部署维护者时，部署代码与该环境材料一起交接，不放进 skill 源码目录。
不得打印真实 env、复制到测试夹具、force-add 或用临时清理命令删除它。
新环境不存在时先明确目标，不生成假配置；多个环境时按 environment-id 精确选择，不猜选第一个。

先修改本机期望配置、审查差异，再在明确授权下部署到服务器副本；远端漂移先回收候选审查，
不自动覆盖本机事实源。材料保留 Linux 目标路径，不能直接在 Mac source 后启动 Compose。
配置留存不是数据库备份：释放服务器前仍须完成持久数据备份及恢复验证。
AWS 凭据继续使用标准 credential chain，Secrets Manager / RDS 托管秘密不为目录统一而复制下来。

## 目录迁移边界

`staging/` 改为 `aws-staging/`，顶层 `cloudformation/` 收进 AWS 目录；
`domestic-staging/` 经 `cn-deploy/` 统一为 `cn-staging/`；skill 名称仍是 `cn-deploy`。
旧本机 `.local/cn-deploy/aliyun-staging/` 整体迁到 `environments/aliyun-staging/`，不维护双份事实源。
服务器 operator 根、历史 release 路径、镜像、project、volume、环境标识和 provider composition 均不变。
`device-bootstrap/contracts/` 的发布路径不变，避免仅为目录美观改变硬件消费合同。
