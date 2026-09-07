ALTER TABLE ota_firmware_versions
    DROP CONSTRAINT IF EXISTS ota_firmware_versions_pid_build_key;

CREATE UNIQUE INDEX IF NOT EXISTS uniq_ota_firmware_versions_app_pid_build
    ON ota_firmware_versions (pid, firmware_build)
    WHERE target_environment_id IS NULL;
