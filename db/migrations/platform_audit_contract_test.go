package migrations

import (
	"strings"
	"testing"
)

func platformAuditMigration(t *testing.T) string {
	t.Helper()
	return readMigration(t, "005_platform_audit_webhooks.sql")
}

// 审计主体列与项目 webhook 必须一次表达完整，且审计表保持 append-only。
func TestPlatformAuditMigrationAddsActorColumnsAndProjectWebhooks(t *testing.T) {
	t.Parallel()
	migration := platformAuditMigration(t)

	for _, fragment := range []string{
		"ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS actor_kind varchar(24)",
		"ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS actor_account_id uuid",
		"ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS actor_label varchar(320)",
		"audit_logs_actor_kind_check",
		"actor_kind IN ('portal_account', 'internal_app', 'system')",
		"CREATE TABLE IF NOT EXISTS project_webhooks (",
		"project_webhooks_project_id_fkey REFERENCES projects(id) ON DELETE CASCADE",
		"CHECK (callback_url LIKE 'https://%')",
		"octet_length(secret_ciphertext) > 16",
		"status IN ('active', 'disabled')",
	} {
		requireMigrationContains(t, migration, fragment)
	}
	for _, indexName := range []string{"idx_audit_logs_actor_time", "idx_project_webhooks_project"} {
		requireMigrationContains(t, migration, "INDEX IF NOT EXISTS "+indexName)
	}
	requireMigrationContains(t, migration, "CREATE TRIGGER trg_project_webhooks_updated_at")
	requireMigrationContains(t, migration, "DROP TRIGGER IF EXISTS trg_project_webhooks_updated_at")
	// 审计表是 append-only 事实：不得被加上 updated_at 或触发器。
	for _, forbidden := range []string{"audit_logs ADD COLUMN IF NOT EXISTS updated_at", "trg_audit_logs_updated_at"} {
		if strings.Contains(migration, forbidden) {
			t.Errorf("audit_logs must stay append-only, found %q", forbidden)
		}
	}
}

func TestPlatformAuditMigrationIsAdditiveAndIdempotent(t *testing.T) {
	t.Parallel()
	migration := platformAuditMigration(t)

	for _, forbidden := range []string{
		"DROP TABLE",
		"DROP COLUMN",
		"DROP CONSTRAINT",
		"TRUNCATE",
		"DELETE FROM",
		"INSERT INTO",
		"ALTER COLUMN",
		"RENAME",
		"SET NOT NULL",
		"CREATE TABLE devices",
		"CREATE TABLE device_bindings",
		"tenant_id",
	} {
		if strings.Contains(migration, forbidden) {
			t.Errorf("platform audit migration must stay additive and narrow, found %q", forbidden)
		}
	}
}
