package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/events"
)

// CaptureReady records a private media transaction; it deliberately does not
// call EventRepository.Store, which would expose internal readiness to the App
// event list and webhook outbox before the photo exists.
func (repository *EventRepository) CaptureReady(ctx context.Context, event events.Event) error {
	triggerID, _ := event.Data["trigger_id"].(string)
	sha256, _ := event.Data["sha256"].(string)
	sizeFloat, ok := event.Data["size_bytes"].(float64)
	if repository == nil || repository.pool == nil || triggerID == "" || len(sha256) != 64 ||
		!ok || sizeFloat < 1 || sizeFloat > 256*1024 || sizeFloat != float64(int64(sizeFloat)) {
		return fmt.Errorf("invalid pet photo readiness")
	}
	var capturedAt any
	if raw, found := event.Data["captured_at_unix_ms"]; found {
		capturedMS, valid := raw.(float64)
		if !valid || capturedMS <= 0 || capturedMS > 9007199254740991 ||
			capturedMS != float64(int64(capturedMS)) || event.ReceivedAt.IsZero() {
			return fmt.Errorf("invalid pet photo capture time")
		}
		captured := time.UnixMilli(int64(capturedMS)).UTC()
		if captured.Before(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)) ||
			captured.After(event.ReceivedAt.Add(5*time.Minute)) {
			return fmt.Errorf("pet photo capture time outside trusted window")
		}
		capturedAt = captured
	}
	size := int64(sizeFloat)
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin pet photo readiness: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var deviceID, bindingID string
	if err := tx.QueryRow(ctx, `
        SELECT d.id::text,b.id::text FROM device_registry d
        JOIN device_bindings b ON b.device_registry_id=d.id AND b.status='active'
        WHERE d.device_sn=$1 AND d.status='active' FOR UPDATE OF d
    `, event.DeviceSN).Scan(&deviceID, &bindingID); err != nil {
		return fmt.Errorf("pet photo requires active device binding: %w", err)
	}
	var same bool
	var status string
	err = tx.QueryRow(ctx, `
        SELECT device_registry_id=$2::uuid AND device_binding_id=$3::uuid AND size_bytes=$4
           AND sha256=$5 AND captured_at IS NOT DISTINCT FROM $6,
           status
        FROM device_pet_photos WHERE id=$1::uuid
    `, triggerID, deviceID, bindingID, size, sha256, capturedAt).Scan(&same, &status)
	if err == nil {
		if !same {
			return fmt.Errorf("pet photo readiness conflicts with binding or content")
		}
		if status == "expiring" || status == "dead" {
			return events.ErrPetPhotoRateLimited
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("look up existing pet photo readiness: %w", err)
	}
	var limited bool
	if err := tx.QueryRow(ctx, `
        SELECT EXISTS(SELECT 1 FROM device_pet_photos
          WHERE device_registry_id=$1::uuid AND status<>'expiring'
            AND created_at>=now()-interval '30 seconds')
          OR (SELECT count(*) FROM device_pet_photos
              WHERE device_registry_id=$1::uuid AND status<>'expiring'
                AND created_at>=now()-interval '1 hour')>=120
    `, deviceID).Scan(&limited); err != nil {
		return fmt.Errorf("check per-device pet photo rate: %w", err)
	}
	if limited {
		if _, err := tx.Exec(ctx, `
            INSERT INTO device_pet_photos
              (id,device_registry_id,device_binding_id,size_bytes,sha256,captured_at,status,expires_at)
            VALUES ($1::uuid,$2::uuid,$3::uuid,$4,$5,$6,'expiring',now())
        `, triggerID, deviceID, bindingID, size, sha256, capturedAt); err != nil {
			return fmt.Errorf("record rate-limited pet photo for cancellation: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit pet photo cancellation task: %w", err)
		}
		return events.ErrPetPhotoRateLimited
	}
	if _, err := tx.Exec(ctx, `
        INSERT INTO device_pet_photos (id,device_registry_id,device_binding_id,size_bytes,sha256,captured_at)
        VALUES ($1::uuid,$2::uuid,$3::uuid,$4,$5,$6)
    `, triggerID, deviceID, bindingID, size, sha256, capturedAt); err != nil {
		return fmt.Errorf("record pet photo readiness: %w", err)
	}
	return tx.Commit(ctx)
}

// VerifyFinal gates the user-visible event on a verified object and the same
// currently active device binding. Never trust an image URL supplied by MQTT.
func (repository *EventRepository) VerifyFinal(ctx context.Context, event events.Event) error {
	mediaID, _ := event.Data["media_id"].(string)
	triggerID, _ := event.Data["trigger_id"].(string)
	imageURL, _ := event.Data["image_url"].(string)
	var capturedAt any
	if raw, found := event.Data["captured_at_unix_ms"]; found {
		capturedMS, valid := raw.(float64)
		if !valid || capturedMS <= 0 || capturedMS > 9007199254740991 ||
			capturedMS != float64(int64(capturedMS)) {
			return fmt.Errorf("invalid final pet photo capture time")
		}
		capturedAt = time.UnixMilli(int64(capturedMS)).UTC()
	}
	if repository == nil || repository.pool == nil || mediaID == "" || mediaID != triggerID || imageURL == "" {
		return fmt.Errorf("invalid final pet photo event")
	}
	var verified bool
	err := repository.pool.QueryRow(ctx, `
        SELECT EXISTS(SELECT 1 FROM device_pet_photos p
            JOIN device_registry d ON d.id=p.device_registry_id AND d.status='active'
            JOIN device_bindings b ON b.id=p.device_binding_id AND b.status='active'
            WHERE p.id=$1::uuid AND d.device_sn=$2 AND p.status IN ('verified','finalized','cleared')
              AND p.object_key IS NOT NULL AND p.image_url=$3
              AND p.captured_at IS NOT DISTINCT FROM $4 AND p.expires_at>now())
    `, mediaID, event.DeviceSN, imageURL, capturedAt).Scan(&verified)
	if err != nil {
		return fmt.Errorf("verify final pet photo: %w", err)
	}
	if !verified {
		return fmt.Errorf("pet photo is unverified or not bound to this device")
	}
	return nil
}

// Called from EventRepository.Store's transaction so the worker cannot ACK
// and clear the device photo before the user-visible event commits.
func finalizePetPhoto(ctx context.Context, transaction pgx.Tx, event events.Event, deviceID string) error {
	mediaID, _ := event.Data["media_id"].(string)
	triggerID, _ := event.Data["trigger_id"].(string)
	imageURL, _ := event.Data["image_url"].(string)
	var capturedAt any
	if raw, found := event.Data["captured_at_unix_ms"]; found {
		capturedMS, valid := raw.(float64)
		if !valid || capturedMS <= 0 || capturedMS > 9007199254740991 ||
			capturedMS != float64(int64(capturedMS)) {
			return fmt.Errorf("invalid final pet photo capture time")
		}
		capturedAt = time.UnixMilli(int64(capturedMS)).UTC()
	}
	if mediaID == "" || mediaID != triggerID || imageURL == "" {
		return fmt.Errorf("invalid final pet photo")
	}
	result, err := transaction.Exec(ctx, `
        UPDATE device_pet_photos p SET status=CASE WHEN p.status='cleared' THEN 'cleared' ELSE 'finalized' END
        FROM device_bindings b
        WHERE p.id=$1::uuid AND p.device_registry_id=$2::uuid AND p.device_binding_id=b.id
          AND b.status='active' AND p.status IN ('verified','finalized','cleared')
          AND p.object_key IS NOT NULL AND p.image_url=$3
          AND p.captured_at IS NOT DISTINCT FROM $4 AND p.expires_at>now()
    `, mediaID, deviceID, imageURL, capturedAt)
	if err != nil {
		return fmt.Errorf("finalize pet photo in event transaction: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("pet photo cannot be finalized for device")
	}
	return nil
}
