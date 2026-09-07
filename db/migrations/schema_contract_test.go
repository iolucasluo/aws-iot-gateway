package migrations

import (
	"io/fs"
	"strings"
	"testing"
)

func TestInitialSchemaFollowsExplorexDatabaseConventionsWithoutPlatformDomain(t *testing.T) {
	t.Parallel()
	initial := readMigration(t, "001_initial.sql")

	for _, table := range []string{
		"products",
		"device_registry",
		"device_activations",
		"device_activation_sessions",
		"device_bindings",
		"device_tokens",
		"device_commands",
		"device_events",
		"device_schedules",
		"device_streams",
		"device_snapshots",
		"device_recordings",
		"webhook_outbox",
		"webhook_deliveries",
		"device_lifecycle_outbox",
		"idempotency_records",
		"audit_logs",
		"ota_firmware_versions",
		"device_ota_deployments",
	} {
		requireMigrationContains(t, initial, "CREATE TABLE "+table+" (")
	}

	for _, indexName := range []string{
		"idx_device_activations_owner_status",
		"uniq_device_bindings_active_device",
		"idx_device_bindings_owner",
		"idx_device_tokens_binding_active",
		"idx_device_events_history",
		"idx_device_recordings_history",
		"idx_audit_logs_app_time",
		"idx_device_lifecycle_outbox_ready",
		"uniq_device_ota_deployments_active",
		"idx_device_ota_deployments_history",
	} {
		requireMigrationContains(t, initial, "INDEX "+indexName)
	}

	for _, table := range []string{
		"products",
		"device_registry",
		"device_activations",
		"device_activation_sessions",
		"device_bindings",
		"device_tokens",
		"device_commands",
		"device_schedules",
		"device_streams",
		"device_snapshots",
		"device_recordings",
		"webhook_outbox",
		"device_lifecycle_outbox",
		"ota_firmware_versions",
		"device_ota_deployments",
	} {
		requireMigrationContains(t, initial, "CREATE TRIGGER trg_"+table+"_updated_at")
	}

	for _, fragment := range []string{
		"CREATE OR REPLACE FUNCTION set_updated_at()",
		"CREATE OR REPLACE FUNCTION enforce_device_binding_user_invariant()",
		"binding_external_user_id is immutable once set",
		"USING ERRCODE = '23514'",
		"CREATE TRIGGER trg_enforce_device_binding_user_invariant",
		"device_registry_id uuid",
		"device_binding_id uuid",
		"presence_online boolean NOT NULL DEFAULT false",
		"CONSTRAINT device_registry_pid_fkey FOREIGN KEY (pid) REFERENCES products(pid)",
		"CONSTRAINT device_activations_pid_fkey FOREIGN KEY (pid) REFERENCES products(pid)",
		"capabilities jsonb NOT NULL DEFAULT '[]'::jsonb",
		"CONSTRAINT products_capabilities_check",
		"updated_at timestamptz NOT NULL DEFAULT now()",
		"COMMENT ON TABLE device_schedules",
	} {
		requireMigrationContains(t, initial, fragment)
	}

	for _, forbidden := range []string{
		"INSERT INTO products",
		"CREATE TABLE devices (",
		"CREATE TABLE activations (",
		"CREATE TABLE bindings (",
		"CREATE TABLE projects (",
		"developer_accounts",
		"factory_batches",
		"idevid",
		"rma",
		"ota_campaigns",
		"ALTER TABLE ",
	} {
		if strings.Contains(strings.ToLower(initial), strings.ToLower(forbidden)) {
			t.Errorf("squashed initial schema contains forbidden legacy/platform fragment %q", forbidden)
		}
	}
}

func TestSingleDeviceOTAMigrationHasExecutionRecordsWithoutCampaignPlatform(t *testing.T) {
	t.Parallel()
	migration := readMigration(t, "001_initial.sql")
	for _, fragment := range []string{
		"CREATE TABLE ota_firmware_versions",
		"CREATE TABLE device_ota_deployments",
		"uniq_device_ota_deployments_active",
		"firmware_version_id uuid NOT NULL",
	} {
		requireMigrationContains(t, migration, fragment)
	}
	for _, forbidden := range []string{"ota_campaigns", "aws_iot_jobs", "tenant_id", "project_id"} {
		if strings.Contains(strings.ToLower(migration), forbidden) {
			t.Errorf("single-device OTA migration contains platform fragment %q", forbidden)
		}
	}
}

func migrationNames(entries []fs.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func readMigration(t *testing.T, name string) string {
	t.Helper()
	content, err := Files.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(content)
}

func requireMigrationContains(t *testing.T, migration, fragment string) {
	t.Helper()
	if !strings.Contains(migration, fragment) {
		t.Errorf("migration missing %q", fragment)
	}
}

func TestLocalRecordingProjectionMigrationIsNarrowAndFailClosed(t *testing.T) {
	t.Parallel()
	migration := readMigration(t, "001_initial.sql")
	for _, fragment := range []string{
		"CREATE TABLE device_local_recordings (",
		"device_local_recordings_identity_key",
		"recording_id ~ '^rec_[0-9a-f]{32}$'",
		"revision ~ '^rev_[0-9a-f]{64}$'",
		"projection_status IN ('active', 'conflict', 'deleted')",
		"recording_date date NOT NULL",
		"thumbnail_status IN ('pending', 'uploading', 'ready', 'failed')",
		"CREATE TABLE device_local_recording_tombstones (",
		"CREATE TABLE local_recording_cursors (",
		"CREATE TABLE local_recording_reconciliations (",
		"started_at timestamptz NOT NULL DEFAULT now()",
		"CREATE TABLE local_recording_reconciliation_sightings (",
		"CREATE TABLE local_recording_thumbnail_outbox (",
		"idx_local_recording_thumbnail_outbox_ready",
		"'delete_pending', 'delete_processing', 'deleted'",
		"ON DELETE CASCADE",
	} {
		requireMigrationContains(t, migration, fragment)
	}
	for _, forbidden := range []string{
		"tenant_id", "product_id", "factory_id", "public_url", "video_object_key",
	} {
		if strings.Contains(strings.ToLower(migration), forbidden) {
			t.Errorf("local recording migration contains forbidden platform/upload field %q", forbidden)
		}
	}
}
