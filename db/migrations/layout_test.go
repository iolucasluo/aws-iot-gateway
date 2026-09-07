package migrations

import (
	"fmt"
	"io/fs"
	"strings"
	"testing"
)

func TestMigrationLayoutContainsOnlyCurrentInitialSchema(t *testing.T) {
	t.Parallel()

	entries, err := fs.ReadDir(Files, ".")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	want := []string{"001_initial.sql"}
	if got := migrationNames(entries); len(got) != len(want) || fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("migration files = %v, want %v", got, want)
	}
}

func TestProviderCompositionMigrationDefinesBindingScopedProviderFacts(t *testing.T) {
	t.Parallel()

	migration := readMigration(t, "001_initial.sql")
	for _, fragment := range []string{
		"CREATE TABLE device_component_bindings",
		"device_component_bindings_device_registry_id_fkey",
		"device_component_bindings_device_binding_id_fkey",
		"uniq_device_component_bindings_active",
		"device_component_bindings_capability_check",
		"device_component_bindings_status_check",
		"CREATE TRIGGER trg_device_component_bindings_updated_at",
		"COMMENT ON TRIGGER trg_device_component_bindings_updated_at",
	} {
		requireMigrationContains(t, migration, fragment)
	}
	for _, forbidden := range []string{"tenant_id", "project_id", "factory_id", "DROP TABLE", "TRUNCATE"} {
		if strings.Contains(strings.ToLower(migration), strings.ToLower(forbidden)) {
			t.Errorf("provider composition migration contains forbidden fragment %q", forbidden)
		}
	}

}

func TestMigrationsDoNotPublishNewRuntimeDPProfiles(t *testing.T) {
	entries, err := fs.ReadDir(Files, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		content, err := Files.ReadFile(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(content), "'feeder-v1'") || strings.Contains(string(content), "'camera-v1'") {
			t.Fatalf("%s publishes initial DP data; put it in seeds", entry.Name())
		}
	}
}

func TestInitialSchemaDefinesProductSelectedDPProfiles(t *testing.T) {
	initial := readMigration(t, "001_initial.sql")
	for _, fragment := range []string{"CREATE TABLE dp_profiles", "CREATE TABLE dp_definitions", "CREATE TABLE dp_profile_members", "products_dp_profile_code_fkey", "dp_profile_members_dp_profile_code_fkey", "UNIQUE (dp_profile_code, code)", "device_registry_dp_profile_id_fkey", "device_activations_dp_profile_id_fkey"} {
		requireMigrationContains(t, initial, fragment)
	}
	for _, forbidden := range []string{"product_dps", "legacy-", "INSERT INTO", "ALTER TABLE", "DROP TABLE", "IF NOT EXISTS"} {
		if strings.Contains(initial, forbidden) {
			t.Errorf("initial schema contains historical/data fragment %q", forbidden)
		}
	}
}
