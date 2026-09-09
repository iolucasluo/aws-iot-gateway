package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/events"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/media"
)

func TestLocalRecordingEventProjectionIsIdempotentConflictSafeAndDeleteFinal(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatalf("ApplyMigrations() error = %v", err)
	}
	applyProductSeeds(t, ctx, pool)
	deviceID := "018f0000-0000-7000-8000-000000000010"
	bindingID := "018f0000-0000-7000-8000-000000000011"
	if _, err := pool.Exec(ctx, `
		INSERT INTO device_registry (dp_profile_id, id, device_sn, thing_name, pid, status)
		VALUES ('40000000-0000-0000-0000-000000000001', $1,'fdr-0001','fdr-0001','feeder-x1','active')
	`, deviceID); err != nil {
		t.Fatalf("seed local recording device: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO device_bindings (
			id, device_registry_id, internal_app_id, binding_external_user_id, status
		) VALUES ($1,$2,'livpet-staging','user-1','active')
	`, bindingID, deviceID); err != nil {
		t.Fatalf("seed local recording binding: %v", err)
	}
	repository, _ := NewEventRepository(pool)
	now := time.Date(2026, 8, 26, 8, 34, 15, 0, time.UTC)
	baseData := func(revision string) map[string]any {
		return map[string]any{
			"contract": "sd-recording-catalog.v1", "type": "recording_finalized",
			"recording": map[string]any{
				"version":      1,
				"recording_id": "rec_0123456789abcdef0123456789abcdef",
				"revision":     revision,
				"start_at":     "2026-08-26T08:32:00Z", "end_at": "2026-08-26T08:34:15Z",
				"duration_ms": 135000, "size_bytes": 33750000, "type": "event",
				"codec": "h264", "width": 1920, "height": 1080, "fps_num": 25,
				"fps_den": 1, "complete": true, "seekable": true,
				"thumbnail_etag": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				"index_version":  1,
			},
			"thumbnail": map[string]any{
				"status": "pending", "content_type": "image/jpeg", "size_bytes": 1234,
				"etag": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			},
		}
	}
	monotonic := int64(135000)
	finalized := events.Event{
		ID: "018f0000-0000-7000-8000-000000000091", DeviceSN: "fdr-0001",
		EventType: "recording_finalized", BootID: "boot-1", EventSeq: 1,
		MonotonicMS: &monotonic, TimeQuality: "synced", TimeSource: "ntp",
		OccurredAt: &now, ReceivedAt: now,
		Data: baseData("rev_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"),
	}
	if err := repository.Store(ctx, finalized); err != nil {
		t.Fatalf("Store(finalized) error = %v", err)
	}
	if err := repository.Store(ctx, finalized); err != nil {
		t.Fatalf("Store(duplicate finalized) error = %v", err)
	}
	var status, thumbnailStatus, revision string
	var outboxCount int
	if err := pool.QueryRow(ctx, `
		SELECT projection_status, thumbnail_status, revision FROM device_local_recordings
		WHERE device_registry_id=$1 AND recording_id='rec_0123456789abcdef0123456789abcdef'
	`, deviceID).Scan(&status, &thumbnailStatus, &revision); err != nil {
		t.Fatalf("read finalized projection: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM local_recording_thumbnail_outbox`).Scan(&outboxCount); err != nil {
		t.Fatalf("count thumbnail outbox: %v", err)
	}
	if status != "active" || thumbnailStatus != "pending" || outboxCount != 1 {
		t.Fatalf("projection=%s/%s outbox=%d", status, thumbnailStatus, outboxCount)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE device_local_recordings
		SET thumbnail_status='ready', thumbnail_object_key='private/thumbnail.jpg'
		WHERE device_registry_id=$1
	`, deviceID); err != nil {
		t.Fatalf("mark thumbnail projection ready: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE local_recording_thumbnail_outbox SET status='ready'`); err != nil {
		t.Fatalf("mark thumbnail outbox ready: %v", err)
	}

	conflict := finalized
	conflict.ID = "018f0000-0000-7000-8000-000000000092"
	conflict.EventSeq = 2
	conflict.Data = baseData("rev_1123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if err := repository.Store(ctx, conflict); err != nil {
		t.Fatalf("Store(conflict) error = %v", err)
	}
	var conflictingRevision *string
	if err := pool.QueryRow(ctx, `
		SELECT projection_status, revision, conflicting_revision
		FROM device_local_recordings WHERE device_registry_id=$1
	`, deviceID).Scan(&status, &revision, &conflictingRevision); err != nil {
		t.Fatalf("read conflict projection: %v", err)
	}
	if status != "conflict" || !strings.HasPrefix(revision, "rev_0") ||
		conflictingRevision == nil || !strings.HasPrefix(*conflictingRevision, "rev_1") {
		t.Fatalf("conflict projection=%s revision=%s conflicting=%v", status, revision, conflictingRevision)
	}

	deleted := events.Event{
		ID: "018f0000-0000-7000-8000-000000000093", DeviceSN: "fdr-0001",
		EventType: "recording_deleted", BootID: "boot-1", EventSeq: 3,
		MonotonicMS: &monotonic, ReceivedAt: now.Add(time.Second),
		Data: map[string]any{
			"contract": "sd-recording-catalog.v1", "type": "recording_deleted",
			"recording_id": "rec_0123456789abcdef0123456789abcdef",
			"revision":     revision,
		},
	}
	if err := repository.Store(ctx, deleted); err != nil {
		t.Fatalf("Store(deleted) error = %v", err)
	}
	delayed := finalized
	delayed.ID = "018f0000-0000-7000-8000-000000000094"
	delayed.EventSeq = 4
	if err := repository.Store(ctx, delayed); err != nil {
		t.Fatalf("Store(delayed finalized) error = %v", err)
	}
	var tombstones int
	var deletionStatus string
	if err := pool.QueryRow(ctx, `
		SELECT projection_status FROM device_local_recordings WHERE device_registry_id=$1
	`, deviceID).Scan(&status); err != nil {
		t.Fatalf("read deleted projection: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM local_recording_thumbnail_outbox`).Scan(&deletionStatus); err != nil {
		t.Fatalf("read thumbnail deletion outbox: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM device_local_recording_tombstones`).Scan(&tombstones); err != nil {
		t.Fatalf("count tombstones: %v", err)
	}
	if status != "deleted" || tombstones != 1 || deletionStatus != "delete_pending" {
		t.Fatalf("deleted projection status=%s tombstones=%d outbox=%s", status, tombstones, deletionStatus)
	}
}

func TestLocalRecordingReconciliationDeletesOnlyAfterCompleteGeneration(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatalf("ApplyMigrations() error = %v", err)
	}
	applyProductSeeds(t, ctx, pool)
	deviceID := "018f0000-0000-7000-8000-000000000020"
	bindingID := "018f0000-0000-7000-8000-000000000021"
	generation := "018f0000-0000-7000-8000-000000000022"
	if _, err := pool.Exec(ctx, `
		INSERT INTO device_registry (dp_profile_id, id, device_sn, thing_name, pid, status)
		VALUES ('40000000-0000-0000-0000-000000000001', $1,'fdr-0002','fdr-0002','feeder-x1','active')
	`, deviceID); err != nil {
		t.Fatalf("seed reconciliation device: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO device_bindings (
			id, device_registry_id, internal_app_id, binding_external_user_id, status
		) VALUES ($1,$2,'livpet-staging','user-2','active')
	`, bindingID, deviceID); err != nil {
		t.Fatalf("seed reconciliation binding: %v", err)
	}
	for index, recordingID := range []string{
		"rec_0123456789abcdef0123456789abcdef",
		"rec_1123456789abcdef0123456789abcdef",
	} {
		id := []string{
			"018f0000-0000-7000-8000-000000000023",
			"018f0000-0000-7000-8000-000000000024",
		}[index]
		if _, err := pool.Exec(ctx, `
			INSERT INTO device_local_recordings (
				id, device_registry_id, recording_id, revision, start_at, end_at,
				recording_date, duration_ms, size_bytes, recording_type, codec,
				width, height, fps_num, fps_den, playable, seekable,
				thumbnail_status, thumbnail_etag, thumbnail_size_bytes
			) VALUES (
				$1,$2,$3,$4,'2026-08-26T08:32:00Z','2026-08-26T08:33:00Z',
				'2026-08-26',60000,1024,'event','h264',1920,1080,25,1,true,true,
				'pending',$5,1234
			)
		`, id, deviceID, recordingID,
			"rev_"+strings.Repeat(string(rune('0'+index)), 64),
			"sha256:"+strings.Repeat(string(rune('a'+index)), 64)); err != nil {
			t.Fatalf("seed projection %d: %v", index, err)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO local_recording_reconciliations (
			device_registry_id, generation, status, next_attempt_at
		) VALUES ($1,$2,'processing',now())
	`, deviceID, generation); err != nil {
		t.Fatalf("seed reconciliation state: %v", err)
	}
	repository, _ := NewMediaRepository(pool)
	task := media.LocalReconciliationTask{
		DeviceID: deviceID, DeviceSN: "fdr-0002", InternalAppID: "livpet-staging",
		Generation: generation,
	}
	next := "rec_0123456789abcdef0123456789abcdef"
	page := media.DeviceCatalogPage{
		Contract:   media.LocalRecordingCatalogContractV1,
		NextCursor: &next,
		Items: []media.DeviceCatalogItem{{
			RecordingID: next,
			Revision:    "rev_" + strings.Repeat("0", 64),
			StartAt:     "2026-08-26T08:32:00Z", EndAt: "2026-08-26T08:33:00Z",
			DurationMS: 60000, SizeBytes: 1024, Type: "event", Codec: "h264",
			Width: 1920, Height: 1080, FPSNum: 25, FPSDen: 1, Playable: true, Seekable: true,
			Thumbnail: media.DeviceCatalogThumbnail{
				Status: "pending", ETag: "sha256:" + strings.Repeat("a", 64), SizeBytes: 1234,
			},
		}},
	}
	if err := repository.ApplyLocalReconciliationPage(ctx, task, page); err != nil {
		t.Fatalf("ApplyLocalReconciliationPage(first) error = %v", err)
	}
	var active int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM device_local_recordings
		WHERE device_registry_id=$1 AND projection_status='active'
	`, deviceID).Scan(&active); err != nil || active != 2 {
		t.Fatalf("active after partial scan = %d err=%v", active, err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO device_local_recordings (
			id, device_registry_id, recording_id, revision, start_at, end_at,
			recording_date, duration_ms, size_bytes, recording_type, codec,
			width, height, fps_num, fps_den, playable, seekable,
			thumbnail_status, thumbnail_etag, thumbnail_size_bytes, created_at
		) VALUES (
			'018f0000-0000-7000-8000-000000000025',$1,
			'rec_2123456789abcdef0123456789abcdef',$2,
			'2026-08-26T09:00:00Z','2026-08-26T09:01:00Z','2026-08-26',60000,1024,
			'event','h264',1920,1080,25,1,true,true,'pending',$3,1234,now()+interval '1 second'
		)
	`, deviceID, "rev_"+strings.Repeat("2", 64), "sha256:"+strings.Repeat("c", 64)); err != nil {
		t.Fatalf("insert concurrent new projection: %v", err)
	}
	task.Cursor = next
	if err := repository.ApplyLocalReconciliationPage(ctx, task, media.DeviceCatalogPage{
		Contract: media.LocalRecordingCatalogContractV1,
	}); err != nil {
		t.Fatalf("ApplyLocalReconciliationPage(final) error = %v", err)
	}
	var activeIDs []string
	rows, err := pool.Query(ctx, `
		SELECT recording_id FROM device_local_recordings
		WHERE device_registry_id=$1 AND projection_status='active'
	`, deviceID)
	if err != nil {
		t.Fatalf("list active reconciled recordings: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan active id: %v", err)
		}
		activeIDs = append(activeIDs, id)
	}
	if len(activeIDs) != 2 ||
		!(activeIDs[0] == next || activeIDs[1] == next) ||
		!(activeIDs[0] == "rec_2123456789abcdef0123456789abcdef" ||
			activeIDs[1] == "rec_2123456789abcdef0123456789abcdef") {
		t.Fatalf("active IDs after complete scan = %v", activeIDs)
	}
}
