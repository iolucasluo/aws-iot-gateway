package migrations

import (
	"strings"
	"testing"
)

func platformManufacturingMigration(t *testing.T) string {
	t.Helper()
	return readMigration(t, "007_platform_manufacturing.sql")
}

// 制造面必须把工位、批次与工位报告表达完整，并保持"报告是 append-only 事实"。
func TestPlatformManufacturingMigrationDefinesStationsRunsAndReports(t *testing.T) {
	t.Parallel()
	migration := platformManufacturingMigration(t)

	for _, fragment := range []string{
		"CREATE TABLE IF NOT EXISTS production_stations (",
		"CREATE TABLE IF NOT EXISTS production_runs (",
		"CREATE TABLE IF NOT EXISTS production_station_reports (",
		"CONSTRAINT uniq_production_stations_code UNIQUE (manufacturer_id, code)",
		"CONSTRAINT uniq_production_runs_batch UNIQUE (project_id, batch_code)",
		"CONSTRAINT uniq_production_station_reports_sequence UNIQUE (run_id, station_id, sequence_no)",
		"units_passed + units_failed = units_total",
		"overall_result IN ('pass', 'fail')",
		"jsonb_typeof(unit_results) = 'array'",
		"production_runs_close_state_check",
		"production_stations_manufacturer_id_fkey REFERENCES manufacturers(id) ON DELETE RESTRICT",
	} {
		requireMigrationContains(t, migration, fragment)
	}
	for _, indexName := range []string{
		"idx_production_runs_project", "idx_production_runs_manufacturer", "idx_production_station_reports_run",
	} {
		requireMigrationContains(t, migration, "INDEX IF NOT EXISTS "+indexName)
	}
	for _, trigger := range []string{"trg_production_stations_updated_at", "trg_production_runs_updated_at"} {
		requireMigrationContains(t, migration, "CREATE TRIGGER "+trigger)
	}
	// 工位报告是 append-only 事实：不得有 updated_at 或更新触发器。
	for _, forbidden := range []string{
		"production_station_reports ADD COLUMN", "trg_production_station_reports_updated_at",
	} {
		if strings.Contains(migration, forbidden) {
			t.Errorf("station reports must stay append-only, found %q", forbidden)
		}
	}
}

// 制造面不得引入产线身份、设备域副本或 tenant。
func TestPlatformManufacturingMigrationStaysOutOfIdentityAndDeviceDomains(t *testing.T) {
	t.Parallel()
	migration := strings.ToLower(platformManufacturingMigration(t))

	for _, forbidden := range []string{
		"create table devices",
		"create table device_bindings",
		"create table device_certificates",
		"idevid",
		"tenant_id",
		"allowed_sn_prefixes",
		"production_admission",
		"manufacturing_release",
	} {
		if strings.Contains(migration, forbidden) {
			t.Errorf("manufacturing migration must not introduce %q", forbidden)
		}
	}
}
