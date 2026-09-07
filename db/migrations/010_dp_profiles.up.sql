-- Device-scoped, versioned command contracts. PID is not a runtime DP selector.
CREATE TABLE dp_profiles (
    id uuid CONSTRAINT dp_profiles_pkey PRIMARY KEY,
    code varchar(96) NOT NULL CONSTRAINT dp_profiles_code_key UNIQUE,
    name varchar(128) NOT NULL,
    status varchar(16) NOT NULL CONSTRAINT dp_profiles_status_check
        CHECK (status IN ('active', 'retired', 'disabled')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT dp_profiles_code_check CHECK (code ~ '^[a-z0-9][a-z0-9.-]*-v[1-9][0-9]*$'),
    CONSTRAINT dp_profiles_name_check CHECK (btrim(name) <> '')
);

CREATE TABLE dp_definitions (
    id uuid CONSTRAINT dp_definitions_pkey PRIMARY KEY,
    code varchar(64) NOT NULL CONSTRAINT dp_definitions_code_check CHECK (btrim(code) <> ''),
    type varchar(16) NOT NULL CONSTRAINT dp_definitions_type_check
        CHECK (type IN ('bool', 'int', 'float', 'string', 'enum', 'object', 'array', 'raw')),
    direction varchar(32) NOT NULL CONSTRAINT dp_definitions_direction_check
        CHECK (direction IN ('wo', 'rw', 'ro', 'event', 'downlink_request_ack')),
    spec jsonb NOT NULL CONSTRAINT dp_definitions_spec_check CHECK (jsonb_typeof(spec) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT dp_definitions_id_code_key UNIQUE (id, code)
);

CREATE TABLE dp_profile_members (
    id uuid CONSTRAINT dp_profile_members_pkey PRIMARY KEY,
    dp_profile_id uuid NOT NULL CONSTRAINT dp_profile_members_dp_profile_id_fkey
        REFERENCES dp_profiles(id) ON DELETE RESTRICT,
    dp_definition_id uuid NOT NULL,
    code varchar(64) NOT NULL,
    status varchar(16) NOT NULL CONSTRAINT dp_profile_members_status_check CHECK (status IN ('active', 'disabled')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT dp_profile_members_dp_definition_id_code_fkey
        FOREIGN KEY (dp_definition_id, code) REFERENCES dp_definitions(id, code) ON DELETE RESTRICT,
    CONSTRAINT dp_profile_members_dp_profile_id_code_key UNIQUE (dp_profile_id, code)
);
CREATE INDEX idx_dp_profile_members_definition ON dp_profile_members (dp_definition_id);
CREATE INDEX idx_dp_profiles_status ON dp_profiles (status, code);

ALTER TABLE device_registry ADD COLUMN dp_profile_id uuid
    CONSTRAINT device_registry_dp_profile_id_fkey REFERENCES dp_profiles(id) ON DELETE RESTRICT;
ALTER TABLE device_activations ADD COLUMN dp_profile_id uuid
    CONSTRAINT device_activations_dp_profile_id_fkey REFERENCES dp_profiles(id) ON DELETE RESTRICT;
CREATE INDEX idx_device_registry_dp_profile ON device_registry (dp_profile_id);
CREATE INDEX idx_device_activations_dp_profile ON device_activations (dp_profile_id);

-- Preserve the actual old database contract, including local overrides and disabled
-- members. Legacy profiles are retired (usable by existing devices, not selectable
-- for new activations). The PID association exists only in this one-time migration.
INSERT INTO dp_profiles (id, code, name, status)
SELECT p.id, 'legacy-' || p.id::text || '-v1', p.name,
       CASE WHEN p.status='active' THEN 'retired' ELSE 'disabled' END
FROM products p;

INSERT INTO dp_definitions (id, code, type, direction, spec)
SELECT min(id::text)::uuid, code, type, direction, spec
FROM product_dps GROUP BY code, type, direction, spec;

INSERT INTO dp_profile_members (id, dp_profile_id, dp_definition_id, code, status)
SELECT old.id, old.product_id, definition.id, old.code, old.status
FROM product_dps old JOIN dp_definitions definition
    ON definition.code=old.code AND definition.type=old.type
    AND definition.direction=old.direction AND definition.spec=old.spec;

UPDATE device_registry d SET dp_profile_id=p.id FROM products p WHERE p.pid=d.pid;
UPDATE device_activations a SET dp_profile_id=p.id FROM products p WHERE p.pid=a.pid;
ALTER TABLE device_registry ALTER COLUMN dp_profile_id SET NOT NULL;
ALTER TABLE device_activations ALTER COLUMN dp_profile_id SET NOT NULL;
DROP TABLE product_dps;

CREATE TRIGGER trg_dp_profiles_updated_at
    BEFORE UPDATE ON dp_profiles FOR EACH ROW EXECUTE FUNCTION set_updated_at();
COMMENT ON TRIGGER trg_dp_profiles_updated_at ON dp_profiles IS
    'Maintain DP contract catalog updated_at.';

CREATE TRIGGER trg_dp_definitions_updated_at
    BEFORE UPDATE ON dp_definitions FOR EACH ROW EXECUTE FUNCTION set_updated_at();
COMMENT ON TRIGGER trg_dp_definitions_updated_at ON dp_definitions IS
    'Maintain DP contract catalog updated_at.';

CREATE TRIGGER trg_dp_profile_members_updated_at
    BEFORE UPDATE ON dp_profile_members FOR EACH ROW EXECUTE FUNCTION set_updated_at();
COMMENT ON TRIGGER trg_dp_profile_members_updated_at ON dp_profile_members IS
    'Maintain DP contract catalog updated_at.';
