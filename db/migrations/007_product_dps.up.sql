-- Gateway-owned product DP contract catalog. Product/DP mutation remains
-- migration/seed based; no runtime CRUD is exposed by this service yet.
CREATE TABLE product_dps (
    id uuid CONSTRAINT product_dps_pkey PRIMARY KEY,
    product_id uuid NOT NULL
        CONSTRAINT product_dps_product_id_fkey REFERENCES products(id) ON DELETE RESTRICT,
    code varchar(64) NOT NULL,
    type varchar(16) NOT NULL
        CONSTRAINT product_dps_type_check CHECK (type IN ('bool', 'int', 'float', 'string', 'enum', 'object', 'array', 'raw')),
    direction varchar(32) NOT NULL
        CONSTRAINT product_dps_direction_check CHECK (direction IN ('wo', 'rw', 'ro', 'event', 'downlink_request_ack')),
    spec jsonb NOT NULL DEFAULT '{}'::jsonb
        CONSTRAINT product_dps_spec_check CHECK (jsonb_typeof(spec) = 'object'),
    status varchar(16) NOT NULL DEFAULT 'active'
        CONSTRAINT product_dps_status_check CHECK (status IN ('active', 'disabled')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT product_dps_code_check CHECK (btrim(code) <> ''),
    CONSTRAINT product_dps_product_id_code_key UNIQUE (product_id, code)
);

CREATE INDEX idx_product_dps_product_status
    ON product_dps (product_id, status, code);

CREATE TRIGGER trg_product_dps_updated_at
    BEFORE UPDATE ON product_dps
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

COMMENT ON TRIGGER trg_product_dps_updated_at ON product_dps IS
    '统一维护 Gateway 产品 DP 合同的 updated_at。';
