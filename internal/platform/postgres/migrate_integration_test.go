package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/db/migrations"
)

func TestMigrateCreatesRequiredTablesAndGlobalBindingConstraint(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatalf("ApplyMigrations() error = %v", err)
	}

	for _, table := range []string{
		"products", "product_dps", "device_registry", "device_activations", "device_activation_sessions", "device_bindings", "device_tokens",
		"device_commands", "device_events", "device_schedules", "device_streams", "device_snapshots", "device_recordings",
		"ota_firmware_versions", "device_ota_deployments",
		"webhook_outbox", "webhook_deliveries", "device_lifecycle_outbox",
		"idempotency_records", "audit_logs", "device_local_recordings",
		"device_local_recording_tombstones", "local_recording_cursors",
		"local_recording_reconciliations", "local_recording_reconciliation_sightings",
		"local_recording_thumbnail_outbox",
		"device_component_bindings",
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
	dpRepository, err := NewProductDPRepository(pool)
	if err != nil {
		t.Fatalf("create product DP repository: %v", err)
	}
	definitions, err := dpRepository.LoadProductDPDefinitions(ctx, "fdr-00000000000000000000000000000001")
	if err != nil {
		t.Fatalf("load feeder product DPs: %v", err)
	}
	if len(definitions) != 2 || definitions[0].Code != "feed" || definitions[1].Code != "set_schedule" {
		t.Fatalf("feeder product DPs = %#v, want feed/set_schedule", definitions)
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
		CREATE TABLE device_bindings (
			id uuid PRIMARY KEY,
			device_registry_id uuid NOT NULL REFERENCES device_registry(id),
			internal_app_id varchar(64) NOT NULL,
			binding_external_user_id varchar(255) NOT NULL,
			status varchar(16) NOT NULL,
			created_at timestamptz NOT NULL DEFAULT now(),
			updated_at timestamptz NOT NULL DEFAULT now()
		);
		CREATE TABLE device_events (
			id uuid PRIMARY KEY,
			device_registry_id uuid NOT NULL REFERENCES device_registry(id),
			event_type varchar(64) NOT NULL,
			data jsonb NOT NULL DEFAULT '{}'::jsonb,
			received_at timestamptz NOT NULL DEFAULT now()
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
	want := map[string][]string{
		"feeder-x1":     {"local-media.v1", "ota.app.v1", "ptz.v1", "webrtc.v1"},
		"lamp-x1":       {},
		"minion-cam-x1": {"webrtc.v1"},
	}
	if len(products) != len(want) {
		t.Fatalf("seeded product count = %d, want %d: %v", len(products), len(want), products)
	}
	for pid, capabilities := range want {
		if !slices.Equal(products[pid], capabilities) {
			t.Fatalf("%s capabilities = %v, want %v", pid, products[pid], capabilities)
		}
	}
	var dpCount, feederDPCount, minionDPCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM product_dps`).Scan(&dpCount); err != nil {
		t.Fatalf("count seeded product DPs: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM product_dps dp JOIN products p ON p.id=dp.product_id WHERE p.pid='feeder-x1'
	`).Scan(&feederDPCount); err != nil {
		t.Fatalf("count feeder product DPs: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM product_dps dp JOIN products p ON p.id=dp.product_id WHERE p.pid='minion-cam-x1'
	`).Scan(&minionDPCount); err != nil {
		t.Fatalf("count minion product DPs: %v", err)
	}
	if dpCount != 31 || feederDPCount != 3 || minionDPCount != 28 {
		t.Fatalf("seeded product DP counts = total %d/feeder %d/minion %d, want 31/3/28", dpCount, feederDPCount, minionDPCount)
	}
	var minionDetailedCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM product_dps dp JOIN products p ON p.id=dp.product_id
		WHERE p.pid='minion-cam-x1' AND jsonb_typeof(dp.spec->'properties')='object'
	`).Scan(&minionDetailedCount); err != nil {
		t.Fatalf("count detailed minion product DP specs: %v", err)
	}
	if minionDetailedCount != 28 {
		t.Fatalf("detailed minion product DP specs = %d, want 28", minionDetailedCount)
	}
	var volumeMin, volumeMax, sdFilesAck string
	if err := pool.QueryRow(ctx, `
		SELECT
			volume.spec->'properties'->'volume'->>'minimum',
			volume.spec->'properties'->'volume'->>'maximum',
			files.spec->'ack'->>'files'
		FROM product_dps volume
		JOIN products vp ON vp.id=volume.product_id AND vp.pid='minion-cam-x1'
		JOIN product_dps files ON files.code='sd_files' AND files.product_id=volume.product_id
		WHERE volume.code='speaker_vol'
	`).Scan(&volumeMin, &volumeMax, &sdFilesAck); err != nil {
		t.Fatalf("load detailed minion DP spec examples: %v", err)
	}
	if volumeMin != "0" || volumeMax != "100" || sdFilesAck != "finalized recording metadata objects" {
		t.Fatalf("detailed minion DP spec examples = volume %s..%s, sd_files=%q", volumeMin, volumeMax, sdFilesAck)
	}
}

func TestMigrateKeepsLocalThumbnailOutboxStatusesLengthSafe(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatalf("ApplyMigrations() error = %v", err)
	}

	var dataType string
	var hasNoCharacterLimit bool
	if err := pool.QueryRow(ctx, `
		SELECT data_type, character_maximum_length IS NULL
		FROM information_schema.columns
		WHERE table_schema=current_schema()
		  AND table_name='local_recording_thumbnail_outbox'
		  AND column_name='status'
	`).Scan(&dataType, &hasNoCharacterLimit); err != nil {
		t.Fatalf("read local thumbnail outbox status type: %v", err)
	}
	if dataType != "text" || !hasNoCharacterLimit {
		t.Fatalf("local thumbnail outbox status type = %s unlimited=%t, want text unlimited=true", dataType, hasNoCharacterLimit)
	}

	outboxID := seedLocalThumbnailOutboxStatusFixture(t, ctx, pool, "delete_pending")
	if _, err := pool.Exec(ctx, `
		UPDATE local_recording_thumbnail_outbox SET status='delete_processing' WHERE id=$1
	`, outboxID); err != nil {
		t.Fatalf("write longest valid local thumbnail status: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE local_recording_thumbnail_outbox SET status='not_a_valid_status' WHERE id=$1
	`, outboxID); err == nil {
		t.Fatal("invalid local thumbnail status was accepted")
	} else {
		var postgresError *pgconn.PgError
		if !errors.As(err, &postgresError) || postgresError.Code != "23514" {
			t.Fatalf("invalid local thumbnail status error = %v, want check violation 23514", err)
		}
	}
}

func TestLocalThumbnailStatusTextDownMigrationRestoresPreviousWidthSafely(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatalf("ApplyMigrations() error = %v", err)
	}
	outboxID := seedLocalThumbnailOutboxStatusFixture(t, ctx, pool, "delete_processing")
	down, err := migrations.Files.ReadFile("006_local_thumbnail_status_text.down.sql")
	if err != nil {
		t.Fatalf("read local thumbnail status down migration: %v", err)
	}
	if _, err := pool.Exec(ctx, string(down)); err != nil {
		t.Fatalf("apply local thumbnail status down migration: %v", err)
	}

	var status, dataType string
	var maximumLength int
	if err := pool.QueryRow(ctx, `
		SELECT status FROM local_recording_thumbnail_outbox WHERE id=$1
	`, outboxID).Scan(&status); err != nil {
		t.Fatalf("read rolled back local thumbnail status: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT data_type, character_maximum_length
		FROM information_schema.columns
		WHERE table_schema=current_schema()
		  AND table_name='local_recording_thumbnail_outbox'
		  AND column_name='status'
	`).Scan(&dataType, &maximumLength); err != nil {
		t.Fatalf("read rolled back local thumbnail status type: %v", err)
	}
	if status != "delete_pending" || dataType != "character varying" || maximumLength != 16 {
		t.Fatalf("rolled back local thumbnail status = %s type=%s length=%d", status, dataType, maximumLength)
	}
}

func seedLocalThumbnailOutboxStatusFixture(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool, status string,
) string {
	t.Helper()
	applyProductSeeds(t, ctx, pool)
	const deviceID = "018f0000-0000-7000-8000-000000000051"
	const recordingID = "018f0000-0000-7000-8000-000000000052"
	const outboxID = "018f0000-0000-7000-8000-000000000053"
	if _, err := pool.Exec(ctx, `
		INSERT INTO device_registry (id, device_sn, thing_name, pid, status)
		VALUES ($1, 'fdr-thumbnail-status', 'fdr-thumbnail-status', 'feeder-x1', 'active')
	`, deviceID); err != nil {
		t.Fatalf("seed local thumbnail device: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO device_local_recordings (
			id, device_registry_id, recording_id, revision, start_at, end_at,
			recording_date, duration_ms, size_bytes, recording_type, codec,
			width, height, fps_num, fps_den, playable, seekable,
			thumbnail_status, thumbnail_etag, thumbnail_size_bytes
		) VALUES (
			$2, $1, 'rec_5123456789abcdef0123456789abcdef',
			'rev_5123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef',
			'2026-08-28T00:00:00Z', '2026-08-28T00:01:00Z', '2026-08-28',
			60000, 1024, 'manual', 'h264', 1920, 1080, 25, 1, true, true,
			'failed', 'sha256:5123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef', 1234
		)
	`, deviceID, recordingID); err != nil {
		t.Fatalf("seed local thumbnail recording: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO local_recording_thumbnail_outbox (id, local_recording_id, status)
		VALUES ($1, $2, $3)
	`, outboxID, recordingID, status); err != nil {
		t.Fatalf("seed local thumbnail task: %v", err)
	}
	return outboxID
}
