package migrations

import (
	"fmt"
	"io/fs"
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
	}
	if got := migrationNames(entries); len(got) != len(want) || fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("migration files = %v, want %v", got, want)
	}
}
