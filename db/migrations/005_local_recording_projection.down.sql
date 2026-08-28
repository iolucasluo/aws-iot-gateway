DROP TRIGGER IF EXISTS trg_local_recording_reconciliations_updated_at
    ON local_recording_reconciliations;
DROP TRIGGER IF EXISTS trg_local_recording_thumbnail_outbox_updated_at
    ON local_recording_thumbnail_outbox;
DROP TRIGGER IF EXISTS trg_device_local_recordings_updated_at
    ON device_local_recordings;
DROP TABLE IF EXISTS local_recording_thumbnail_outbox;
DROP TABLE IF EXISTS local_recording_reconciliation_sightings;
DROP TABLE IF EXISTS local_recording_reconciliations;
DROP TABLE IF EXISTS local_recording_cursors;
DROP TABLE IF EXISTS device_local_recording_tombstones;
DROP TABLE IF EXISTS device_local_recordings;
