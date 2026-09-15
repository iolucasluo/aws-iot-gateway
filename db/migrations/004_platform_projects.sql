-- 004_platform_projects：项目、项目产品白名单与项目级 API 凭证。
--
-- 背景：003 建立账号域后，本文件建立"凭证与设备归属的锚点"。凭证归属 project，
-- 但 project 不是 tenant：设备仍然全局唯一绑定（uniq_device_bindings_active_device
-- 不变），project 只表达作用域与归属，因此设备/激活表不需要新增归属列。
--
-- 语义：纯 additive。新增平台面表，并为既有 products 增加可空归属列；
-- 不改写 001/002/003 的既有结构，也不写业务数据。

CREATE TABLE IF NOT EXISTS projects (
    id uuid CONSTRAINT projects_pkey PRIMARY KEY,
    owner_account_id uuid NOT NULL
        CONSTRAINT projects_owner_account_id_fkey REFERENCES portal_accounts(id) ON DELETE RESTRICT,
    project_type varchar(16) NOT NULL
        CONSTRAINT projects_project_type_check CHECK (project_type IN ('cloud', 'oem')),
    name varchar(128) NOT NULL,
    description text,
    manufacturer_id uuid
        CONSTRAINT projects_manufacturer_id_fkey REFERENCES manufacturers(id) ON DELETE RESTRICT,
    status varchar(16) NOT NULL DEFAULT 'active'
        CONSTRAINT projects_status_check CHECK (status IN ('active', 'disabled')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT projects_name_check CHECK (btrim(name) <> ''),
    CONSTRAINT projects_type_manufacturer_check CHECK (
        (project_type = 'oem' AND manufacturer_id IS NOT NULL) OR
        (project_type = 'cloud' AND manufacturer_id IS NULL)
    )
);

COMMENT ON TABLE projects IS
    '项目：凭证与设备归属的锚点。它是作用域维度，不是 tenant；同一设备仍然只有一个全局 active binding。';

CREATE INDEX IF NOT EXISTS idx_projects_owner ON projects (owner_account_id, status);
CREATE INDEX IF NOT EXISTS idx_projects_manufacturer ON projects (manufacturer_id)
    WHERE manufacturer_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS project_products (
    id uuid CONSTRAINT project_products_pkey PRIMARY KEY,
    project_id uuid NOT NULL
        CONSTRAINT project_products_project_id_fkey REFERENCES projects(id) ON DELETE CASCADE,
    product_id uuid NOT NULL
        CONSTRAINT project_products_product_id_fkey REFERENCES products(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uniq_project_products UNIQUE (project_id, product_id)
);

COMMENT ON TABLE project_products IS
    '项目可用的产品白名单。它对 DB 凭证主体同时决定 GET /api/v1/openapi/device-products 的可见集合。';

CREATE TABLE IF NOT EXISTS api_credentials (
    id uuid CONSTRAINT api_credentials_pkey PRIMARY KEY,
    project_id uuid NOT NULL
        CONSTRAINT api_credentials_project_id_fkey REFERENCES projects(id) ON DELETE RESTRICT,
    name varchar(128) NOT NULL,
    api_key varchar(96) NOT NULL CONSTRAINT api_credentials_api_key_key UNIQUE,
    api_secret_ciphertext bytea NOT NULL,
    kek_id varchar(64) NOT NULL,
    permissions text[] NOT NULL DEFAULT '{}',
    status varchar(16) NOT NULL DEFAULT 'active'
        CONSTRAINT api_credentials_status_check CHECK (status IN ('active', 'disabled')),
    expires_at timestamptz,
    last_used_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT api_credentials_name_check CHECK (btrim(name) <> ''),
    CONSTRAINT api_credentials_api_key_check CHECK (btrim(api_key) <> ''),
    CONSTRAINT api_credentials_kek_id_check CHECK (btrim(kek_id) <> ''),
    CONSTRAINT api_credentials_secret_check CHECK (octet_length(api_secret_ciphertext) > 16),
    CONSTRAINT api_credentials_permissions_check CHECK (
        permissions <@ ARRAY['devices:read', 'devices:operate', 'ota:publish', 'ota:deploy']::text[]
    )
);

COMMENT ON TABLE api_credentials IS
    '项目级 HMAC 凭证。secret 只存 AES-256-GCM 密文，明文仅在创建/轮换响应中出现一次；主体 id 取 project_id，因此轮换凭证不会丢失设备归属。';
COMMENT ON COLUMN api_credentials.permissions IS
    '权限白名单（CHECK 约束）。未列出的权限无法写入，避免权限字符串自由漂移。';

CREATE INDEX IF NOT EXISTS idx_api_credentials_project ON api_credentials (project_id, status);

-- products 归属：nullable，seed/catalog 产品没有 owner。
ALTER TABLE products ADD COLUMN IF NOT EXISTS owner_account_id uuid;
ALTER TABLE products ADD COLUMN IF NOT EXISTS category varchar(32);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'products_owner_account_id_fkey' AND conrelid = 'products'::regclass
    ) THEN
        ALTER TABLE products ADD CONSTRAINT products_owner_account_id_fkey
            FOREIGN KEY (owner_account_id) REFERENCES portal_accounts(id) ON DELETE RESTRICT;
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_products_owner ON products (owner_account_id)
    WHERE owner_account_id IS NOT NULL;

DROP TRIGGER IF EXISTS trg_projects_updated_at ON projects;
CREATE TRIGGER trg_projects_updated_at
    BEFORE UPDATE ON projects
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

DROP TRIGGER IF EXISTS trg_project_products_updated_at ON project_products;
CREATE TRIGGER trg_project_products_updated_at
    BEFORE UPDATE ON project_products
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

DROP TRIGGER IF EXISTS trg_api_credentials_updated_at ON api_credentials;
CREATE TRIGGER trg_api_credentials_updated_at
    BEFORE UPDATE ON api_credentials
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
