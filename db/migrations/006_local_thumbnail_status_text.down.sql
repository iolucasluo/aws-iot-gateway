UPDATE local_recording_thumbnail_outbox
SET status='delete_pending'
WHERE status='delete_processing';

ALTER TABLE local_recording_thumbnail_outbox
    ALTER COLUMN status TYPE varchar(16);
