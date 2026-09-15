-- 006_project_webhooks_single_active：每个项目最多一个 active 回调。
--
-- 背景：投递器按"项目 → 目标"解析，一个项目出现两个 active 回调会让解析结果不确定。
-- 005 已经发布过（可能已被其它环境登记），因此这里的约束以新迁移补齐，
-- 不改写 005 的历史。
--
-- 语义：纯 additive；只加 partial unique index。

CREATE UNIQUE INDEX IF NOT EXISTS uniq_project_webhooks_active
    ON project_webhooks (project_id) WHERE status = 'active';

COMMENT ON INDEX uniq_project_webhooks_active IS
    '每个项目最多一个 active 回调；多个目标需要显式扇出设计，当前不支持。';
