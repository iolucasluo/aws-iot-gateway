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
)

func TestMigrateCreatesRequiredTablesAndGlobalBindingConstraint(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatalf("ApplyMigrations() error = %v", err)
	}

	for _, table := range []string{
		"products", "dp_profiles", "dp_definitions", "dp_profile_members", "device_registry", "device_activations", "device_activation_sessions", "device_bindings", "device_tokens",
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
		INSERT INTO device_registry (dp_profile_id, id, device_sn, thing_name, pid, status)
		VALUES ('40000000-0000-0000-0000-000000000001', $1, 'fdr-00000000000000000000000000000001', 'fdr-00000000000000000000000000000001', 'feeder-x1', 'active')
	`, deviceID)
	if err != nil {
		t.Fatalf("insert device: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE device_registry SET dp_profile_id=(SELECT id FROM dp_profiles WHERE code='feeder-v1') WHERE id=$1`, deviceID); err != nil {
		t.Fatal(err)
	}
	dpRepository, err := NewDPProfileRepository(pool)
	if err != nil {
		t.Fatalf("create product DP repository: %v", err)
	}
	definitions, err := dpRepository.LoadDPDefinitions(ctx, "fdr-00000000000000000000000000000001")
	if err != nil {
		t.Fatalf("load feeder product DPs: %v", err)
	}
	codes := make([]string, len(definitions))
	for index, definition := range definitions {
		codes[index] = definition.Code
	}
	wantCodes := []string{"audio_playback", "audio_reset", "audio_update", "clear_jam", "device_info", "factory_reset", "feed", "feed_done", "feed_error", "get_feed_log", "get_log", "get_schedule", "led", "local_media", "motion_det", "motion_track", "night_mode", "ota_install", "pet_det", "pet_detection_v1", "ptz", "query_status", "reboot", "recommission", "record_clip", "record_mode", "record_start", "record_stop", "sd_files", "sd_recordings_v1", "sd_thumbnail_v1", "sdcard", "sdcard_status", "set_schedule", "snapshot", "speaker_vol", "status", "stream", "two_way_audio", "video_res"}
	if !slices.Equal(codes, wantCodes) {
		t.Fatalf("feeder product DP codes = %v, want %v", codes, wantCodes)
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
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dp_profile_members`).Scan(&dpCount); err != nil {
		t.Fatalf("count seeded product DPs: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM dp_profile_members member JOIN dp_profiles profile ON profile.code=member.dp_profile_code WHERE profile.code='feeder-v1'
	`).Scan(&feederDPCount); err != nil {
		t.Fatalf("count feeder product DPs: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM dp_profile_members member JOIN dp_profiles profile ON profile.code=member.dp_profile_code WHERE profile.code='camera-v1'
	`).Scan(&minionDPCount); err != nil {
		t.Fatalf("count minion product DPs: %v", err)
	}
	if dpCount != 96 || feederDPCount != 40 || minionDPCount != 28 {
		t.Fatalf("seeded product DP counts = total %d/feeder %d/minion %d, want 96/40/28", dpCount, feederDPCount, minionDPCount)
	}
	var minionDetailedCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM dp_definitions dp JOIN dp_profile_members member ON member.dp_definition_id=dp.id JOIN dp_profiles profile ON profile.code=member.dp_profile_code
		WHERE profile.code='camera-v1' AND jsonb_typeof(dp.spec->'properties')='object'
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
		FROM dp_definitions volume
        JOIN dp_profile_members vm ON vm.dp_definition_id=volume.id
        JOIN dp_profiles profile ON profile.code=vm.dp_profile_code AND profile.code='camera-v1'
        JOIN dp_profile_members fm ON fm.dp_profile_code=profile.code AND fm.code='sd_files'
        JOIN dp_definitions files ON files.id=fm.dp_definition_id
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

func seedLocalThumbnailOutboxStatusFixture(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool, status string,
) string {
	t.Helper()
	applyProductSeeds(t, ctx, pool)
	const deviceID = "018f0000-0000-7000-8000-000000000051"
	const recordingID = "018f0000-0000-7000-8000-000000000052"
	const outboxID = "018f0000-0000-7000-8000-000000000053"
	if _, err := pool.Exec(ctx, `
		INSERT INTO device_registry (dp_profile_id, id, device_sn, thing_name, pid, status)
		VALUES ('40000000-0000-0000-0000-000000000001', $1, 'fdr-thumbnail-status', 'fdr-thumbnail-status', 'feeder-x1', 'active')
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
