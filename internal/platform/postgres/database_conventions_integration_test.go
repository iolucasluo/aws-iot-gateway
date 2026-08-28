package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSquashedInitialSchemaEnforcesDatabaseConventions(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatalf("ApplyMigrations() error = %v", err)
	}
	applyProductSeeds(t, ctx, pool)

	_, err := pool.Exec(ctx, `
		INSERT INTO device_registry (id, device_sn, thing_name, pid, status)
		VALUES ('018f0000-0000-7000-8000-000000000001',
		        'fdr-00000000000000000000000000000001',
		        'fdr-00000000000000000000000000000001', 'feeder-x1', 'active');
		INSERT INTO device_activations (
			id, internal_app_id, binding_external_user_id, method, pid, status,
			code_hash, idempotency_key, request_hash, device_registry_id, expires_at
		) VALUES (
			'018f0000-0000-7000-8000-000000000002', 'livpet-staging', 'user-1',
			'ble', 'feeder-x1', 'activated', decode(repeat('aa', 32), 'hex'), 'migration-fixture',
			decode(repeat('bb', 32), 'hex'), '018f0000-0000-7000-8000-000000000001',
			now() + interval '1 hour'
		);
		INSERT INTO device_activation_sessions (
			id, device_activation_id, session_external_id, method, status, expires_at
		) VALUES (
			'018f0000-0000-7000-8000-000000000003',
			'018f0000-0000-7000-8000-000000000002', 'session-1', 'ble', 'confirmed',
			now() + interval '1 hour'
		);
		INSERT INTO device_bindings (
			id, device_registry_id, internal_app_id, binding_external_user_id, status
		) VALUES (
			'018f0000-0000-7000-8000-000000000004',
			'018f0000-0000-7000-8000-000000000001', 'livpet-staging', 'user-1', 'active'
		);
		INSERT INTO device_tokens (id, device_binding_id, token_hash, expires_at)
		VALUES ('018f0000-0000-7000-8000-000000000005',
		        '018f0000-0000-7000-8000-000000000004',
		        decode(repeat('cc', 32), 'hex'), now() + interval '1 hour');
		INSERT INTO device_commands (
			id, device_registry_id, internal_app_id, session_id, command, status
		) VALUES (
			'018f0000-0000-7000-8000-000000000006',
			'018f0000-0000-7000-8000-000000000001', 'livpet-staging', 'command-1',
			'{"code":"feed"}'::jsonb, 'pending'
		);
		INSERT INTO device_events (
			id, device_registry_id, event_type, boot_id, event_seq, data
		) VALUES (
			'018f0000-0000-7000-8000-000000000007',
			'018f0000-0000-7000-8000-000000000001', 'status', 'boot-1', 1, '{}'::jsonb
		);
		INSERT INTO device_schedules (device_registry_id, schedules, confirmed_at)
		VALUES ('018f0000-0000-7000-8000-000000000001', '[]'::jsonb, now());
		INSERT INTO device_streams (id, device_registry_id, device_binding_id, kvs_channel_arn, status, expires_at)
		VALUES ('018f0000-0000-7000-8000-000000000008',
		        '018f0000-0000-7000-8000-000000000001',
		        '018f0000-0000-7000-8000-000000000004', 'arn:fixture', 'ready',
		        now() + interval '1 hour');
		INSERT INTO device_snapshots (id, device_registry_id, device_binding_id, status)
		VALUES ('018f0000-0000-7000-8000-000000000009',
		        '018f0000-0000-7000-8000-000000000001',
		        '018f0000-0000-7000-8000-000000000004', 'ready');
		INSERT INTO device_recordings (id, device_registry_id, device_binding_id, kind, status)
		VALUES ('018f0000-0000-7000-8000-000000000010',
		        '018f0000-0000-7000-8000-000000000001',
		        '018f0000-0000-7000-8000-000000000004', 'local', 'ready');
		INSERT INTO device_lifecycle_outbox (id, device_registry_id, operation, payload, status)
		VALUES ('018f0000-0000-7000-8000-000000000011',
		        '018f0000-0000-7000-8000-000000000001', 'revoke_certificate',
		        '{}'::jsonb, 'pending');
	`)
	if err != nil {
		t.Fatalf("seed squashed schema: %v", err)
	}

	for _, table := range []string{
		"device_registry", "device_activations", "device_activation_sessions",
		"device_bindings", "device_commands", "device_schedules", "device_streams",
		"device_snapshots", "device_recordings",
	} {
		var count int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{table}.Sanitize()).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("%s row count = %d, want 1", table, count)
		}
	}

	var before time.Time
	if err := pool.QueryRow(ctx, `
		SELECT updated_at FROM device_bindings
		WHERE id='018f0000-0000-7000-8000-000000000004'
	`).Scan(&before); err != nil {
		t.Fatalf("read binding timestamp: %v", err)
	}
	if _, err := pool.Exec(ctx, `SELECT pg_sleep(0.01)`); err != nil {
		t.Fatalf("wait for timestamp edge: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE device_bindings SET release_reason='fixture'
		WHERE id='018f0000-0000-7000-8000-000000000004'
	`); err != nil {
		t.Fatalf("update binding: %v", err)
	}
	var after time.Time
	if err := pool.QueryRow(ctx, `
		SELECT updated_at FROM device_bindings
		WHERE id='018f0000-0000-7000-8000-000000000004'
	`).Scan(&after); err != nil {
		t.Fatalf("read updated binding timestamp: %v", err)
	}
	if !after.After(before) {
		t.Fatalf("updated_at did not advance: before=%s after=%s", before, after)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE device_bindings SET binding_external_user_id='user-1'
		WHERE id='018f0000-0000-7000-8000-000000000004'
	`); err != nil {
		t.Fatalf("idempotent binding owner update rejected: %v", err)
	}
	_, err = pool.Exec(ctx, `
		UPDATE device_bindings SET binding_external_user_id='user-2'
		WHERE id='018f0000-0000-7000-8000-000000000004'
	`)
	if err == nil {
		t.Fatal("binding owner overwrite was accepted")
	}
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != "23514" ||
		!strings.Contains(postgresError.Message, "is immutable once set") {
		t.Fatalf("binding owner overwrite error = %v, want 23514 immutable message", err)
	}

	for _, name := range []string{
		"device_bindings_device_registry_id_fkey",
		"device_tokens_device_binding_id_fkey",
		"uniq_device_bindings_active_device",
		"idx_device_lifecycle_outbox_ready",
	} {
		var exists bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM pg_constraint WHERE conname=$1
				UNION ALL
				SELECT 1 FROM pg_indexes WHERE schemaname=current_schema() AND indexname=$1
			)
		`, name).Scan(&exists); err != nil {
			t.Fatalf("check schema object %s: %v", name, err)
		}
		if !exists {
			t.Errorf("schema object %s does not exist", name)
		}
	}
}

func TestApplyMigrationsRecordsInitialAndForwardCompatibilityMigration(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatalf("ApplyMigrations() error = %v", err)
	}

	var appliedNames []string
	rows, err := pool.Query(ctx, `SELECT name FROM gateway_schema_migrations ORDER BY name`)
	if err != nil {
		t.Fatalf("list applied migrations: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan migration name: %v", err)
		}
		appliedNames = append(appliedNames, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migration names: %v", err)
	}
	want := []string{
		"001_initial.sql",
		"002_legacy_product_catalog.up.sql",
		"003_local_recording_projection.up.sql",
		"004_local_thumbnail_status_text.up.sql",
	}
	if fmt.Sprint(appliedNames) != fmt.Sprint(want) {
		t.Fatalf("applied migrations = %v, want %v", appliedNames, want)
	}
}

func newIsolatedMigrationPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("TEST_POSTGRES_URL")
	if databaseURL == "" {
		t.Skip("TEST_POSTGRES_URL is not configured")
	}
	base, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect base PostgreSQL: %v", err)
	}
	schema := fmt.Sprintf("gateway_migration_test_%d", time.Now().UnixNano())
	if _, err := base.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		base.Close()
		t.Fatalf("create isolated schema: %v", err)
	}

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		_, _ = base.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		base.Close()
		t.Fatalf("parse PostgreSQL URL: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		_, _ = base.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		base.Close()
		t.Fatalf("connect isolated schema: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = base.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		base.Close()
	})
	return pool
}

func applyProductSeeds(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if err := ApplySeeds(ctx, pool); err != nil {
		t.Fatalf("apply product seed: %v", err)
	}
}
