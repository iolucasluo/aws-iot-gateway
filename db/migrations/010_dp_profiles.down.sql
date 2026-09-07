-- Run with API/worker stopped, before adopting new profiles. A profile model can
-- express contracts the old PID model cannot; refuse those cases without data loss.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM device_registry d JOIN products p ON p.pid=d.pid
        WHERE d.dp_profile_id IS DISTINCT FROM p.id
    ) OR EXISTS (
        SELECT 1 FROM device_activations a JOIN products p ON p.pid=a.pid
        WHERE a.dp_profile_id IS DISTINCT FROM p.id
    ) OR EXISTS (
        SELECT 1 FROM dp_profiles dp LEFT JOIN products p ON p.id=dp.id
        WHERE p.id IS NULL OR dp.code <> 'legacy-' || p.id::text || '-v1'
           OR dp.status <> CASE WHEN p.status='active' THEN 'retired' ELSE 'disabled' END
    ) THEN
        RAISE EXCEPTION 'DP profile rollback cannot represent current device contracts by PID' USING ERRCODE='23514';
    END IF;
END $$;

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

INSERT INTO product_dps (id, product_id, code, type, direction, spec, status)
SELECT member.id, profile.id, definition.code, definition.type, definition.direction, definition.spec, member.status
FROM dp_profile_members member
JOIN dp_profiles profile ON profile.id=member.dp_profile_id
JOIN dp_definitions definition ON definition.id=member.dp_definition_id;
ALTER TABLE device_activations DROP COLUMN dp_profile_id;
ALTER TABLE device_registry DROP COLUMN dp_profile_id;
DROP TABLE dp_profile_members;
DROP TABLE dp_definitions;
DROP TABLE dp_profiles;
