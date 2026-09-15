package postgres

import (
	"context"
	"testing"
)

// baselineWithoutDeviceIdentity 是 664fdfa 之前的 device_registry / device_activations 基线。
// 已部署库登记过 001_initial.sql，runner 按文件名跳过，因此只能靠 002 顺序增量补齐身份列。
// 这里内嵌最小基线，避免测试依赖 Git 历史中的旧文件内容。
//
// set_updated_at() 也必须出现在基线里：它自最初版 001 起就由每个业务表的 trg_*_updated_at
// 依赖，任何真实登记过 001 的库都有它；而 003 起的顺序增量会继续复用它。
const baselineWithoutDeviceIdentity = `
CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS trigger
LANGUAGE plpgsql
AS $body$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$body$;
CREATE TABLE device_registry (
    id uuid CONSTRAINT device_registry_pkey PRIMARY KEY,
    device_sn varchar(64) NOT NULL CONSTRAINT device_registry_device_sn_key UNIQUE,
    thing_name varchar(128) NOT NULL CONSTRAINT device_registry_thing_name_key UNIQUE,
    pid varchar(64) NOT NULL,
    certificate_id varchar(128),
    status varchar(24) NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT device_registry_thing_name_check CHECK (thing_name = device_sn)
);
CREATE TABLE device_activations (
    id uuid CONSTRAINT device_activations_pkey PRIMARY KEY,
    idempotency_key varchar(128) NOT NULL,
    status varchar(24) NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
-- products 也属于最初版 001：004 会给它补归属列，因此基线必须包含它。
CREATE TABLE products (
    id uuid CONSTRAINT products_pkey PRIMARY KEY,
    pid varchar(64) NOT NULL CONSTRAINT products_pid_key UNIQUE,
    name varchar(128) NOT NULL,
    status varchar(16) NOT NULL DEFAULT 'active'
);
-- audit_logs 同样自最初版就存在：005 会补审计主体列。这里的形状刻意保持"旧"，
-- 即没有 actor_kind/actor_account_id/actor_label，迁移必须能把它补齐。
CREATE TABLE audit_logs (
    id uuid CONSTRAINT audit_logs_pkey PRIMARY KEY,
    internal_app_id varchar(64),
    operation varchar(96) NOT NULL,
    resource_type varchar(64),
    resource_id varchar(128),
    request_id varchar(128),
    outcome varchar(16) NOT NULL
        CONSTRAINT audit_logs_outcome_check
        CHECK (outcome IN ('accepted', 'succeeded', 'rejected', 'failed')),
    error_code varchar(64),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO device_registry (id, device_sn, thing_name, pid, status)
VALUES ('00000000-0000-4000-8000-0000000000a1',
        'fdr-319fe199e4be7aa2d71958f4a0f8c941',
        'fdr-319fe199e4be7aa2d71958f4a0f8c941', 'feeder-x1', 'active');
`

// 已登记 001 的旧库必须能靠顺序增量补齐身份列，既有行保留，且冗余的 sn_source 被移除。
func TestDeviceIdentityMigrationUpgradesRegisteredBaseline(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)

	if _, err := pool.Exec(ctx, baselineWithoutDeviceIdentity); err != nil {
		t.Fatalf("apply pre-identity baseline: %v", err)
	}
	// 已部署库的特征是台账里已登记 001_initial.sql：runner 因此跳过该文件，只执行 002。
	// 不登记这条记录就不是"已部署库"的模拟，测试会走成全新库。
	if _, err := pool.Exec(ctx, `
		CREATE TABLE gateway_schema_migrations (
			name text PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		)
	`); err != nil {
		t.Fatalf("create migration registry: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO gateway_schema_migrations (name) VALUES ('001_initial.sql')`); err != nil {
		t.Fatalf("record baseline migration: %v", err)
	}
	// 旧代码路径对 chip_uid 的读取必须在迁移前真实失败，证明确有缺口。
	if _, err := pool.Exec(ctx,
		`SELECT id::text FROM device_registry WHERE chip_uid=$1`, []byte{0x01}); err == nil {
		t.Fatal("baseline unexpectedly has chip_uid; the migration would be untested")
	}

	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatalf("ApplyMigrations() error = %v", err)
	}

	var sn string
	var chipUID *[]byte
	if err := pool.QueryRow(ctx, `
		SELECT device_sn, chip_uid FROM device_registry
		WHERE device_sn = 'fdr-319fe199e4be7aa2d71958f4a0f8c941'
	`).Scan(&sn, &chipUID); err != nil {
		t.Fatalf("read migrated row: %v", err)
	}
	if chipUID != nil {
		t.Fatalf("existing row chip_uid = %v, want NULL until a device reports its UID", *chipUID)
	}
	if sn != "fdr-319fe199e4be7aa2d71958f4a0f8c941" {
		t.Fatalf("existing device_sn changed: %q", sn)
	}

	// 迁移后只保留 chip_uid；sn_source 已移除，唯一索引必须真实生效。
	if _, err := pool.Exec(ctx,
		`UPDATE device_registry SET chip_uid=$1 WHERE device_sn=$2`,
		[]byte{0x01, 0x02, 0x03}, sn); err != nil {
		t.Fatalf("chip_uid is not writable after migration: %v", err)
	}
	var snSourceCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'device_registry' AND column_name = 'sn_source'
	`).Scan(&snSourceCount); err != nil {
		t.Fatalf("inspect sn_source after migration: %v", err)
	}
	if snSourceCount != 0 {
		t.Fatal("sn_source must be removed after migration")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO device_registry (id, device_sn, thing_name, pid, status, chip_uid)
		VALUES ('00000000-0000-4000-8000-0000000000a2','fdr-duplicate','fdr-duplicate','feeder-x1','active',$1)
	`, []byte{0x01, 0x02, 0x03}); err == nil {
		t.Fatal("chip_uid partial unique index is missing after migration")
	}

	// 重复执行必须仍然成功：迁移对已升级库是 no-op。
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatalf("ApplyMigrations() is not idempotent: %v", err)
	}
}

// 全新空库走 001 + 002 后，身份列必须与 001 基线声明一致。
func TestDeviceIdentityMigrationMatchesFreshBaseline(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatalf("ApplyMigrations() error = %v", err)
	}
	for _, column := range []struct{ table, name, dataType string }{
		{"device_registry", "chip_uid", "bytea"},
		{"device_activations", "requested_device_sn", "character varying"},
		{"device_activations", "requested_chip_uid", "bytea"},
	} {
		var dataType string
		var isNullable string
		if err := pool.QueryRow(ctx, `
			SELECT data_type, is_nullable FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2
		`, column.table, column.name).Scan(&dataType, &isNullable); err != nil {
			t.Fatalf("fresh schema missing %s.%s: %v", column.table, column.name, err)
		}
		if dataType != column.dataType {
			t.Fatalf("%s.%s type = %q, want %q", column.table, column.name, dataType, column.dataType)
		}
	}
	var indexDef string
	if err := pool.QueryRow(ctx, `
		SELECT indexdef FROM pg_indexes
		WHERE schemaname = current_schema() AND indexname = 'uniq_device_registry_chip_uid'
	`).Scan(&indexDef); err != nil {
		t.Fatalf("fresh schema missing uniq_device_registry_chip_uid: %v", err)
	}
	if indexDef == "" {
		t.Fatal("uniq_device_registry_chip_uid has an empty definition")
	}
	// 设备身份只用 device_sn 表达：fresh schema 不得再出现 sn_source。
	var snSourceCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'device_registry' AND column_name = 'sn_source'
	`).Scan(&snSourceCount); err != nil {
		t.Fatal(err)
	}
	if snSourceCount != 0 {
		t.Fatal("fresh schema still has device_registry.sn_source")
	}
}
