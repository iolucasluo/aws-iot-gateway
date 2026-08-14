CREATE TABLE devices (
    id uuid PRIMARY KEY,
    device_sn varchar(64) NOT NULL UNIQUE,
    thing_name varchar(128) NOT NULL UNIQUE,
    pid varchar(64) NOT NULL DEFAULT 'feeder-x1' CHECK (pid = 'feeder-x1'),
    certificate_id varchar(128),
    certificate_arn text,
    aws_iot_endpoint text,
    status varchar(24) NOT NULL CHECK (status IN ('provisioning', 'active', 'revoked', 'reset')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (thing_name = device_sn)
);

CREATE TABLE activations (
    id uuid PRIMARY KEY,
    internal_app_id varchar(64) NOT NULL,
    binding_external_user_id varchar(255) NOT NULL,
    method varchar(8) NOT NULL CHECK (method IN ('qr', 'ap', 'ble')),
    status varchar(24) NOT NULL CHECK (status IN (
        'pending', 'activating', 'installing', 'finalizing',
        'activated', 'failed', 'expired', 'cancelled'
    )),
    code_hash bytea NOT NULL CHECK (octet_length(code_hash) = 32),
    idempotency_key varchar(128) NOT NULL,
    request_hash bytea NOT NULL CHECK (octet_length(request_hash) = 32),
    device_id uuid REFERENCES devices(id),
    failure_code varchar(64),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (internal_app_id, idempotency_key)
);

CREATE INDEX activations_owner_status_idx
    ON activations (internal_app_id, binding_external_user_id, status);

CREATE TABLE activation_sessions (
    id uuid PRIMARY KEY,
    activation_id uuid NOT NULL REFERENCES activations(id) ON DELETE CASCADE,
    session_external_id varchar(255) NOT NULL,
    method varchar(8) NOT NULL CHECK (method IN ('qr', 'ap', 'ble')),
    status varchar(24) NOT NULL CHECK (status IN ('pending', 'prepared', 'confirmed', 'failed', 'expired')),
    csr_pem text,
    csr_sha256 bytea CHECK (csr_sha256 IS NULL OR octet_length(csr_sha256) = 32),
    installation_nonce varchar(128),
    certificate_pem text,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (activation_id, session_external_id)
);

CREATE TABLE bindings (
    id uuid PRIMARY KEY,
    device_id uuid NOT NULL REFERENCES devices(id),
    internal_app_id varchar(64) NOT NULL,
    binding_external_user_id varchar(255) NOT NULL,
    status varchar(16) NOT NULL CHECK (status IN ('active', 'inactive')),
    activated_at timestamptz NOT NULL DEFAULT now(),
    released_at timestamptz,
    release_reason varchar(64),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((status = 'active' AND released_at IS NULL) OR status = 'inactive')
);

CREATE UNIQUE INDEX one_active_binding_per_device
    ON bindings (device_id) WHERE status = 'active';
CREATE INDEX bindings_owner_idx
    ON bindings (internal_app_id, binding_external_user_id, status);

CREATE TABLE device_tokens (
    id uuid PRIMARY KEY,
    binding_id uuid NOT NULL REFERENCES bindings(id),
    token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX device_tokens_binding_active_idx
    ON device_tokens (binding_id, expires_at) WHERE revoked_at IS NULL;

CREATE TABLE commands (
    id uuid PRIMARY KEY,
    device_id uuid NOT NULL REFERENCES devices(id),
    internal_app_id varchar(64) NOT NULL,
    session_id varchar(128) NOT NULL,
    request_id varchar(128),
    command jsonb NOT NULL CHECK (jsonb_typeof(command) = 'object'),
    status varchar(16) NOT NULL CHECK (status IN ('pending', 'published', 'acked', 'failed', 'timed_out')),
    ack jsonb,
    error_code varchar(64),
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    UNIQUE (device_id, session_id)
);

CREATE TABLE device_events (
    id uuid PRIMARY KEY,
    device_id uuid NOT NULL REFERENCES devices(id),
    event_type varchar(64) NOT NULL,
    boot_id varchar(128) NOT NULL,
    event_seq bigint NOT NULL CHECK (event_seq >= 0),
    monotonic_ms bigint CHECK (monotonic_ms IS NULL OR monotonic_ms >= 0),
    time_quality varchar(32),
    time_source varchar(32),
    occurred_at timestamptz,
    received_at timestamptz NOT NULL DEFAULT now(),
    data jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(data) = 'object'),
    UNIQUE (device_id, boot_id, event_seq)
);

CREATE INDEX device_events_history_idx
    ON device_events (device_id, event_type, received_at DESC, id DESC);

CREATE TABLE schedules (
    device_id uuid PRIMARY KEY REFERENCES devices(id),
    schedules jsonb NOT NULL CHECK (jsonb_typeof(schedules) = 'array'),
    confirmed_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE streams (
    id uuid PRIMARY KEY,
    device_id uuid NOT NULL REFERENCES devices(id),
    binding_id uuid NOT NULL REFERENCES bindings(id),
    kvs_channel_arn text NOT NULL,
    status varchar(16) NOT NULL CHECK (status IN ('creating', 'ready', 'closing', 'closed', 'failed', 'expired')),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE snapshots (
    id uuid PRIMARY KEY,
    device_id uuid NOT NULL REFERENCES devices(id),
    binding_id uuid NOT NULL REFERENCES bindings(id),
    object_key text,
    status varchar(16) NOT NULL CHECK (status IN ('pending', 'uploading', 'ready', 'failed', 'expired')),
    expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE recordings (
    id uuid PRIMARY KEY,
    device_id uuid NOT NULL REFERENCES devices(id),
    binding_id uuid NOT NULL REFERENCES bindings(id),
    kind varchar(16) NOT NULL CHECK (kind IN ('cloud', 'local')),
    object_key text,
    status varchar(16) NOT NULL CHECK (status IN ('pending', 'recording', 'uploading', 'ready', 'failed', 'expired')),
    duration_seconds integer CHECK (duration_seconds IS NULL OR duration_seconds > 0),
    device_metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(device_metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX recordings_device_history_idx
    ON recordings (device_id, created_at DESC, id DESC);

CREATE TABLE webhook_outbox (
    id uuid PRIMARY KEY,
    device_event_id uuid NOT NULL REFERENCES device_events(id) ON DELETE CASCADE,
    internal_app_id varchar(64) NOT NULL,
    delivery_id varchar(64) NOT NULL UNIQUE,
    event_type varchar(64) NOT NULL,
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    status varchar(16) NOT NULL CHECK (status IN ('pending', 'queued', 'delivered', 'dead')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE webhook_deliveries (
    id uuid PRIMARY KEY,
    outbox_id uuid NOT NULL REFERENCES webhook_outbox(id) ON DELETE CASCADE,
    attempt integer NOT NULL CHECK (attempt > 0),
    status_code integer,
    error_code varchar(64),
    attempted_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (outbox_id, attempt)
);

CREATE TABLE idempotency_records (
    id uuid PRIMARY KEY,
    internal_app_id varchar(64) NOT NULL,
    operation varchar(64) NOT NULL,
    idempotency_key varchar(128) NOT NULL,
    request_hash bytea NOT NULL CHECK (octet_length(request_hash) = 32),
    resource_type varchar(64) NOT NULL,
    resource_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    UNIQUE (internal_app_id, operation, idempotency_key)
);

CREATE TABLE audit_logs (
    id uuid PRIMARY KEY,
    internal_app_id varchar(64),
    operation varchar(96) NOT NULL,
    resource_type varchar(64),
    resource_id varchar(128),
    request_id varchar(128),
    outcome varchar(16) NOT NULL CHECK (outcome IN ('accepted', 'succeeded', 'rejected', 'failed')),
    error_code varchar(64),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX audit_logs_app_time_idx
    ON audit_logs (internal_app_id, created_at DESC);
