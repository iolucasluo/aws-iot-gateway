-- Internal AWS Gateway initial schema.
-- This is a pre-production squash of the former 001-004 migrations. It keeps
-- only the narrow activation, binding, device runtime and OpenAPI operation
-- domain while following the database conventions inherited from explorex-api.

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

CREATE TABLE device_registry (
    id uuid CONSTRAINT device_registry_pkey PRIMARY KEY,
    device_sn varchar(64) NOT NULL CONSTRAINT device_registry_device_sn_key UNIQUE,
    thing_name varchar(128) NOT NULL CONSTRAINT device_registry_thing_name_key UNIQUE,
    pid varchar(64) NOT NULL DEFAULT 'feeder-x1'
        CONSTRAINT device_registry_pid_check CHECK (pid = 'feeder-x1'),
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
    CONSTRAINT device_registry_thing_name_check CHECK (thing_name = device_sn)
);

CREATE TABLE device_activations (
    id uuid CONSTRAINT device_activations_pkey PRIMARY KEY,
    internal_app_id varchar(64) NOT NULL,
    binding_external_user_id varchar(255) NOT NULL,
    method varchar(8) NOT NULL
        CONSTRAINT device_activations_method_check CHECK (method IN ('qr', 'ap', 'ble')),
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
        UNIQUE (internal_app_id, idempotency_key)
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
