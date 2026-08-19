package migrations

import (
	"io/fs"
	"strings"
	"testing"
)

func TestOnlySquashedInitialMigrationRemains(t *testing.T) {
	t.Parallel()

	entries, err := fs.ReadDir(Files, "sql")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	if len(entries) != 1 || entries[0].IsDir() || entries[0].Name() != "001_initial.sql" {
		t.Fatalf("migration files = %v, want only 001_initial.sql", migrationNames(entries))
	}
}

func TestInitialSchemaFollowsExplorexDatabaseConventionsWithoutPlatformDomain(t *testing.T) {
	t.Parallel()
	initial := readMigration(t, "sql/001_initial.sql")

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
		"updated_at timestamptz NOT NULL DEFAULT now()",
		"COMMENT ON TABLE device_schedules",
	} {
		requireMigrationContains(t, initial, fragment)
	}

	for _, forbidden := range []string{
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
