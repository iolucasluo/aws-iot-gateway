ALTER TABLE ota_firmware_versions
    DROP CONSTRAINT IF EXISTS ota_firmware_versions_target_environment_pair_check,
    DROP CONSTRAINT IF EXISTS ota_firmware_versions_target_environment_id_check,
    DROP CONSTRAINT IF EXISTS ota_firmware_versions_target_environment_code_check,
    DROP COLUMN IF EXISTS target_environment_id,
    DROP COLUMN IF EXISTS target_environment_code;
