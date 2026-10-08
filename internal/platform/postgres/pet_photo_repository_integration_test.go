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
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM device_pet_photos`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM device_events`).Scan(&count); err != nil || count != 0 {
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
