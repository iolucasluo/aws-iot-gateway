# Branch: py/develop

## 创建时间

2026-09-01

## 目标与成功标准

- **目标**：在当前固定 AWS IoT Gateway 参考 commit 上建立本地 py/develop，统一新聚合工作区的本地 Git 入口。
- **成功标准**：
  - 分支日志先于分支创建提交。
  - py/develop 从当前固定 commit 创建。
  - 不修改 Gateway 参考实现。
- **非目标**：不承接 LivHome 产品需求，不修改 API 或设备实现。

## 范围、所有权与影响

| Repository / module（branch） | 本分支职责 | 精确文件或候选路径 | 契约 / 消费者影响 |
|---|---|---|---|
| aws-iot-gateway（py/develop） | 仅建立个人开发分支和分支日志 | docs/branches/py-develop.md | 仅作为参考仓；无 LivHome 产品契约变更 |

## 实施计划与当前状态

- [x] 创建分支日志文件 — owner: aws-iot-gateway；files: docs/branches/py-develop.md；verify: V1
- [x] 创建 py/develop 分支 — owner: aws-iot-gateway；files: Git ref；depends: 日志 commit a07f0cb；verify: V2

## 验证计划与证据

| ID | 测试 / 检查 owner 与 cwd | 准确命令或检查 | 预期证据 | 实际证据 |
|---|---|---|---|---|
| V1 | aws-iot-gateway；cwd: 当前子仓根 | git status --short、git rev-parse HEAD | 仅分支日志待提交，无参考实现 dirty | 已验证：a07f0cb 仅提交分支日志，工作树无业务 dirty |
| V2 | aws-iot-gateway；cwd: 当前子仓根 | git branch --show-current、git rev-parse py/develop | 当前为 py/develop，分支从固定 commit 创建 | 已验证：当前分支为 py/develop；HEAD a07f0cb，基于 84f44dc |

## 全局约束与未决问题

- **约束 / 安全边界**：参考仓默认只读；本次只添加分支日志元数据，不修改参考实现；不 push。
- **未决问题**：是否向远端创建/推送 py/develop，后续单独确认。

## 关键决策记录

- 以当前聚合主仓固定的 aws-iot-gateway gitlink commit 为分支起点，避免参考版本漂移。

## 相关文档与备注

- **设计 / spec / finding**：聚合主仓 docs/branches/py-develop.md
- **开发工作区**：普通 checkout；参考 child repository
- **修改前基线**：当前为 detached HEAD，工作树干净
- **MR / 合并状态**：未创建
- **备注**：本地 py/develop 已创建；尚未 push。
