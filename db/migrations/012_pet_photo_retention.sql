-- Capture time is optional for backward-compatible old devices; new PET firmware writes it.
ALTER TABLE device_pet_photos ADD COLUMN IF NOT EXISTS captured_at timestamptz;
-- Seven-day deduplication tombstones follow completed 24-hour private-object cleanup.
-- A partial index bounds the purge scan without changing live transaction rows.
CREATE INDEX IF NOT EXISTS idx_device_pet_photos_retention
    ON device_pet_photos (updated_at, id)
    WHERE status IN ('expired', 'dead');

-- Limit checks use the server receipt clock and only accepted (not cancelled) captures.
CREATE INDEX IF NOT EXISTS idx_device_pet_photos_rate_window
    ON device_pet_photos (device_registry_id, created_at DESC)
    WHERE status <> 'expiring';
