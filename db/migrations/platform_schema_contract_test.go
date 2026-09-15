package migrations

import (
	"regexp"
	"strings"
	"testing"
)

func platformMigration(t *testing.T) string {
	t.Helper()
	return readMigration(t, "003_platform_accounts.sql")
}

// 平台面账号域必须自洽：账号、会话、一次性 token 与它们依赖的 manufacturers 都在 003，
// 且 kind/role/manufacturer_id 的组合、token hash 长度和 email 规范化由约束表达。
func TestPlatformAccountsMigrationDefinesAccountsSessionsAndSetupTokens(t *testing.T) {
	t.Parallel()
	migration := platformMigration(t)

	for _, fragment := range []string{
		"CREATE TABLE IF NOT EXISTS manufacturers (",
		"CREATE TABLE IF NOT EXISTS portal_accounts (",
		"CREATE TABLE IF NOT EXISTS portal_sessions (",
		"CREATE TABLE IF NOT EXISTS portal_account_setup_tokens (",
		"kind IN ('staff', 'developer', 'manufacturer')",
		"role IN ('super_admin')",
		"role IN ('developer')",
		"role IN ('admin', 'operator')",
		"manufacturer_id IS NOT NULL",
		"audience IN ('admin', 'developer', 'manufacturer')",
		"purpose IN ('invite', 'password_reset')",
		"octet_length(token_hash) = 32",
		"email = lower(btrim(email))",
		"password_hash IS NULL OR btrim(password_hash) <> ''",
		"portal_accounts_manufacturer_id_fkey REFERENCES manufacturers(id) ON DELETE RESTRICT",
		"portal_sessions_account_id_fkey REFERENCES portal_accounts(id) ON DELETE CASCADE",
	} {
		requireMigrationContains(t, migration, fragment)
	}

	for _, indexName := range []string{
		"idx_portal_accounts_kind_status",
		"idx_portal_accounts_manufacturer",
		"idx_portal_sessions_account_active",
		"idx_portal_account_setup_tokens_account",
	} {
		requireMigrationContains(t, migration, "INDEX IF NOT EXISTS "+indexName)
	}

	for _, trigger := range []string{
		"trg_manufacturers_updated_at",
		"trg_portal_accounts_updated_at",
		"trg_portal_sessions_updated_at",
		"trg_portal_account_setup_tokens_updated_at",
	} {
		requireMigrationContains(t, migration, "CREATE TRIGGER "+trigger)
		requireMigrationContains(t, migration, "DROP TRIGGER IF EXISTS "+trigger)
	}
	requireMigrationContains(t, migration, "EXECUTE FUNCTION set_updated_at()")
}

// 平台面不得成为第二套设备域，也不得把 tenant / IDevID / RMA / OTA campaign 带回 schema。
func TestPlatformAccountsMigrationStaysOutOfDeviceAndNonGoalDomains(t *testing.T) {
	t.Parallel()
	migration := strings.ToLower(platformMigration(t))

	for _, forbidden := range []string{
		"create table devices",
		"create table device (",
		"create table bindings",
		"create table device_tokens",
		"create table device_certificates",
		"create table device_registry",
		"allowed_sn_prefixes",
	} {
		if strings.Contains(migration, forbidden) {
			t.Errorf("platform accounts migration must not introduce %q", forbidden)
		}
	}

	for _, word := range []string{"tenant_id", "idevid", "rma", "ota_campaigns", "product_dps"} {
		pattern := regexp.MustCompile("\\b" + word + "\\b")
		if pattern.MatchString(migration) {
			t.Errorf("platform accounts migration must not introduce the non-goal identifier %q", word)
		}
	}
}

// 增量迁移必须是纯 additive 且可重复执行：不得改既有结构，也不得写业务数据。
func TestPlatformAccountsMigrationIsAdditiveAndIdempotent(t *testing.T) {
	t.Parallel()
	migration := platformMigration(t)

	for _, forbidden := range []string{
		"ALTER TABLE ",
		"DROP TABLE",
		"DROP COLUMN",
		"DROP CONSTRAINT",
		"TRUNCATE",
		"DELETE FROM",
		"INSERT INTO",
	} {
		if strings.Contains(migration, forbidden) {
			t.Errorf("platform accounts migration must stay additive, found %q", forbidden)
		}
	}
}
