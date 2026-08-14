CREATE TABLE device_lifecycle_outbox (
    id uuid PRIMARY KEY,
    device_id uuid NOT NULL REFERENCES devices(id),
    operation varchar(32) NOT NULL CHECK (operation IN ('revoke_certificate')),
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    status varchar(16) NOT NULL CHECK (status IN ('pending', 'processing', 'done', 'dead')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    last_error varchar(255),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX device_lifecycle_outbox_ready_idx
    ON device_lifecycle_outbox (status, next_attempt_at)
    WHERE status IN ('pending', 'processing');
