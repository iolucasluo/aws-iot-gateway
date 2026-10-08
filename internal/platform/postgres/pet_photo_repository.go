package postgres

import (
	"context"
	"fmt"

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
	size := int64(sizeFloat)
	_, err := repository.pool.Exec(ctx, `
        INSERT INTO device_pet_photos (id,device_registry_id,device_binding_id,size_bytes,sha256)
        SELECT $1::uuid,d.id,b.id,$2,$3
        FROM device_registry d JOIN device_bindings b ON b.device_registry_id=d.id AND b.status='active'
        WHERE d.device_sn=$4 AND d.status='active'
        ON CONFLICT (id) DO NOTHING
    `, triggerID, size, sha256, event.DeviceSN)
	if err != nil {
		return fmt.Errorf("record pet photo readiness: %w", err)
	}
	var matches bool
	if err := repository.pool.QueryRow(ctx, `
        SELECT EXISTS(
            SELECT 1 FROM device_pet_photos p
            JOIN device_registry d ON d.id=p.device_registry_id
            JOIN device_bindings b ON b.id=p.device_binding_id AND b.status='active'
            WHERE p.id=$1::uuid AND d.device_sn=$2 AND d.status='active'
              AND p.size_bytes=$3 AND p.sha256=$4 AND p.expires_at>now()
        )
    `, triggerID, event.DeviceSN, size, sha256).Scan(&matches); err != nil {
		return fmt.Errorf("verify pet photo readiness: %w", err)
	}
	if !matches {
		return fmt.Errorf("pet photo readiness conflicts with device, binding or content")
	}
	return nil
}

// VerifyFinal gates the user-visible event on a verified object and the same
// currently active device binding. Never trust an image URL supplied by MQTT.
func (repository *EventRepository) VerifyFinal(ctx context.Context, event events.Event) error {
	mediaID, _ := event.Data["media_id"].(string)
	triggerID, _ := event.Data["trigger_id"].(string)
	imageURL, _ := event.Data["image_url"].(string)
	if repository == nil || repository.pool == nil || mediaID == "" || mediaID != triggerID || imageURL == "" {
		return fmt.Errorf("invalid final pet photo event")
	}
	var verified bool
	err := repository.pool.QueryRow(ctx, `
        SELECT EXISTS(SELECT 1 FROM device_pet_photos p
            JOIN device_registry d ON d.id=p.device_registry_id AND d.status='active'
            JOIN device_bindings b ON b.id=p.device_binding_id AND b.status='active'
            WHERE p.id=$1::uuid AND d.device_sn=$2 AND p.status IN ('verified','finalized','cleared')
              AND p.object_key IS NOT NULL AND p.image_url=$3 AND p.expires_at>now())
    `, mediaID, event.DeviceSN, imageURL).Scan(&verified)
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
	if mediaID == "" || mediaID != triggerID || imageURL == "" {
		return fmt.Errorf("invalid final pet photo")
	}
	result, err := transaction.Exec(ctx, `
        UPDATE device_pet_photos p SET status=CASE WHEN p.status='cleared' THEN 'cleared' ELSE 'finalized' END
        FROM device_bindings b
        WHERE p.id=$1::uuid AND p.device_registry_id=$2::uuid AND p.device_binding_id=b.id
          AND b.status='active' AND p.status IN ('verified','finalized','cleared')
          AND p.object_key IS NOT NULL AND p.image_url=$3 AND p.expires_at>now()
    `, mediaID, deviceID, imageURL)
	if err != nil {
		return fmt.Errorf("finalize pet photo in event transaction: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("pet photo cannot be finalized for device")
	}
	return nil
}
