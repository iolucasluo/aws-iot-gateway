package migrations

import (
	"strings"
	"testing"
)

func TestSingleActiveProjectWebhookMigrationIsNarrowAndAdditive(t *testing.T) {
	t.Parallel()
	migration := readMigration(t, "006_project_webhooks_single_active.sql")

	requireMigrationContains(t, migration, "CREATE UNIQUE INDEX IF NOT EXISTS uniq_project_webhooks_active")
	requireMigrationContains(t, migration, "ON project_webhooks (project_id) WHERE status = 'active'")
	for _, forbidden := range []string{
		"ALTER TABLE", "DROP ", "TRUNCATE", "DELETE FROM", "INSERT INTO", "UPDATE ",
	} {
		if strings.Contains(migration, forbidden) {
			t.Errorf("single-active webhook migration must stay additive, found %q", forbidden)
		}
	}
}
