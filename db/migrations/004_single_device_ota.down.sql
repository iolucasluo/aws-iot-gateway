-- The final OTA shape is also part of the squashed 001 baseline. Dropping it
-- blindly would corrupt a fresh database, so rollback requires a reviewed backup.
DO $$
BEGIN
    RAISE EXCEPTION '004_single_device_ota cannot be rolled back automatically; restore a reviewed database backup instead';
END;
$$;
