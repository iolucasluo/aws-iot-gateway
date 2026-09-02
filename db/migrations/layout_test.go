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
		"009_environment_ota_non_monotonic.down.sql",
		"009_environment_ota_non_monotonic.up.sql",
	}
	if got := migrationNames(entries); len(got) != len(want) || fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("migration files = %v, want %v", got, want)
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
