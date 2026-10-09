package postgres

import (
	"context"
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/events"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/media"
)

var _ events.PetPhotoWorkflow = (*EventRepository)(nil)
var _ media.PetPhotoRepository = (*MediaRepository)(nil)
var _ media.PetPhotoReadRepository = (*MediaRepository)(nil)

func TestPetPhotoReadyIsPrivateIdempotentAndFinalRequiresVerifiedBinding(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	applyProductSeeds(t, ctx, pool)
	const deviceID = "018f0000-0000-7000-8000-000000000010"
	const bindingID = "018f0000-0000-7000-8000-000000000011"
	const triggerID = "018f0000-0000-7000-8000-000000000012"
	const imageURL = "https://gateway.example.test/api/v1/openapi/devices/fdr-0001/pet-photos/" + triggerID
	if _, err := pool.Exec(ctx, `INSERT INTO device_registry (dp_profile_id,id,device_sn,thing_name,pid,status)
       VALUES ('40000000-0000-0000-0000-000000000001',$1,'fdr-0001','fdr-0001','feeder-x1','active')`, deviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO device_bindings (id,device_registry_id,internal_app_id,binding_external_user_id,status)
       VALUES ($1,$2,'livpet-staging','user-1','active')`, bindingID, deviceID); err != nil {
		t.Fatal(err)
	}
	repo, _ := NewEventRepository(pool)
	ready := events.Event{DeviceSN: "fdr-0001", EventType: "pet_photo_ready_v1", Data: map[string]any{
		"trigger_id": triggerID, "size_bytes": float64(100), "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}
	if err := repo.CaptureReady(ctx, ready); err != nil {
		t.Fatal(err)
	}
	if err := repo.CaptureReady(ctx, ready); err != nil {
		t.Fatalf("same capture retry: %v", err)
	}
	other := ready
	other.Data = map[string]any{"trigger_id": "018f0000-0000-7000-8000-000000000019", "size_bytes": float64(100),
		"sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if err := repo.CaptureReady(ctx, other); err != events.ErrPetPhotoRateLimited {
		t.Fatalf("second capture within 30 seconds: %v", err)
	}
	if err := repo.CaptureReady(ctx, other); err != events.ErrPetPhotoRateLimited {
		t.Fatalf("rate-limited replay must remain idempotently denied: %v", err)
	}
	var rejectedStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM device_pet_photos WHERE id=$1`,
		other.Data["trigger_id"]).Scan(&rejectedStatus); err != nil || rejectedStatus != "expiring" {
		t.Fatalf("rate-limited photo must be queued for cancel: status=%q err=%v", rejectedStatus, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE device_pet_photos SET created_at=now()-interval '31 seconds' WHERE id=$1`, triggerID); err != nil {
		t.Fatal(err)
	}
	readyWithCaptureTime := ready
	readyWithCaptureTime.ReceivedAt = time.Now().UTC()
	readyWithCaptureTime.Data = map[string]any{"trigger_id": "018f0000-0000-7000-8000-00000000001a", "size_bytes": float64(100),
		"sha256":              "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"captured_at_unix_ms": float64(time.Now().UTC().Add(-time.Second).UnixMilli())}
	if err := repo.CaptureReady(ctx, readyWithCaptureTime); err != nil {
		t.Fatalf("capture after quota elapsed: %v", err)
	}
	if err := repo.CaptureReady(ctx, readyWithCaptureTime); err != nil {
		t.Fatalf("same timed capture is idempotent: %v", err)
	}
	bogusTimestamp := readyWithCaptureTime
	bogusTimestamp.Data = map[string]any{"trigger_id": "018f0000-0000-7000-8000-00000000001b", "size_bytes": float64(100),
		"sha256":              "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"captured_at_unix_ms": float64(time.Now().UTC().Add(24 * time.Hour).UnixMilli())}
	if err := repo.CaptureReady(ctx, bogusTimestamp); err == nil {
		t.Fatal("future capture timestamp accepted")
	}
	var storedCapture time.Time
	if err := pool.QueryRow(ctx, `SELECT captured_at FROM device_pet_photos WHERE id=$1`,
		readyWithCaptureTime.Data["trigger_id"]).Scan(&storedCapture); err != nil ||
		storedCapture.UnixMilli() != int64(readyWithCaptureTime.Data["captured_at_unix_ms"].(float64)) {
		t.Fatalf("stored capture=%v err=%v", storedCapture, err)
	}
	timedID := readyWithCaptureTime.Data["trigger_id"].(string)
	timedURL := "https://gateway.example.test/api/v1/openapi/devices/fdr-0001/pet-photos/" + timedID
	if _, err := pool.Exec(ctx, `UPDATE device_pet_photos SET status='verified',object_key='staging/pet-photos/timed.jpg',image_url=$2 WHERE id=$1`, timedID, timedURL); err != nil {
		t.Fatal(err)
	}
	timedFinal := events.Event{DeviceSN: "fdr-0001", EventType: "pet_detected", Data: map[string]any{
		"trigger_id": timedID, "media_id": timedID, "image_url": timedURL,
		"captured_at_unix_ms": readyWithCaptureTime.Data["captured_at_unix_ms"],
	}}
	if err := repo.VerifyFinal(ctx, timedFinal); err != nil {
		t.Fatalf("original timestamp must match readiness: %v", err)
	}
	timedFinal.ID = "018f0000-0000-7000-8000-00000000001c"
	timedFinal.BootID = "5ce0b7f2-1f0a-4ed7-9f06-bf6ce8393148"
	timedFinal.EventSeq = 13
	timedFinal.ReceivedAt = time.Now().UTC()
	timedFinal.Data["schema"] = "pet-detection.v1"
	capturedAt := storedCapture
	timedFinal.OccurredAt = &capturedAt
	if err := repo.Store(ctx, timedFinal); err != nil {
		t.Fatalf("store timed final event: %v", err)
	}
	var storedOccurredAt time.Time
	if err := pool.QueryRow(ctx, `SELECT occurred_at FROM device_events WHERE id=$1`, timedFinal.ID).Scan(&storedOccurredAt); err != nil ||
		storedOccurredAt.UnixMilli() != storedCapture.UnixMilli() {
		t.Fatalf("event time=%v err=%v", storedOccurredAt, err)
	}
	timedFinal.Data["captured_at_unix_ms"] = float64(time.Now().UTC().UnixMilli())
	if err := repo.VerifyFinal(ctx, timedFinal); err == nil {
		t.Fatal("forged final capture time accepted")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM device_pet_photos`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM device_events WHERE event_type='pet_photo_ready_v1'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("internal ready exposed: %d %v", count, err)
	}
	conflict := ready
	conflict.Data = map[string]any{"trigger_id": triggerID, "size_bytes": float64(101), "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if err := repo.CaptureReady(ctx, conflict); err == nil {
		t.Fatal("conflicting capture overwritten")
	}
	final := events.Event{DeviceSN: "fdr-0001", EventType: "pet_detected", Data: map[string]any{
		"trigger_id": triggerID, "media_id": triggerID, "image_url": imageURL,
	}}
	if err := repo.VerifyFinal(ctx, final); err == nil {
		t.Fatal("unverified image accepted")
	}
	if _, err := pool.Exec(ctx, `UPDATE device_pet_photos SET status='verified',object_key='staging/pet-photos/key.jpg',image_url=$2 WHERE id=$1`, triggerID, imageURL); err != nil {
		t.Fatal(err)
	}
	if err := repo.VerifyFinal(ctx, final); err != nil {
		t.Fatalf("verified image: %v", err)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM device_pet_photos WHERE id=$1`, triggerID).Scan(&status); err != nil || status != "verified" {
		t.Fatalf("preflight must not finalize before event store, status=%q err=%v", status, err)
	}
	final.ID = "018f0000-0000-7000-8000-000000000013"
	final.BootID = "5ce0b7f2-1f0a-4ed7-9f06-bf6ce8393148"
	final.EventSeq = 12
	final.ReceivedAt = time.Now().UTC()
	final.Data["schema"] = "pet-detection.v1"
	if err := repo.Store(ctx, final); err != nil {
		t.Fatalf("Store(final) error=%v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM device_pet_photos WHERE id=$1`, triggerID).Scan(&status); err != nil || status != "finalized" {
		t.Fatalf("stored event must finalize photo atomically, status=%q err=%v", status, err)
	}
	forged := final
	forged.Data = map[string]any{"trigger_id": triggerID, "media_id": triggerID, "image_url": imageURL + "/other"}
	if err := repo.VerifyFinal(ctx, forged); err == nil {
		t.Fatal("forged URL accepted")
	}
	if _, err := pool.Exec(ctx, `UPDATE device_bindings SET status='inactive' WHERE id=$1`, bindingID); err != nil {
		t.Fatal(err)
	}
	if err := repo.VerifyFinal(ctx, final); err == nil {
		t.Fatal("unbound photo accepted")
	}
}
