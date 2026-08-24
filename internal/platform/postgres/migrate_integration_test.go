package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
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
		"products", "device_registry", "device_activations", "device_activation_sessions", "device_bindings", "device_tokens",
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
	applyProductSeeds(t, ctx, pool)
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

func TestMigrateUpgradesLegacySquashedInitialWithoutDroppingData(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	_, err := pool.Exec(ctx, `
		CREATE OR REPLACE FUNCTION set_updated_at()
		RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			NEW.updated_at = now();
			RETURN NEW;
		END;
		$$;
		CREATE TABLE gateway_schema_migrations (
			name text PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		);
		INSERT INTO gateway_schema_migrations (name) VALUES ('001_initial.sql');
		CREATE TABLE device_registry (
			id uuid PRIMARY KEY,
			device_sn varchar(64) NOT NULL UNIQUE,
			thing_name varchar(128) NOT NULL UNIQUE,
			pid varchar(64) NOT NULL DEFAULT 'feeder-x1'
				CONSTRAINT device_registry_pid_check CHECK (pid = 'feeder-x1'),
			status varchar(24) NOT NULL,
			created_at timestamptz NOT NULL DEFAULT now(),
			updated_at timestamptz NOT NULL DEFAULT now()
		);
		CREATE TABLE device_activations (
			id uuid PRIMARY KEY,
			internal_app_id varchar(64) NOT NULL,
			binding_external_user_id varchar(255) NOT NULL,
			method varchar(8) NOT NULL,
			status varchar(24) NOT NULL,
			code_hash bytea NOT NULL,
			idempotency_key varchar(128) NOT NULL,
			request_hash bytea NOT NULL,
			device_registry_id uuid REFERENCES device_registry(id),
			expires_at timestamptz NOT NULL,
			created_at timestamptz NOT NULL DEFAULT now(),
			updated_at timestamptz NOT NULL DEFAULT now()
		);
		INSERT INTO device_registry (id, device_sn, thing_name, status) VALUES
			('018f0000-0000-7000-8000-000000000011',
			 'fdr-00000000000000000000000000000011',
			 'fdr-00000000000000000000000000000011', 'active');
		INSERT INTO device_activations (
			id, internal_app_id, binding_external_user_id, method, status,
			code_hash, idempotency_key, request_hash, device_registry_id, expires_at
		) VALUES
			('018f0000-0000-7000-8000-000000000012', 'livpet-staging', 'legacy-user', 'ble', 'activated',
			 decode(repeat('aa', 32), 'hex'), 'legacy-linked', decode(repeat('bb', 32), 'hex'),
			 '018f0000-0000-7000-8000-000000000011', now() + interval '1 hour'),
			('018f0000-0000-7000-8000-000000000013', 'livpet-staging', 'legacy-user', 'ble', 'pending',
			 decode(repeat('cc', 32), 'hex'), 'legacy-unlinked', decode(repeat('dd', 32), 'hex'),
			 NULL, now() + interval '1 hour');
	`)
	if err != nil {
		t.Fatalf("install legacy schema: %v", err)
	}

	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatalf("ApplyMigrations() legacy upgrade error = %v", err)
	}

	var productName, productStatus string
	var capabilities []byte
	if err := pool.QueryRow(ctx, `SELECT name, status, capabilities FROM products WHERE pid='feeder-x1'`).Scan(
		&productName, &productStatus, &capabilities,
	); err != nil {
		t.Fatalf("legacy feeder product was not preserved: %v", err)
	}
	if productName != "Feeder X1" || productStatus != "active" || string(capabilities) != "[]" {
		t.Fatalf("legacy feeder product = %q/%q capabilities=%s", productName, productStatus, capabilities)
	}

	rows, err := pool.Query(ctx, `SELECT id::text, pid FROM device_activations ORDER BY id`)
	if err != nil {
		t.Fatalf("list upgraded activations: %v", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id, pid string
		if err := rows.Scan(&id, &pid); err != nil {
			t.Fatalf("scan upgraded activation: %v", err)
		}
		if pid != "feeder-x1" {
			t.Errorf("activation %s pid = %q, want feeder-x1", id, pid)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate upgraded activations: %v", err)
	}
	if count != 2 {
		t.Fatalf("upgraded activation count = %d, want 2", count)
	}

	var defaultValue *string
	if err := pool.QueryRow(ctx, `
		SELECT column_default FROM information_schema.columns
		WHERE table_schema=current_schema() AND table_name='device_registry' AND column_name='pid'
	`).Scan(&defaultValue); err != nil {
		t.Fatalf("read device_registry.pid default: %v", err)
	}
	if defaultValue != nil {
		t.Fatalf("device_registry.pid default = %q, want NULL", *defaultValue)
	}
	for _, constraint := range []string{"device_registry_pid_fkey", "device_activations_pid_fkey"} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_constraint WHERE conname=$1)`, constraint).Scan(&exists); err != nil {
			t.Fatalf("check %s: %v", constraint, err)
		}
		if !exists {
			t.Errorf("constraint %s does not exist", constraint)
		}
	}

	applyProductSeeds(t, ctx, pool)
	if _, err := pool.Exec(ctx, `
		INSERT INTO device_registry (id, device_sn, thing_name, pid, status)
		VALUES ('018f0000-0000-7000-8000-000000000014',
		        'fdr-00000000000000000000000000000014',
		        'fdr-00000000000000000000000000000014', 'minion-cam-x1', 'active')
	`); err != nil {
		t.Fatalf("insert second product after legacy upgrade: %v", err)
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

func TestProductSeedIsExplicitAndIdempotent(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatalf("ApplyMigrations() error = %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM products`).Scan(&count); err != nil {
		t.Fatalf("count products after migration: %v", err)
	}
	if count != 0 {
		t.Fatalf("products after migration = %d, want 0 before explicit seed", count)
	}

	applyProductSeeds(t, ctx, pool)
	applyProductSeeds(t, ctx, pool)
	rows, err := pool.Query(ctx, `SELECT pid, capabilities FROM products WHERE status='active' ORDER BY pid`)
	if err != nil {
		t.Fatalf("list seeded products: %v", err)
	}
	defer rows.Close()
	products := make(map[string][]string)
	for rows.Next() {
		var pid string
		var capabilities []byte
		if err := rows.Scan(&pid, &capabilities); err != nil {
			t.Fatalf("scan product pid: %v", err)
		}
		var capabilityIDs []string
		if err := json.Unmarshal(capabilities, &capabilityIDs); err != nil {
			t.Fatalf("decode %s capabilities: %v", pid, err)
		}
		slices.Sort(capabilityIDs)
		products[pid] = capabilityIDs
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate product pids: %v", err)
	}
	if !slices.Equal(products["feeder-x1"], []string{"local-media.v1", "ptz.v1", "webrtc.v1"}) {
		t.Fatalf("feeder capabilities = %v", products["feeder-x1"])
	}
	if !slices.Equal(products["minion-cam-x1"], []string{"webrtc.v1"}) {
		t.Fatalf("minion capabilities = %v", products["minion-cam-x1"])
	}
}
