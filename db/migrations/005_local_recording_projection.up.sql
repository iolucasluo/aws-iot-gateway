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
    status varchar(16) NOT NULL DEFAULT 'pending'
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
