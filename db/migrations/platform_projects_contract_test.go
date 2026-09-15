package migrations

import (
	"regexp"
	"strings"
	"testing"
)

func platformProjectsMigration(t *testing.T) string {
	t.Helper()
	return readMigration(t, "004_platform_projects.sql")
}

// 项目域必须把归属、白名单与凭证三件事表达完整，且权限只能是受控白名单。
func TestPlatformProjectsMigrationDefinesProjectsProductsAndCredentials(t *testing.T) {
	t.Parallel()
	migration := platformProjectsMigration(t)

	for _, fragment := range []string{
		"CREATE TABLE IF NOT EXISTS projects (",
		"CREATE TABLE IF NOT EXISTS project_products (",
		"CREATE TABLE IF NOT EXISTS api_credentials (",
		"project_type IN ('cloud', 'oem')",
		"(project_type = 'oem' AND manufacturer_id IS NOT NULL)",
		"(project_type = 'cloud' AND manufacturer_id IS NULL)",
		"CONSTRAINT uniq_project_products UNIQUE (project_id, product_id)",
		"projects_owner_account_id_fkey REFERENCES portal_accounts(id) ON DELETE RESTRICT",
		"project_products_project_id_fkey REFERENCES projects(id) ON DELETE CASCADE",
		"api_credentials_project_id_fkey REFERENCES projects(id) ON DELETE RESTRICT",
		"api_credentials_api_key_key UNIQUE",
		"octet_length(api_secret_ciphertext) > 16",
		"permissions <@ ARRAY['devices:read', 'devices:operate', 'ota:publish', 'ota:deploy']::text[]",
		"ALTER TABLE products ADD COLUMN IF NOT EXISTS owner_account_id uuid",
		"ALTER TABLE products ADD COLUMN IF NOT EXISTS category varchar(32)",
		"products_owner_account_id_fkey",
	} {
		requireMigrationContains(t, migration, fragment)
	}

	for _, indexName := range []string{
		"idx_projects_owner",
		"idx_projects_manufacturer",
		"idx_api_credentials_project",
		"idx_products_owner",
	} {
		requireMigrationContains(t, migration, "INDEX IF NOT EXISTS "+indexName)
	}

	for _, trigger := range []string{
		"trg_projects_updated_at",
		"trg_project_products_updated_at",
		"trg_api_credentials_updated_at",
	} {
		requireMigrationContains(t, migration, "CREATE TRIGGER "+trigger)
		requireMigrationContains(t, migration, "DROP TRIGGER IF EXISTS "+trigger)
	}
	requireMigrationContains(t, migration, "EXECUTE FUNCTION set_updated_at()")
}

// 项目域同样不得变成第二套设备域，也不得引入 tenant / IDevID / RMA / OTA campaign。
func TestPlatformProjectsMigrationStaysOutOfDeviceAndNonGoalDomains(t *testing.T) {
	t.Parallel()
	migration := strings.ToLower(platformProjectsMigration(t))

	for _, forbidden := range []string{
		"create table devices",
		"create table bindings",
		"create table device_tokens",
		"create table device_certificates",
		"create table device_registry",
		"create table device_bindings",
		"allowed_sn_prefixes",
	} {
		if strings.Contains(migration, forbidden) {
			t.Errorf("platform projects migration must not introduce %q", forbidden)
		}
	}
	for _, word := range []string{"tenant_id", "idevid", "rma", "ota_campaigns", "product_dps"} {
		pattern := regexp.MustCompile("\\b" + word + "\\b")
		if pattern.MatchString(migration) {
			t.Errorf("platform projects migration must not introduce the non-goal identifier %q", word)
		}
	}
}

// 允许的 ALTER 只有"加列/补命名约束"，其余结构性改动与数据写入都不允许。
func TestPlatformProjectsMigrationIsAdditiveAndIdempotent(t *testing.T) {
	t.Parallel()
	migration := platformProjectsMigration(t)

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
	} {
		if strings.Contains(migration, forbidden) {
			t.Errorf("platform projects migration must stay additive, found %q", forbidden)
		}
	}
	for _, line := range strings.Split(migration, "\n") {
		trimmed := strings.TrimSpace(strings.ToUpper(line))
		if strings.HasPrefix(trimmed, "ALTER TABLE ") && !strings.Contains(trimmed, "ADD COLUMN IF NOT EXISTS") &&
			!strings.Contains(trimmed, "ADD CONSTRAINT") {
			t.Errorf("platform projects migration uses an unsupported ALTER: %q", line)
		}
	}
}
