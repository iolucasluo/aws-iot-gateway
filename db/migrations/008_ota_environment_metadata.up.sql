ALTER TABLE ota_firmware_versions
    ADD COLUMN IF NOT EXISTS target_environment_id varchar(64),
    ADD COLUMN IF NOT EXISTS target_environment_code varchar(2);

ALTER TABLE ota_firmware_versions
    DROP CONSTRAINT IF EXISTS ota_firmware_versions_target_environment_pair_check,
    DROP CONSTRAINT IF EXISTS ota_firmware_versions_target_environment_id_check,
    DROP CONSTRAINT IF EXISTS ota_firmware_versions_target_environment_code_check;

ALTER TABLE ota_firmware_versions
    ADD CONSTRAINT ota_firmware_versions_target_environment_pair_check
        CHECK ((target_environment_id IS NULL) = (target_environment_code IS NULL)),
    ADD CONSTRAINT ota_firmware_versions_target_environment_id_check
        CHECK (target_environment_id IS NULL OR target_environment_id ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$'),
    ADD CONSTRAINT ota_firmware_versions_target_environment_code_check
        CHECK (target_environment_code IS NULL OR target_environment_code ~ '^(0[1-9]|[1-9][0-9])$');
