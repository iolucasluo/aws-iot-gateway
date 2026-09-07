-- Provider-neutral binding facts. A device has at most one active instance
-- for each capability; the adapter-specific external reference stays opaque.
CREATE TABLE device_component_bindings (
    id uuid CONSTRAINT device_component_bindings_pkey PRIMARY KEY,
    device_registry_id uuid NOT NULL
        CONSTRAINT device_component_bindings_device_registry_id_fkey
        REFERENCES device_registry(id) ON DELETE RESTRICT,
    device_binding_id uuid NOT NULL
        CONSTRAINT device_component_bindings_device_binding_id_fkey
        REFERENCES device_bindings(id) ON DELETE RESTRICT,
    capability varchar(64) NOT NULL
        CONSTRAINT device_component_bindings_capability_check
        CHECK (capability ~ '^[a-z][a-z0-9.-]{0,63}$'),
    provider_instance varchar(64) NOT NULL
        CONSTRAINT device_component_bindings_provider_instance_check
        CHECK (provider_instance ~ '^[a-z0-9][a-z0-9._-]{0,63}$'),
    external_ref text NOT NULL
        CONSTRAINT device_component_bindings_external_ref_check
        CHECK (btrim(external_ref) <> '' AND external_ref !~ '[\r\n]'),
    contract_version varchar(64) NOT NULL
        CONSTRAINT device_component_bindings_contract_version_check
        CHECK (contract_version ~ '^[a-z0-9][a-z0-9._-]{0,63}$'),
    status varchar(16) NOT NULL
        CONSTRAINT device_component_bindings_status_check
        CHECK (status IN ('active', 'revoked')),
    revoked_reason varchar(128),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT device_component_bindings_revocation_check
        CHECK ((status = 'active' AND revoked_reason IS NULL) OR
               (status = 'revoked' AND revoked_reason IS NOT NULL AND btrim(revoked_reason) <> ''))
);

CREATE UNIQUE INDEX uniq_device_component_bindings_active
    ON device_component_bindings (device_registry_id, capability)
    WHERE status = 'active';
CREATE INDEX idx_device_component_bindings_binding
    ON device_component_bindings (device_binding_id, status);

CREATE TRIGGER trg_device_component_bindings_updated_at
    BEFORE UPDATE ON device_component_bindings
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

COMMENT ON TRIGGER trg_device_component_bindings_updated_at ON device_component_bindings IS
    '统一维护 provider composition binding 的 updated_at；external_ref 仅由对应 adapter 解释。';
