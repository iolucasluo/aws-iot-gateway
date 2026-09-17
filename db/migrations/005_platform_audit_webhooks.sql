-- 005_platform_audit_webhooks：门户审计主体列与项目级 webhook 配置。
--
-- 背景：004 之前 audit_logs 只描述 internal app 行为，平台面的账号动作无处落库；
-- 同时 webhook 目标一直来自部署期 INTERNAL_APPS_JSON，项目无法自助配置。
--
-- 语义：纯 additive。audit_logs 是 append-only 事实表，只补列与索引，不加 updated_at；
-- project_webhooks 是新表；投递仍复用既有 webhook_outbox/webhook_deliveries。

ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS actor_kind varchar(24);
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS actor_account_id uuid;
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS actor_label varchar(320);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'audit_logs_actor_kind_check' AND conrelid = 'audit_logs'::regclass
    ) THEN
        ALTER TABLE audit_logs ADD CONSTRAINT audit_logs_actor_kind_check
            CHECK (actor_kind IS NULL OR actor_kind IN ('portal_account', 'internal_app', 'system'));
    END IF;
END $$;

COMMENT ON COLUMN audit_logs.actor_kind IS
    '动作主体类型：portal_account（门户账号）、internal_app（HMAC 主体）、system（后台任务）。历史行为 NULL。';
COMMENT ON COLUMN audit_logs.actor_account_id IS
    '门户账号 id。不建外键：账号删除后审计事实必须保留。';
COMMENT ON COLUMN audit_logs.actor_label IS
    '动作主体的可读标识（门户账号为 email）。不存 secret/token/口令。';

CREATE INDEX IF NOT EXISTS idx_audit_logs_actor_time
    ON audit_logs (actor_account_id, created_at DESC) WHERE actor_account_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS project_webhooks (
    id uuid CONSTRAINT project_webhooks_pkey PRIMARY KEY,
    project_id uuid NOT NULL
        CONSTRAINT project_webhooks_project_id_fkey REFERENCES projects(id) ON DELETE CASCADE,
    callback_url text NOT NULL,
    secret_ciphertext bytea NOT NULL,
    kek_id varchar(64) NOT NULL,
    event_types text[] NOT NULL DEFAULT '{}',
    status varchar(16) NOT NULL DEFAULT 'active'
        CONSTRAINT project_webhooks_status_check CHECK (status IN ('active', 'disabled')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT project_webhooks_callback_url_check CHECK (btrim(callback_url) <> ''),
    CONSTRAINT project_webhooks_callback_url_scheme_check CHECK (callback_url LIKE 'https://%'),
    CONSTRAINT project_webhooks_secret_check CHECK (octet_length(secret_ciphertext) > 16),
    CONSTRAINT project_webhooks_kek_id_check CHECK (btrim(kek_id) <> '')
);

COMMENT ON TABLE project_webhooks IS
    '项目级 webhook 目标。回调 secret 与凭证同样只存 AES-256-GCM 密文；投递复用 webhook_outbox/webhook_deliveries。';

CREATE INDEX IF NOT EXISTS idx_project_webhooks_project ON project_webhooks (project_id, status);

DROP TRIGGER IF EXISTS trg_project_webhooks_updated_at ON project_webhooks;
CREATE TRIGGER trg_project_webhooks_updated_at
    BEFORE UPDATE ON project_webhooks
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
