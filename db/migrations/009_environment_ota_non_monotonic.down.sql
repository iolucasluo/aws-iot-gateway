DROP INDEX IF EXISTS uniq_ota_firmware_versions_app_pid_build;

ALTER TABLE ota_firmware_versions
    ADD CONSTRAINT ota_firmware_versions_pid_build_key UNIQUE (pid, firmware_build);
