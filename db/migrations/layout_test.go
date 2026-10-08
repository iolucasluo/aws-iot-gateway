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
	// 002 是首个真正的顺序增量迁移：001 基线不含运行时身份锚列，而已部署库已登记
	// 001_initial.sql，runner 按文件名跳过，必须靠新文件补齐，不能原地改 001。
	// 003 起是平台面（账号/项目/凭证/制造质量）：同样只允许顺序 additive 新文件。
	want := []string{
		"001_initial.sql", "002_device_identity.sql", "003_platform_accounts.sql",
		"004_platform_projects.sql", "005_platform_audit_webhooks.sql",
		"006_project_webhooks_single_active.sql", "007_platform_manufacturing.sql",
		"008_drop_sn_source.sql", "009_dp_definitions_owner.sql",
		"010_webhook_delivery_history_index.sql",
		"011_device_pet_photos.sql",
	}
	if got := migrationNames(entries); len(got) != len(want) || fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("migration files = %v, want %v", got, want)
	}
}

// 002 必须是幂等、非破坏的增量：只允许加列/索引，不得删改既有结构或数据。
func TestDeviceIdentityMigrationIsAdditiveAndIdempotent(t *testing.T) {
	t.Parallel()

	migration := readMigration(t, "002_device_identity.sql")
	for _, fragment := range []string{
		"ALTER TABLE device_registry ADD COLUMN IF NOT EXISTS chip_uid bytea",
		"CREATE UNIQUE INDEX IF NOT EXISTS uniq_device_registry_chip_uid",
		"ALTER TABLE device_activations ADD COLUMN IF NOT EXISTS requested_device_sn varchar(64)",
		"ALTER TABLE device_activations ADD COLUMN IF NOT EXISTS requested_chip_uid bytea",
		"device_activations_requested_device_sn_check",
	} {
		requireMigrationContains(t, migration, fragment)
	}
	for _, forbidden := range []string{"DROP ", "TRUNCATE", "DELETE ", "UPDATE "} {
		if strings.Contains(migration, forbidden) {
			t.Errorf("device identity migration must stay additive, found %q", forbidden)
		}
	}
}

// 008 移除 device_sn 的并行来源语义：设备身份只用 device_sn 表达，sn_source 是冗余派生态。
func TestDropSNSourceMigrationRemovesColumnIdempotently(t *testing.T) {
	t.Parallel()

	migration := readMigration(t, "008_drop_sn_source.sql")
	for _, fragment := range []string{
		"DROP CONSTRAINT IF EXISTS device_registry_sn_source_check",
		"DROP COLUMN IF EXISTS sn_source",
	} {
		requireMigrationContains(t, migration, fragment)
	}
	if strings.Contains(migration, "ADD COLUMN") {
		t.Error("008 must only remove the redundant sn_source column, not add columns")
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
