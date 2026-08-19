package migrations

import (
	"io/fs"
	"testing"
)

func TestOnlySquashedInitialMigrationRemains(t *testing.T) {
	t.Parallel()

	entries, err := fs.ReadDir(Files, ".")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	if len(entries) != 1 || entries[0].IsDir() || entries[0].Name() != "001_initial.sql" {
		t.Fatalf("migration files = %v, want only 001_initial.sql at the migration root", migrationNames(entries))
	}
}
