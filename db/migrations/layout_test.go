package migrations

import (
	"fmt"
	"io/fs"
	"strings"
	"testing"
)

func TestMigrationLayoutKeepsInitialAndForwardCompatibilityPair(t *testing.T) {
	t.Parallel()

	entries, err := fs.ReadDir(Files, ".")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	want := []string{
		"001_initial.sql",
		"002_legacy_product_catalog.down.sql",
		"002_legacy_product_catalog.up.sql",
		"003_product_capabilities.down.sql",
		"003_product_capabilities.up.sql",
		"004_single_device_ota.down.sql",
		"004_single_device_ota.up.sql",
		"005_local_recording_projection.down.sql",
		"005_local_recording_projection.up.sql",
		"006_local_thumbnail_status_text.down.sql",
		"006_local_thumbnail_status_text.up.sql",
		"007_product_dps.down.sql",
		"007_product_dps.up.sql",
		"008_ota_environment_metadata.down.sql",
		"008_ota_environment_metadata.up.sql",
		"008_provider_composition.down.sql",
		"008_provider_composition.up.sql",
		"009_environment_ota_non_monotonic.down.sql",
		"009_environment_ota_non_monotonic.up.sql",
		"010_dp_profiles.down.sql",
		"010_dp_profiles.up.sql",
	}
	if got := migrationNames(entries); len(got) != len(want) || fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("migration files = %v, want %v", got, want)
	}
}

func TestProviderCompositionMigrationDefinesBindingScopedProviderFacts(t *testing.T) {
	t.Parallel()

	migration := readMigration(t, "008_provider_composition.up.sql")
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

	down := readMigration(t, "008_provider_composition.down.sql")
	for _, fragment := range []string{"DROP TRIGGER", "DROP INDEX", "DROP TABLE device_component_bindings"} {
		requireMigrationContains(t, down, fragment)
	}
}

func TestProductDPMigrationDefinesGatewayOwnedContractCatalog(t *testing.T) {
	t.Parallel()

	content, err := Files.ReadFile("007_product_dps.up.sql")
	if err != nil {
		t.Fatalf("read product DP migration: %v", err)
	}
	migration := string(content)
	for _, fragment := range []string{
		"CREATE TABLE product_dps",
		"product_dps_product_id_fkey REFERENCES products(id) ON DELETE RESTRICT",
		"UNIQUE (product_id, code)",
		"CREATE INDEX idx_product_dps_product_status",
		"CREATE TRIGGER trg_product_dps_updated_at",
		"COMMENT ON TRIGGER trg_product_dps_updated_at",
	} {
		if !strings.Contains(migration, fragment) {
			t.Errorf("product DP migration missing %q", fragment)
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
