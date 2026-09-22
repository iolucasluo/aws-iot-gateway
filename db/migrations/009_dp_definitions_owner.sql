-- 009_dp_definitions_owner.sql
-- 为 dp_definitions 增加 owner_account_id，区分公共 DP（NULL）与开发者自定义 DP。
-- 纯 additive：不改现有行，不加 FK（账号删除后 DP 定义保留）。

ALTER TABLE dp_definitions ADD COLUMN IF NOT EXISTS owner_account_id uuid;

COMMENT ON COLUMN dp_definitions.owner_account_id IS
    'NULL = 平台公共 DP（seed 发布）；非 NULL = 开发者自定义 DP，仅该账号可见/可删。';

CREATE INDEX IF NOT EXISTS idx_dp_definitions_owner
    ON dp_definitions(owner_account_id)
    WHERE owner_account_id IS NOT NULL;
