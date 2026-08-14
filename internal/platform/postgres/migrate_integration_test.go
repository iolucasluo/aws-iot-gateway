package postgres

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigrateCreatesRequiredTablesAndGlobalBindingConstraint(t *testing.T) {
	databaseURL := os.Getenv("TEST_POSTGRES_URL")
	if databaseURL == "" {
		t.Skip("TEST_POSTGRES_URL is not configured")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("pgxpool.New() error = %v", err)
	}
	t.Cleanup(pool.Close)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatalf("ApplyMigrations() error = %v", err)
	}

	for _, table := range []string{
		"devices", "activations", "activation_sessions", "bindings", "device_tokens",
		"commands", "device_events", "schedules", "streams", "snapshots", "recordings",
		"webhook_outbox", "webhook_deliveries", "device_lifecycle_outbox",
		"idempotency_records", "audit_logs",
	} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass('public.' || $1) IS NOT NULL`, table).Scan(&exists); err != nil {
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
				WHERE table_schema='public' AND table_name='devices' AND column_name=$1
			)
		`, column).Scan(&exists); err != nil {
			t.Fatalf("check devices.%s: %v", column, err)
		}
		if !exists {
			t.Fatalf("devices.%s does not exist", column)
		}
	}

	deviceID := "018f0000-0000-7000-8000-000000000001"
	_, err = pool.Exec(ctx, `
		INSERT INTO devices (id, device_sn, thing_name, pid, status)
		VALUES ($1, 'fdr-00000000000000000000000000000001', 'fdr-00000000000000000000000000000001', 'feeder-x1', 'active')
	`, deviceID)
	if err != nil {
		t.Fatalf("insert device: %v", err)
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO bindings (id, device_id, internal_app_id, binding_external_user_id, status)
		VALUES ($1, $2, 'livpet-staging', 'user-1', 'active')
	`, "018f0000-0000-7000-8000-000000000002", deviceID)
	if err != nil {
		t.Fatalf("insert first binding: %v", err)
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO bindings (id, device_id, internal_app_id, binding_external_user_id, status)
		VALUES ($1, $2, 'demo-staging', 'user-2', 'active')
	`, "018f0000-0000-7000-8000-000000000003", deviceID)
	if err == nil {
		t.Fatal("second active binding for the same device was accepted")
	}
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != "23505" {
		t.Fatalf("second binding error = %v, want PostgreSQL unique violation", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE bindings SET status='inactive', released_at=now() WHERE id=$1`, "018f0000-0000-7000-8000-000000000002"); err != nil {
		t.Fatalf("release first binding: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO bindings (id, device_id, internal_app_id, binding_external_user_id, status)
		VALUES ($1, $2, 'demo-staging', 'user-2', 'active')
	`, "018f0000-0000-7000-8000-000000000003", deviceID); err != nil {
		t.Fatalf("bind after release: %v", err)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	databaseURL := os.Getenv("TEST_POSTGRES_URL")
	if databaseURL == "" {
		t.Skip("TEST_POSTGRES_URL is not configured")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("pgxpool.New() error = %v", err)
	}
	t.Cleanup(pool.Close)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatalf("first ApplyMigrations() error = %v", err)
	}
	if err := ApplyMigrations(ctx, pool); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("second ApplyMigrations() error = %v", err)
	}
}
