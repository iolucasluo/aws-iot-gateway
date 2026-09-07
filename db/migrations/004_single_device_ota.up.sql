-- Minimal OTA inventory and per-device execution records. This is deliberately
-- not a campaign scheduler and does not depend on AWS IoT Jobs.

CREATE TABLE IF NOT EXISTS ota_firmware_versions (
    id uuid CONSTRAINT ota_firmware_versions_pkey PRIMARY KEY,
    created_by_internal_app_id varchar(64) NOT NULL,
    idempotency_key varchar(128) NOT NULL,
    pid varchar(64) NOT NULL
        CONSTRAINT ota_firmware_versions_pid_fkey REFERENCES products(pid) ON DELETE RESTRICT,
    target_environment_id varchar(64),
    target_environment_code varchar(2),
    version varchar(64) NOT NULL,
    firmware_build bigint NOT NULL
        CONSTRAINT ota_firmware_versions_build_check CHECK (firmware_build > 0),
    sha256 varchar(64) NOT NULL
        CONSTRAINT ota_firmware_versions_sha256_check CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    manifest_sha256 varchar(64) NOT NULL
        CONSTRAINT ota_firmware_versions_manifest_sha256_check
        CHECK (manifest_sha256 ~ '^[0-9a-f]{64}$'),
    file_size bigint NOT NULL
        CONSTRAINT ota_firmware_versions_file_size_check
        CHECK (file_size > 0 AND file_size <= 3145728),
    release_statement text NOT NULL,
    signature text NOT NULL,
    artifact_key text,
    source_url text,
    status varchar(16) NOT NULL
        CONSTRAINT ota_firmware_versions_status_check CHECK (status IN ('uploading', 'available')),
    available_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ota_firmware_versions_target_environment_pair_check
        CHECK ((target_environment_id IS NULL) = (target_environment_code IS NULL)),
    CONSTRAINT ota_firmware_versions_target_environment_id_check
        CHECK (target_environment_id IS NULL OR target_environment_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$'),
    CONSTRAINT ota_firmware_versions_target_environment_code_check
        CHECK (target_environment_code IS NULL OR target_environment_code ~ '^(0[1-9]|[1-9][0-9])$'),
    CONSTRAINT ota_firmware_versions_artifact_source_check
        CHECK ((artifact_key IS NOT NULL) <> (source_url IS NOT NULL)),
    CONSTRAINT ota_firmware_versions_status_available_check
        CHECK ((status = 'available' AND available_at IS NOT NULL) OR
               (status = 'uploading' AND available_at IS NULL)),
    CONSTRAINT ota_firmware_versions_publisher_idempotency_key
        UNIQUE (created_by_internal_app_id, idempotency_key),
    CONSTRAINT ota_firmware_versions_pid_version_key UNIQUE (pid, version),
    CONSTRAINT ota_firmware_versions_pid_build_key UNIQUE (pid, firmware_build)
);

CREATE TABLE IF NOT EXISTS device_ota_deployments (
    id uuid CONSTRAINT device_ota_deployments_pkey PRIMARY KEY,
    device_registry_id uuid NOT NULL
        CONSTRAINT device_ota_deployments_device_registry_id_fkey
        REFERENCES device_registry(id) ON DELETE RESTRICT,
    internal_app_id varchar(64) NOT NULL,
    firmware_version_id uuid NOT NULL
        CONSTRAINT device_ota_deployments_firmware_version_id_fkey
        REFERENCES ota_firmware_versions(id) ON DELETE RESTRICT,
    idempotency_key varchar(128) NOT NULL,
    target_version varchar(64) NOT NULL,
    target_build bigint NOT NULL
        CONSTRAINT device_ota_deployments_target_build_check CHECK (target_build > 0),
    state varchar(16) NOT NULL
        CONSTRAINT device_ota_deployments_state_check CHECK (state IN (
            'pending', 'accepted', 'downloading', 'verifying', 'installing',
            'succeeded', 'failed', 'rolled_back'
        )),
    failure_reason varchar(128),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    CONSTRAINT device_ota_deployments_owner_idempotency_key
        UNIQUE (internal_app_id, device_registry_id, idempotency_key),
    CONSTRAINT device_ota_deployments_completion_check CHECK (
        (state IN ('succeeded', 'failed', 'rolled_back') AND completed_at IS NOT NULL) OR
        (state NOT IN ('succeeded', 'failed', 'rolled_back') AND completed_at IS NULL)
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS uniq_device_ota_deployments_active
    ON device_ota_deployments (device_registry_id)
    WHERE state IN ('pending', 'accepted', 'downloading', 'verifying', 'installing');
CREATE INDEX IF NOT EXISTS idx_device_ota_deployments_history
    ON device_ota_deployments (device_registry_id, created_at DESC, id DESC);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_trigger
        WHERE tgrelid = 'ota_firmware_versions'::regclass
          AND tgname = 'trg_ota_firmware_versions_updated_at'
          AND NOT tgisinternal
    ) THEN
        CREATE TRIGGER trg_ota_firmware_versions_updated_at
            BEFORE UPDATE ON ota_firmware_versions
            FOR EACH ROW EXECUTE FUNCTION set_updated_at();
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_trigger
        WHERE tgrelid = 'device_ota_deployments'::regclass
          AND tgname = 'trg_device_ota_deployments_updated_at'
          AND NOT tgisinternal
    ) THEN
        CREATE TRIGGER trg_device_ota_deployments_updated_at
            BEFORE UPDATE ON device_ota_deployments
            FOR EACH ROW EXECUTE FUNCTION set_updated_at();
    END IF;
END;
$$;
