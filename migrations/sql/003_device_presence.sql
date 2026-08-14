ALTER TABLE devices
    ADD COLUMN presence_online boolean NOT NULL DEFAULT false,
    ADD COLUMN presence_updated_at timestamptz,
    ADD COLUMN last_seen_at timestamptz;
