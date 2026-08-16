package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestMigrateCreatesRequiredTablesAndGlobalBindingConstraint(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatalf("ApplyMigrations() error = %v", err)
	}

	for _, table := range []string{
		"device_registry", "device_activations", "device_activation_sessions", "device_bindings", "device_tokens",
		"device_commands", "device_events", "device_schedules", "device_streams", "device_snapshots", "device_recordings",
		"webhook_outbox", "webhook_deliveries", "device_lifecycle_outbox",
		"idempotency_records", "audit_logs",
	} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatalf("check table %s: %v", table, err)
		}
		if !exists {
			t.Fatalf("table %s does not exist", table)
		}
	}
	for _, column := range []string{"presence_online", "presence_updated_at", "last_seen_at"} {
		var exists bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM information_schema.columns
				WHERE table_schema=current_schema() AND table_name='device_registry' AND column_name=$1
			)
		`, column).Scan(&exists); err != nil {
			t.Fatalf("check device_registry.%s: %v", column, err)
		}
		if !exists {
			t.Fatalf("device_registry.%s does not exist", column)
		}
	}

	deviceID := "018f0000-0000-7000-8000-000000000001"
	_, err := pool.Exec(ctx, `
		INSERT INTO device_registry (id, device_sn, thing_name, pid, status)
		VALUES ($1, 'fdr-00000000000000000000000000000001', 'fdr-00000000000000000000000000000001', 'feeder-x1', 'active')
	`, deviceID)
	if err != nil {
		t.Fatalf("insert device: %v", err)
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO device_bindings (id, device_registry_id, internal_app_id, binding_external_user_id, status)
		VALUES ($1, $2, 'livpet-staging', 'user-1', 'active')
	`, "018f0000-0000-7000-8000-000000000002", deviceID)
	if err != nil {
		t.Fatalf("insert first binding: %v", err)
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO device_bindings (id, device_registry_id, internal_app_id, binding_external_user_id, status)
		VALUES ($1, $2, 'demo-staging', 'user-2', 'active')
	`, "018f0000-0000-7000-8000-000000000003", deviceID)
	if err == nil {
		t.Fatal("second active binding for the same device was accepted")
	}
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != "23505" {
		t.Fatalf("second binding error = %v, want PostgreSQL unique violation", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE device_bindings SET status='inactive', released_at=now() WHERE id=$1`, "018f0000-0000-7000-8000-000000000002"); err != nil {
		t.Fatalf("release first binding: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO device_bindings (id, device_registry_id, internal_app_id, binding_external_user_id, status)
		VALUES ($1, $2, 'demo-staging', 'user-2', 'active')
	`, "018f0000-0000-7000-8000-000000000003", deviceID); err != nil {
		t.Fatalf("bind after release: %v", err)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatalf("first ApplyMigrations() error = %v", err)
	}
	if err := ApplyMigrations(ctx, pool); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("second ApplyMigrations() error = %v", err)
	}
}
