package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/events"
)

func TestPetPhotoTombstonePurgeIsBoundedAndPreservesLiveOrRecentRows(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	applyProductSeeds(t, ctx, pool)
	const deviceID = "018f0000-0000-7000-8000-000000000010"
	const bindingID = "018f0000-0000-7000-8000-000000000011"
	if _, err := pool.Exec(ctx, `INSERT INTO device_registry (dp_profile_id,id,device_sn,thing_name,pid,status)
        VALUES ('40000000-0000-0000-0000-000000000001',$1,'fdr-retention','fdr-retention','feeder-x1','active')`, deviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO device_bindings (id,device_registry_id,internal_app_id,binding_external_user_id,status)
        VALUES ($1,$2,'livpet-staging','user-1','active')`, bindingID, deviceID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	ids := []string{
		"018f0000-0000-7000-8000-000000000012", // expired old
		"018f0000-0000-7000-8000-000000000013", // dead old
		"018f0000-0000-7000-8000-000000000014", // expired recent
		"018f0000-0000-7000-8000-000000000015", // cleared old
		"018f0000-0000-7000-8000-000000000016", // pending old
	}
	statuses := []string{"expired", "dead", "expired", "cleared", "pending"}
	ages := []time.Duration{10 * 24 * time.Hour, 9 * 24 * time.Hour, 6 * 24 * time.Hour, 10 * 24 * time.Hour, 10 * 24 * time.Hour}
	for index, id := range ids {
		objectKey, imageURL := any(nil), any(nil)
		if statuses[index] == "cleared" {
			objectKey, imageURL = "private/pet.jpg", "https://gateway.example.test/pet-photos/"+id
		}
		if _, err := pool.Exec(ctx, `INSERT INTO device_pet_photos
            (id,device_registry_id,device_binding_id,size_bytes,sha256,status,object_key,image_url,updated_at)
            VALUES ($1,$2,$3,100,$4,$5,$6,$7,$8)`, id, deviceID, bindingID,
			"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", statuses[index], objectKey, imageURL, now.Add(-ages[index])); err != nil {
			t.Fatal(err)
		}
	}
	repo, err := NewMediaRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	cutoff := now.Add(-7 * 24 * time.Hour)
	if count, err := repo.PurgePetPhotoTombstones(ctx, cutoff, 1); err != nil || count != 1 {
		t.Fatalf("first bounded purge=%d err=%v", count, err)
	}
	if count, err := repo.PurgePetPhotoTombstones(ctx, cutoff, 1); err != nil || count != 1 {
		t.Fatalf("second bounded purge=%d err=%v", count, err)
	}
	if count, err := repo.PurgePetPhotoTombstones(ctx, cutoff, 100); err != nil || count != 0 {
		t.Fatalf("third purge=%d err=%v", count, err)
	}
	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM device_pet_photos`).Scan(&remaining); err != nil || remaining != 3 {
		t.Fatalf("remaining=%d err=%v", remaining, err)
	}
}

func TestPetPhotoHourlyQuotaRejectsAndSchedulesDeviceCancellation(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	applyProductSeeds(t, ctx, pool)
	const deviceID = "018f0000-0000-7000-8000-000000000030"
	const bindingID = "018f0000-0000-7000-8000-000000000031"
	if _, err := pool.Exec(ctx, `INSERT INTO device_registry (dp_profile_id,id,device_sn,thing_name,pid,status)
        VALUES ('40000000-0000-0000-0000-000000000001',$1,'fdr-hourly','fdr-hourly','feeder-x1','active')`, deviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO device_bindings (id,device_registry_id,internal_app_id,binding_external_user_id,status)
        VALUES ($1,$2,'livpet-staging','user-1','active')`, bindingID, deviceID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 120; i++ {
		id := fmt.Sprintf("018f0000-0000-7000-8000-%012x", 0x100+i)
		if _, err := pool.Exec(ctx, `INSERT INTO device_pet_photos
            (id,device_registry_id,device_binding_id,size_bytes,sha256,status,object_key,image_url,created_at)
            VALUES ($1,$2,$3,100,$4,'cleared','private/pet.jpg','https://example.test/pet.jpg',now()-interval '31 seconds')`,
			id, deviceID, bindingID, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
			// Completed rows need a verified object key and URL.
			t.Fatal(err)
		}
	}
	repo, _ := NewEventRepository(pool)
	ready := events.Event{DeviceSN: "fdr-hourly", EventType: "pet_photo_ready_v1", ReceivedAt: time.Now().UTC(), Data: map[string]any{
		"trigger_id": "018f0000-0000-7000-8000-000000000032", "size_bytes": float64(100),
		"sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}
	if err := repo.CaptureReady(ctx, ready); err != events.ErrPetPhotoRateLimited {
		t.Fatalf("hourly quota err=%v", err)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM device_pet_photos WHERE id=$1`, ready.Data["trigger_id"]).Scan(&status); err != nil || status != "expiring" {
		t.Fatalf("status=%s err=%v", status, err)
	}
}

func TestExpiredFinalizedPetPhotoTransitionsToCancellationAfterObjectDeletion(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	applyProductSeeds(t, ctx, pool)
	const deviceID = "018f0000-0000-7000-8000-000000000040"
	const bindingID = "018f0000-0000-7000-8000-000000000041"
	const photoID = "018f0000-0000-7000-8000-000000000042"
	if _, err := pool.Exec(ctx, `INSERT INTO device_registry (dp_profile_id,id,device_sn,thing_name,pid,status)
        VALUES ('40000000-0000-0000-0000-000000000001',$1,'fdr-expiry','fdr-expiry','feeder-x1','active')`, deviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO device_bindings (id,device_registry_id,internal_app_id,binding_external_user_id,status)
        VALUES ($1,$2,'livpet-staging','user-1','active')`, bindingID, deviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO device_pet_photos
        (id,device_registry_id,device_binding_id,size_bytes,sha256,status,object_key,image_url,expires_at)
        VALUES ($1,$2,$3,100,$4,'finalized','private/expired.jpg','https://example.test/photo',now()-interval '1 second')`,
		photoID, deviceID, bindingID, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	repo, _ := NewMediaRepository(pool)
	if err := repo.SetPetPhotoExpiring(ctx, photoID); err != nil {
		t.Fatal(err)
	}
	task, err := repo.ClaimPetPhoto(ctx)
	if err != nil || task.Status != "expiring" || !task.Expired {
		t.Fatalf("task=%#v err=%v", task, err)
	}
	if err := repo.SetPetPhotoDead(ctx, photoID); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM device_pet_photos WHERE id=$1`, photoID).Scan(&status); err != nil || status != "dead" {
		t.Fatalf("status=%s err=%v", status, err)
	}
}
