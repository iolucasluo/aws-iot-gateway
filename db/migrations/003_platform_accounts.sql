-- 003_platform_accounts：平台面账号、会话与一次性 setup token。
--
-- 背景：gateway 定位从「activation + AWS IoT 窄域中间层」扩展为「IoT 中间层 +
-- ExploreX 平台面」，见 docs/architecture/platform-plane-v1.md。设备域 schema 不变；
-- 平台面只表达归属与作用域，不作为 tenant 隔离维度，也不新建第二套设备域。
--
-- 语义：纯 additive（只新建平台面表），可重复执行。manufacturers 放在本文件，
-- 因为 portal_accounts.manufacturer_id 需要它先存在；projects 等归属表在 004。
--
-- 触发器使用 DROP TRIGGER IF EXISTS + CREATE TRIGGER 保持可重复执行：被删除并重建的
-- 只可能是本文件自己创建的触发器，不触碰 001/002 建立的任何结构。

CREATE TABLE IF NOT EXISTS manufacturers (
    id uuid CONSTRAINT manufacturers_pkey PRIMARY KEY,
    code varchar(64) NOT NULL CONSTRAINT manufacturers_code_key UNIQUE,
    name varchar(128) NOT NULL,
    contact_name varchar(128),
    contact_email varchar(320),
    status varchar(16) NOT NULL DEFAULT 'active'
        CONSTRAINT manufacturers_status_check CHECK (status IN ('active', 'disabled')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT manufacturers_code_check CHECK (btrim(code) <> ''),
    CONSTRAINT manufacturers_name_check CHECK (btrim(name) <> ''),
    CONSTRAINT manufacturers_contact_email_check
        CHECK (contact_email IS NULL OR (contact_email = lower(btrim(contact_email)) AND contact_email <> ''))
);

COMMENT ON TABLE manufacturers IS
    '代工厂主数据（平台面）。它是归属与作用域维度，不是 tenant；设备绑定仍然全局唯一。';

CREATE TABLE IF NOT EXISTS portal_accounts (
    id uuid CONSTRAINT portal_accounts_pkey PRIMARY KEY,
    kind varchar(16) NOT NULL
        CONSTRAINT portal_accounts_kind_check CHECK (kind IN ('staff', 'developer', 'manufacturer')),
    role varchar(24) NOT NULL,
    email varchar(320) NOT NULL CONSTRAINT portal_accounts_email_key UNIQUE,
    password_hash text,
    display_name varchar(128),
    manufacturer_id uuid
        CONSTRAINT portal_accounts_manufacturer_id_fkey REFERENCES manufacturers(id) ON DELETE RESTRICT,
    status varchar(16) NOT NULL DEFAULT 'active'
        CONSTRAINT portal_accounts_status_check CHECK (status IN ('active', 'disabled')),
    last_login_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT portal_accounts_email_check CHECK (email = lower(btrim(email)) AND email <> ''),
    CONSTRAINT portal_accounts_password_hash_check
        CHECK (password_hash IS NULL OR btrim(password_hash) <> ''),
    CONSTRAINT portal_accounts_kind_role_check CHECK (
        (kind = 'staff' AND role IN ('super_admin') AND manufacturer_id IS NULL) OR
        (kind = 'developer' AND role IN ('developer') AND manufacturer_id IS NULL) OR
        (kind = 'manufacturer' AND role IN ('admin', 'operator') AND manufacturer_id IS NOT NULL)
    )
);

COMMENT ON TABLE portal_accounts IS
    '门户账号（staff / developer / manufacturer）。kind、role 与 manufacturer_id 的组合由 CHECK 锁定，不存在跨门户的通用账号。';
COMMENT ON COLUMN portal_accounts.password_hash IS
    'Argon2id 标准编码串（$argon2id$...）。NULL 表示账号已创建但尚未通过一次性 setup token 设置口令，此时不允许登录。';

CREATE INDEX IF NOT EXISTS idx_portal_accounts_kind_status ON portal_accounts (kind, status);
CREATE INDEX IF NOT EXISTS idx_portal_accounts_manufacturer ON portal_accounts (manufacturer_id)
    WHERE manufacturer_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS portal_sessions (
    id uuid CONSTRAINT portal_sessions_pkey PRIMARY KEY,
    account_id uuid NOT NULL
        CONSTRAINT portal_sessions_account_id_fkey REFERENCES portal_accounts(id) ON DELETE CASCADE,
    token_hash bytea NOT NULL CONSTRAINT portal_sessions_token_hash_key UNIQUE,
    audience varchar(16) NOT NULL
        CONSTRAINT portal_sessions_audience_check CHECK (audience IN ('admin', 'developer', 'manufacturer')),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT portal_sessions_token_hash_check CHECK (octet_length(token_hash) = 32)
);

COMMENT ON TABLE portal_sessions IS
    '门户会话。只保存 token 的 SHA-256；audience 必须与账号 kind 匹配，由应用层在签发和校验时双重确认。';

CREATE INDEX IF NOT EXISTS idx_portal_sessions_account_active
    ON portal_sessions (account_id, expires_at) WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS portal_account_setup_tokens (
    id uuid CONSTRAINT portal_account_setup_tokens_pkey PRIMARY KEY,
    account_id uuid NOT NULL
        CONSTRAINT portal_account_setup_tokens_account_id_fkey REFERENCES portal_accounts(id) ON DELETE CASCADE,
    token_hash bytea NOT NULL CONSTRAINT portal_account_setup_tokens_token_hash_key UNIQUE,
    purpose varchar(16) NOT NULL
        CONSTRAINT portal_account_setup_tokens_purpose_check CHECK (purpose IN ('invite', 'password_reset')),
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT portal_account_setup_tokens_token_hash_check CHECK (octet_length(token_hash) = 32)
);

COMMENT ON TABLE portal_account_setup_tokens IS
    '一次性邀请 / 重置口令 token。明文只在创建响应中出现一次，过期或已使用后不可复用。';

CREATE INDEX IF NOT EXISTS idx_portal_account_setup_tokens_account
    ON portal_account_setup_tokens (account_id) WHERE used_at IS NULL;

DROP TRIGGER IF EXISTS trg_manufacturers_updated_at ON manufacturers;
CREATE TRIGGER trg_manufacturers_updated_at
    BEFORE UPDATE ON manufacturers
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

DROP TRIGGER IF EXISTS trg_portal_accounts_updated_at ON portal_accounts;
CREATE TRIGGER trg_portal_accounts_updated_at
    BEFORE UPDATE ON portal_accounts
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

DROP TRIGGER IF EXISTS trg_portal_sessions_updated_at ON portal_sessions;
CREATE TRIGGER trg_portal_sessions_updated_at
    BEFORE UPDATE ON portal_sessions
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

DROP TRIGGER IF EXISTS trg_portal_account_setup_tokens_updated_at ON portal_account_setup_tokens;
CREATE TRIGGER trg_portal_account_setup_tokens_updated_at
    BEFORE UPDATE ON portal_account_setup_tokens
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
