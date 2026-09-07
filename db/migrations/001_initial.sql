-- Current pre-production schema. Initialize an empty database, then apply seeds explicitly.
-- Historical upgrade/backfill/down paths have been removed; existing test databases must be rebuilt.

CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$;

COMMENT ON FUNCTION set_updated_at() IS
    '统一维护 Gateway 可变业务表 updated_at；调用方不得依赖手工更新时间戳。';

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
    dp_profile_code varchar(96) NOT NULL CONSTRAINT dp_profile_members_dp_profile_code_fkey
        REFERENCES dp_profiles(code) ON UPDATE RESTRICT ON DELETE RESTRICT,
    dp_definition_id uuid NOT NULL,
    code varchar(64) NOT NULL,
    status varchar(16) NOT NULL CONSTRAINT dp_profile_members_status_check CHECK (status IN ('active', 'disabled')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT dp_profile_members_dp_definition_id_code_fkey
        FOREIGN KEY (dp_definition_id, code) REFERENCES dp_definitions(id, code) ON DELETE RESTRICT,
    CONSTRAINT dp_profile_members_dp_profile_code_code_key UNIQUE (dp_profile_code, code)
);
CREATE INDEX idx_dp_profile_members_definition ON dp_profile_members (dp_definition_id);
CREATE INDEX idx_dp_profiles_status ON dp_profiles (status, code);

CREATE TABLE products (
    id uuid CONSTRAINT products_pkey PRIMARY KEY,
    pid varchar(64) NOT NULL CONSTRAINT products_pid_key UNIQUE,
    dp_profile_code varchar(96)
        CONSTRAINT products_dp_profile_code_fkey REFERENCES dp_profiles(code) ON UPDATE RESTRICT ON DELETE RESTRICT,
    name varchar(128) NOT NULL,
    status varchar(16) NOT NULL DEFAULT 'active'
        CONSTRAINT products_status_check CHECK (status IN ('active', 'disabled')),
    capabilities jsonb NOT NULL DEFAULT '[]'::jsonb
        CONSTRAINT products_capabilities_check CHECK (jsonb_typeof(capabilities) = 'array'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT products_pid_check CHECK (btrim(pid) <> ''),
    CONSTRAINT products_name_check CHECK (btrim(name) <> '')
);

CREATE TABLE device_registry (
    id uuid CONSTRAINT device_registry_pkey PRIMARY KEY,
    dp_profile_id uuid NOT NULL
        CONSTRAINT device_registry_dp_profile_id_fkey REFERENCES dp_profiles(id) ON DELETE RESTRICT,
    device_sn varchar(64) NOT NULL CONSTRAINT device_registry_device_sn_key UNIQUE,
    thing_name varchar(128) NOT NULL CONSTRAINT device_registry_thing_name_key UNIQUE,
    pid varchar(64) NOT NULL
        CONSTRAINT device_registry_pid_check CHECK (btrim(pid) <> ''),
    certificate_id varchar(128),
    certificate_arn text,
    aws_iot_endpoint text,
    status varchar(24) NOT NULL
        CONSTRAINT device_registry_status_check
        CHECK (status IN ('provisioning', 'active', 'revoked', 'reset')),
    presence_online boolean NOT NULL DEFAULT false,
    presence_updated_at timestamptz,
    last_seen_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT device_registry_thing_name_check CHECK (thing_name = device_sn),
    CONSTRAINT device_registry_pid_fkey FOREIGN KEY (pid) REFERENCES products(pid) ON DELETE RESTRICT
);

CREATE TABLE device_activations (
    id uuid CONSTRAINT device_activations_pkey PRIMARY KEY,
    dp_profile_id uuid NOT NULL
        CONSTRAINT device_activations_dp_profile_id_fkey REFERENCES dp_profiles(id) ON DELETE RESTRICT,
    internal_app_id varchar(64) NOT NULL,
    binding_external_user_id varchar(255) NOT NULL,
    method varchar(8) NOT NULL
        CONSTRAINT device_activations_method_check CHECK (method IN ('qr', 'ap', 'ble')),
    pid varchar(64) NOT NULL
        CONSTRAINT device_activations_pid_check CHECK (btrim(pid) <> ''),
    status varchar(24) NOT NULL
        CONSTRAINT device_activations_status_check CHECK (status IN (
            'pending', 'activating', 'installing', 'finalizing',
            'activated', 'failed', 'expired', 'cancelled'
        )),
    code_hash bytea NOT NULL
        CONSTRAINT device_activations_code_hash_check CHECK (octet_length(code_hash) = 32),
    idempotency_key varchar(128) NOT NULL,
    request_hash bytea NOT NULL
        CONSTRAINT device_activations_request_hash_check CHECK (octet_length(request_hash) = 32),
    device_registry_id uuid
        CONSTRAINT device_activations_device_registry_id_fkey REFERENCES device_registry(id),
    failure_code varchar(64),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT device_activations_internal_app_id_idempotency_key_key
        UNIQUE (internal_app_id, idempotency_key),
    CONSTRAINT device_activations_pid_fkey FOREIGN KEY (pid) REFERENCES products(pid) ON DELETE RESTRICT
);

CREATE INDEX idx_device_activations_owner_status
    ON device_activations (internal_app_id, binding_external_user_id, status);

CREATE TABLE device_activation_sessions (
    id uuid CONSTRAINT device_activation_sessions_pkey PRIMARY KEY,
    device_activation_id uuid NOT NULL
        CONSTRAINT device_activation_sessions_device_activation_id_fkey
        REFERENCES device_activations(id) ON DELETE CASCADE,
    session_external_id varchar(255) NOT NULL,
    method varchar(8) NOT NULL
        CONSTRAINT device_activation_sessions_method_check CHECK (method IN ('qr', 'ap', 'ble')),
    status varchar(24) NOT NULL
        CONSTRAINT device_activation_sessions_status_check
        CHECK (status IN ('pending', 'prepared', 'confirmed', 'failed', 'expired')),
    csr_pem text,
    csr_sha256 bytea
        CONSTRAINT device_activation_sessions_csr_sha256_check
        CHECK (csr_sha256 IS NULL OR octet_length(csr_sha256) = 32),
    installation_nonce varchar(128),
    certificate_pem text,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT device_activation_sessions_activation_external_key
        UNIQUE (device_activation_id, session_external_id)
);

CREATE TABLE device_bindings (
    id uuid CONSTRAINT device_bindings_pkey PRIMARY KEY,
    device_registry_id uuid NOT NULL
        CONSTRAINT device_bindings_device_registry_id_fkey REFERENCES device_registry(id),
    internal_app_id varchar(64) NOT NULL,
    binding_external_user_id varchar(255) NOT NULL,
    status varchar(16) NOT NULL
        CONSTRAINT device_bindings_status_check CHECK (status IN ('active', 'inactive')),
    activated_at timestamptz NOT NULL DEFAULT now(),
    released_at timestamptz,
    release_reason varchar(64),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT device_bindings_release_state_check
        CHECK ((status = 'active' AND released_at IS NULL) OR status = 'inactive')
);

CREATE UNIQUE INDEX uniq_device_bindings_active_device
    ON device_bindings (device_registry_id) WHERE status = 'active';
CREATE INDEX idx_device_bindings_owner
    ON device_bindings (internal_app_id, binding_external_user_id, status);

CREATE TABLE device_tokens (
    id uuid CONSTRAINT device_tokens_pkey PRIMARY KEY,
    device_binding_id uuid NOT NULL
        CONSTRAINT device_tokens_device_binding_id_fkey REFERENCES device_bindings(id),
    token_hash bytea NOT NULL CONSTRAINT device_tokens_token_hash_key UNIQUE
        CONSTRAINT device_tokens_token_hash_check CHECK (octet_length(token_hash) = 32),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_device_tokens_binding_active
    ON device_tokens (device_binding_id, expires_at) WHERE revoked_at IS NULL;

CREATE TABLE device_commands (
    id uuid CONSTRAINT device_commands_pkey PRIMARY KEY,
    device_registry_id uuid NOT NULL
        CONSTRAINT device_commands_device_registry_id_fkey REFERENCES device_registry(id),
    internal_app_id varchar(64) NOT NULL,
    session_id varchar(128) NOT NULL,
    request_id varchar(128),
    command jsonb NOT NULL
        CONSTRAINT device_commands_command_check CHECK (jsonb_typeof(command) = 'object'),
    status varchar(16) NOT NULL
        CONSTRAINT device_commands_status_check
        CHECK (status IN ('pending', 'published', 'acked', 'failed', 'timed_out')),
    ack jsonb,
    error_code varchar(64),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    CONSTRAINT device_commands_device_registry_id_session_id_key
        UNIQUE (device_registry_id, session_id)
);

CREATE TABLE device_events (
    id uuid CONSTRAINT device_events_pkey PRIMARY KEY,
    device_registry_id uuid NOT NULL
        CONSTRAINT device_events_device_registry_id_fkey REFERENCES device_registry(id),
    event_type varchar(64) NOT NULL,
    boot_id varchar(128) NOT NULL,
    event_seq bigint NOT NULL
        CONSTRAINT device_events_event_seq_check CHECK (event_seq >= 0),
    monotonic_ms bigint
        CONSTRAINT device_events_monotonic_ms_check
        CHECK (monotonic_ms IS NULL OR monotonic_ms >= 0),
    time_quality varchar(32),
    time_source varchar(32),
    occurred_at timestamptz,
    received_at timestamptz NOT NULL DEFAULT now(),
    data jsonb NOT NULL DEFAULT '{}'::jsonb
        CONSTRAINT device_events_data_check CHECK (jsonb_typeof(data) = 'object'),
    CONSTRAINT device_events_device_registry_boot_event_key
        UNIQUE (device_registry_id, boot_id, event_seq)
);

CREATE INDEX idx_device_events_history
    ON device_events (device_registry_id, event_type, received_at DESC, id DESC);

CREATE TABLE ota_firmware_versions (
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
    CONSTRAINT ota_firmware_versions_pid_version_key UNIQUE (pid, version)
);

CREATE UNIQUE INDEX uniq_ota_firmware_versions_app_pid_build
    ON ota_firmware_versions (pid, firmware_build)
    WHERE target_environment_id IS NULL;

CREATE TABLE device_ota_deployments (
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

CREATE UNIQUE INDEX uniq_device_ota_deployments_active
    ON device_ota_deployments (device_registry_id)
    WHERE state IN ('pending', 'accepted', 'downloading', 'verifying', 'installing');
CREATE INDEX idx_device_ota_deployments_history
    ON device_ota_deployments (device_registry_id, created_at DESC, id DESC);

CREATE TABLE device_schedules (
    device_registry_id uuid
        CONSTRAINT device_schedules_pkey PRIMARY KEY
        CONSTRAINT device_schedules_device_registry_id_fkey REFERENCES device_registry(id),
    schedules jsonb NOT NULL
        CONSTRAINT device_schedules_schedules_check CHECK (jsonb_typeof(schedules) = 'array'),
    confirmed_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE device_schedules IS
    '每台设备仅保存一份 Gateway 已确认 schedule 投影；device_registry_id 作为自然主键是一设备一行的显式例外。';

CREATE TABLE device_streams (
    id uuid CONSTRAINT device_streams_pkey PRIMARY KEY,
    device_registry_id uuid NOT NULL
        CONSTRAINT device_streams_device_registry_id_fkey REFERENCES device_registry(id),
    device_binding_id uuid NOT NULL
        CONSTRAINT device_streams_device_binding_id_fkey REFERENCES device_bindings(id),
    kvs_channel_arn text NOT NULL,
    status varchar(16) NOT NULL
        CONSTRAINT device_streams_status_check
        CHECK (status IN ('creating', 'ready', 'closing', 'closed', 'failed', 'expired')),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE device_snapshots (
    id uuid CONSTRAINT device_snapshots_pkey PRIMARY KEY,
    device_registry_id uuid NOT NULL
        CONSTRAINT device_snapshots_device_registry_id_fkey REFERENCES device_registry(id),
    device_binding_id uuid NOT NULL
        CONSTRAINT device_snapshots_device_binding_id_fkey REFERENCES device_bindings(id),
    object_key text,
    status varchar(16) NOT NULL
        CONSTRAINT device_snapshots_status_check
        CHECK (status IN ('pending', 'uploading', 'ready', 'failed', 'expired')),
    expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE device_recordings (
    id uuid CONSTRAINT device_recordings_pkey PRIMARY KEY,
    device_registry_id uuid NOT NULL
        CONSTRAINT device_recordings_device_registry_id_fkey REFERENCES device_registry(id),
    device_binding_id uuid NOT NULL
        CONSTRAINT device_recordings_device_binding_id_fkey REFERENCES device_bindings(id),
    kind varchar(16) NOT NULL
        CONSTRAINT device_recordings_kind_check CHECK (kind IN ('cloud', 'local')),
    object_key text,
    status varchar(16) NOT NULL
        CONSTRAINT device_recordings_status_check
        CHECK (status IN ('pending', 'recording', 'uploading', 'ready', 'failed', 'expired')),
    duration_seconds integer
        CONSTRAINT device_recordings_duration_seconds_check
        CHECK (duration_seconds IS NULL OR duration_seconds > 0),
    device_metadata jsonb NOT NULL DEFAULT '{}'::jsonb
        CONSTRAINT device_recordings_device_metadata_check
        CHECK (jsonb_typeof(device_metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_device_recordings_history
    ON device_recordings (device_registry_id, created_at DESC, id DESC);

CREATE TABLE webhook_outbox (
    id uuid CONSTRAINT webhook_outbox_pkey PRIMARY KEY,
    device_event_id uuid NOT NULL
        CONSTRAINT webhook_outbox_device_event_id_fkey
        REFERENCES device_events(id) ON DELETE CASCADE,
    internal_app_id varchar(64) NOT NULL,
    delivery_id varchar(64) NOT NULL CONSTRAINT webhook_outbox_delivery_id_key UNIQUE,
    event_type varchar(64) NOT NULL,
    payload jsonb NOT NULL
        CONSTRAINT webhook_outbox_payload_check CHECK (jsonb_typeof(payload) = 'object'),
    status varchar(16) NOT NULL
        CONSTRAINT webhook_outbox_status_check
        CHECK (status IN ('pending', 'queued', 'delivered', 'dead')),
    attempts integer NOT NULL DEFAULT 0
        CONSTRAINT webhook_outbox_attempts_check CHECK (attempts >= 0),
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE webhook_deliveries (
    id uuid CONSTRAINT webhook_deliveries_pkey PRIMARY KEY,
    webhook_outbox_id uuid NOT NULL
        CONSTRAINT webhook_deliveries_webhook_outbox_id_fkey
        REFERENCES webhook_outbox(id) ON DELETE CASCADE,
    attempt integer NOT NULL
        CONSTRAINT webhook_deliveries_attempt_check CHECK (attempt > 0),
    status_code integer,
    error_code varchar(64),
    attempted_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT webhook_deliveries_webhook_outbox_id_attempt_key
        UNIQUE (webhook_outbox_id, attempt)
);

CREATE TABLE device_lifecycle_outbox (
    id uuid CONSTRAINT device_lifecycle_outbox_pkey PRIMARY KEY,
    device_registry_id uuid NOT NULL
        CONSTRAINT device_lifecycle_outbox_device_registry_id_fkey REFERENCES device_registry(id),
    operation varchar(32) NOT NULL
        CONSTRAINT device_lifecycle_outbox_operation_check
        CHECK (operation IN ('revoke_certificate')),
    payload jsonb NOT NULL
        CONSTRAINT device_lifecycle_outbox_payload_check CHECK (jsonb_typeof(payload) = 'object'),
    status varchar(16) NOT NULL
        CONSTRAINT device_lifecycle_outbox_status_check
        CHECK (status IN ('pending', 'processing', 'done', 'dead')),
    attempts integer NOT NULL DEFAULT 0
        CONSTRAINT device_lifecycle_outbox_attempts_check CHECK (attempts >= 0),
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    last_error varchar(255),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_device_lifecycle_outbox_ready
    ON device_lifecycle_outbox (status, next_attempt_at)
    WHERE status IN ('pending', 'processing');

CREATE TABLE idempotency_records (
    id uuid CONSTRAINT idempotency_records_pkey PRIMARY KEY,
    internal_app_id varchar(64) NOT NULL,
    operation varchar(64) NOT NULL,
    idempotency_key varchar(128) NOT NULL,
    request_hash bytea NOT NULL
        CONSTRAINT idempotency_records_request_hash_check CHECK (octet_length(request_hash) = 32),
    resource_type varchar(64) NOT NULL,
    resource_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    CONSTRAINT idempotency_records_internal_app_operation_key
        UNIQUE (internal_app_id, operation, idempotency_key)
);

CREATE TABLE audit_logs (
    id uuid CONSTRAINT audit_logs_pkey PRIMARY KEY,
    internal_app_id varchar(64),
    operation varchar(96) NOT NULL,
    resource_type varchar(64),
    resource_id varchar(128),
    request_id varchar(128),
    outcome varchar(16) NOT NULL
        CONSTRAINT audit_logs_outcome_check
        CHECK (outcome IN ('accepted', 'succeeded', 'rejected', 'failed')),
    error_code varchar(64),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb
        CONSTRAINT audit_logs_metadata_check CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_audit_logs_app_time
    ON audit_logs (internal_app_id, created_at DESC);

CREATE OR REPLACE FUNCTION enforce_device_binding_user_invariant()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.binding_external_user_id IS DISTINCT FROM OLD.binding_external_user_id THEN
        RAISE EXCEPTION
            'device_bindings.binding_external_user_id is immutable once set (device_registry_id=%, old=%, new=%)',
            OLD.device_registry_id,
            OLD.binding_external_user_id,
            NEW.binding_external_user_id
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

COMMENT ON FUNCTION enforce_device_binding_user_invariant() IS
    '绑定用户一旦写入便不可静默覆盖；违反时返回 23514，解绑或设备移交必须走显式生命周期。';

CREATE TRIGGER trg_enforce_device_binding_user_invariant
    BEFORE UPDATE OF binding_external_user_id ON device_bindings
    FOR EACH ROW EXECUTE FUNCTION enforce_device_binding_user_invariant();

COMMENT ON TRIGGER trg_enforce_device_binding_user_invariant ON device_bindings IS
    '拒绝把 active/inactive binding 的 binding_external_user_id 改成另一个用户；同值幂等更新允许。';

CREATE TRIGGER trg_device_registry_updated_at
    BEFORE UPDATE ON device_registry
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_products_updated_at
    BEFORE UPDATE ON products
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_device_activations_updated_at
    BEFORE UPDATE ON device_activations
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_device_activation_sessions_updated_at
    BEFORE UPDATE ON device_activation_sessions
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_device_bindings_updated_at
    BEFORE UPDATE ON device_bindings
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_device_tokens_updated_at
    BEFORE UPDATE ON device_tokens
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_device_commands_updated_at
    BEFORE UPDATE ON device_commands
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_ota_firmware_versions_updated_at
    BEFORE UPDATE ON ota_firmware_versions
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_device_ota_deployments_updated_at
    BEFORE UPDATE ON device_ota_deployments
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_device_schedules_updated_at
    BEFORE UPDATE ON device_schedules
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_device_streams_updated_at
    BEFORE UPDATE ON device_streams
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_device_snapshots_updated_at
    BEFORE UPDATE ON device_snapshots
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_device_recordings_updated_at
    BEFORE UPDATE ON device_recordings
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_webhook_outbox_updated_at
    BEFORE UPDATE ON webhook_outbox
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_device_lifecycle_outbox_updated_at
    BEFORE UPDATE ON device_lifecycle_outbox
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX idx_products_dp_profile ON products (dp_profile_code);
CREATE INDEX idx_device_registry_dp_profile ON device_registry (dp_profile_id);
CREATE INDEX idx_device_activations_dp_profile ON device_activations (dp_profile_id);

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

CREATE TABLE device_local_recordings (
    id uuid CONSTRAINT device_local_recordings_pkey PRIMARY KEY,
    device_registry_id uuid NOT NULL
        CONSTRAINT device_local_recordings_device_registry_id_fkey
        REFERENCES device_registry(id) ON DELETE RESTRICT,
    source_event_id uuid
        CONSTRAINT device_local_recordings_source_event_id_fkey
        REFERENCES device_events(id) ON DELETE RESTRICT,
    recording_id varchar(36) NOT NULL,
    revision varchar(68) NOT NULL,
    conflicting_revision varchar(68),
    start_at timestamptz NOT NULL,
    end_at timestamptz NOT NULL,
    recording_date date NOT NULL,
    duration_ms bigint NOT NULL
        CONSTRAINT device_local_recordings_duration_ms_check CHECK (duration_ms >= 0),
    size_bytes bigint NOT NULL
        CONSTRAINT device_local_recordings_size_bytes_check CHECK (size_bytes >= 0),
    recording_type varchar(16) NOT NULL
        CONSTRAINT device_local_recordings_type_check
        CHECK (recording_type IN ('event', 'manual', 'continuous')),
    codec varchar(16) NOT NULL
        CONSTRAINT device_local_recordings_codec_check CHECK (codec = 'h264'),
    width integer NOT NULL CONSTRAINT device_local_recordings_width_check CHECK (width > 0),
    height integer NOT NULL CONSTRAINT device_local_recordings_height_check CHECK (height > 0),
    fps_num integer NOT NULL CONSTRAINT device_local_recordings_fps_num_check CHECK (fps_num > 0),
    fps_den integer NOT NULL CONSTRAINT device_local_recordings_fps_den_check CHECK (fps_den > 0),
    playable boolean NOT NULL DEFAULT true,
    seekable boolean NOT NULL,
    projection_status varchar(16) NOT NULL DEFAULT 'active'
        CONSTRAINT device_local_recordings_projection_status_check
        CHECK (projection_status IN ('active', 'conflict', 'deleted')),
    thumbnail_status varchar(16) NOT NULL DEFAULT 'pending'
        CONSTRAINT device_local_recordings_thumbnail_status_check
        CHECK (thumbnail_status IN ('pending', 'uploading', 'ready', 'failed')),
    thumbnail_etag varchar(71) NOT NULL,
    thumbnail_size_bytes bigint NOT NULL
        CONSTRAINT device_local_recordings_thumbnail_size_check CHECK (thumbnail_size_bytes > 0),
    thumbnail_object_key text,
    deleted_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT device_local_recordings_identity_key UNIQUE (device_registry_id, recording_id),
    CONSTRAINT device_local_recordings_time_check CHECK (end_at >= start_at),
    CONSTRAINT device_local_recordings_recording_id_check
        CHECK (recording_id ~ '^rec_[0-9a-f]{32}$'),
    CONSTRAINT device_local_recordings_revision_check
        CHECK (revision ~ '^rev_[0-9a-f]{64}$'),
    CONSTRAINT device_local_recordings_thumbnail_etag_check
        CHECK (thumbnail_etag ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT device_local_recordings_conflict_check
        CHECK ((projection_status = 'conflict') = (conflicting_revision IS NOT NULL)),
    CONSTRAINT device_local_recordings_deleted_check
        CHECK ((projection_status = 'deleted') = (deleted_at IS NOT NULL))
);

CREATE INDEX idx_device_local_recordings_history
    ON device_local_recordings (device_registry_id, recording_date, start_at DESC, recording_id DESC)
    WHERE projection_status = 'active';
CREATE INDEX idx_device_local_recordings_thumbnail_pending
    ON device_local_recordings (updated_at, id)
    WHERE projection_status = 'active' AND thumbnail_status IN ('pending', 'failed');

CREATE TABLE device_local_recording_tombstones (
    id uuid CONSTRAINT device_local_recording_tombstones_pkey PRIMARY KEY,
    device_registry_id uuid NOT NULL
        CONSTRAINT device_local_recording_tombstones_device_registry_id_fkey
        REFERENCES device_registry(id) ON DELETE RESTRICT,
    source_event_id uuid
        CONSTRAINT device_local_recording_tombstones_source_event_id_fkey
        REFERENCES device_events(id) ON DELETE RESTRICT,
    recording_id varchar(36) NOT NULL,
    revision varchar(68) NOT NULL,
    deleted_at timestamptz NOT NULL,
    CONSTRAINT device_local_recording_tombstones_identity_key
        UNIQUE (device_registry_id, recording_id),
    CONSTRAINT device_local_recording_tombstones_recording_id_check
        CHECK (recording_id ~ '^rec_[0-9a-f]{32}$'),
    CONSTRAINT device_local_recording_tombstones_revision_check
        CHECK (revision ~ '^rev_[0-9a-f]{64}$')
);

CREATE TABLE local_recording_cursors (
    token uuid CONSTRAINT local_recording_cursors_pkey PRIMARY KEY,
    device_binding_id uuid NOT NULL
        CONSTRAINT local_recording_cursors_device_binding_id_fkey
        REFERENCES device_bindings(id) ON DELETE CASCADE,
    device_registry_id uuid NOT NULL
        CONSTRAINT local_recording_cursors_device_registry_id_fkey
        REFERENCES device_registry(id) ON DELETE CASCADE,
    after_start_at timestamptz NOT NULL,
    after_recording_id varchar(36) NOT NULL,
    date_filter date,
    type_filter varchar(16)
        CONSTRAINT local_recording_cursors_type_filter_check
        CHECK (type_filter IS NULL OR type_filter IN ('event', 'manual', 'continuous')),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_local_recording_cursors_expiry
    ON local_recording_cursors (expires_at);

CREATE TABLE local_recording_reconciliations (
    device_registry_id uuid
        CONSTRAINT local_recording_reconciliations_pkey PRIMARY KEY
        CONSTRAINT local_recording_reconciliations_device_registry_id_fkey
        REFERENCES device_registry(id) ON DELETE CASCADE,
    generation uuid NOT NULL,
    started_at timestamptz NOT NULL DEFAULT now(),
    cursor varchar(36),
    status varchar(16) NOT NULL DEFAULT 'pending'
        CONSTRAINT local_recording_reconciliations_status_check
        CHECK (status IN ('pending', 'processing')),
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    last_error text,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT local_recording_reconciliations_cursor_check
        CHECK (cursor IS NULL OR cursor ~ '^rec_[0-9a-f]{32}$')
);
CREATE INDEX idx_local_recording_reconciliations_ready
    ON local_recording_reconciliations (next_attempt_at, device_registry_id);

CREATE TABLE local_recording_reconciliation_sightings (
    device_registry_id uuid NOT NULL
        CONSTRAINT local_recording_reconciliation_sightings_device_id_fkey
        REFERENCES device_registry(id) ON DELETE CASCADE,
    generation uuid NOT NULL,
    recording_id varchar(36) NOT NULL,
    CONSTRAINT local_recording_reconciliation_sightings_pkey
        PRIMARY KEY (device_registry_id, generation, recording_id),
    CONSTRAINT local_recording_reconciliation_sightings_recording_id_check
        CHECK (recording_id ~ '^rec_[0-9a-f]{32}$')
);

CREATE TABLE local_recording_thumbnail_outbox (
    id uuid CONSTRAINT local_recording_thumbnail_outbox_pkey PRIMARY KEY,
    local_recording_id uuid NOT NULL
        CONSTRAINT local_recording_thumbnail_outbox_recording_id_fkey
        REFERENCES device_local_recordings(id) ON DELETE CASCADE,
    status text NOT NULL DEFAULT 'pending'
        CONSTRAINT local_recording_thumbnail_outbox_status_check
        CHECK (status IN ('pending', 'processing', 'ready', 'dead', 'delete_pending', 'delete_processing', 'deleted')),
    attempts integer NOT NULL DEFAULT 0
        CONSTRAINT local_recording_thumbnail_outbox_attempts_check CHECK (attempts >= 0),
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT local_recording_thumbnail_outbox_recording_key UNIQUE (local_recording_id)
);
CREATE INDEX idx_local_recording_thumbnail_outbox_ready
    ON local_recording_thumbnail_outbox (next_attempt_at, id)
    WHERE status IN ('pending', 'processing', 'delete_pending', 'delete_processing');

CREATE TRIGGER trg_device_local_recordings_updated_at
    BEFORE UPDATE ON device_local_recordings
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER trg_local_recording_thumbnail_outbox_updated_at
    BEFORE UPDATE ON local_recording_thumbnail_outbox
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER trg_local_recording_reconciliations_updated_at
    BEFORE UPDATE ON local_recording_reconciliations
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

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
