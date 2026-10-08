package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/media"
)

func (repository *MediaRepository) ClaimPetPhoto(ctx context.Context) (media.PetPhotoTask, error) {
	var task media.PetPhotoTask
	err := repository.pool.QueryRow(ctx, `
        WITH due AS (
            SELECT p.id FROM device_pet_photos p
            JOIN device_bindings b ON b.id=p.device_binding_id
            WHERE p.status IN ('pending','uploading','verified','finalized','cleared','expiring')
              AND p.next_attempt_at<=now() AND (p.status<>'cleared' OR p.expires_at<=now() OR b.status<>'active')
            ORDER BY p.next_attempt_at,p.id FOR UPDATE OF p SKIP LOCKED LIMIT 1
        ), claimed AS (
            UPDATE device_pet_photos p SET attempts=p.attempts+1,
                status=CASE WHEN (p.expires_at<=now() OR EXISTS (SELECT 1 FROM device_bindings b WHERE b.id=p.device_binding_id AND b.status<>'active')) AND p.status IN ('pending','uploading','verified')
                    THEN 'expiring' ELSE p.status END,
                next_attempt_at=now()+interval '30 seconds'
            FROM due WHERE p.id=due.id RETURNING p.*
        )
        SELECT p.id::text,p.device_registry_id::text,d.device_sn,b.internal_app_id,
            p.size_bytes,p.sha256,p.status,COALESCE(p.object_key,''),
            COALESCE(p.image_url,''),p.attempts,p.expires_at<=now(),b.status<>'active'
        FROM claimed p JOIN device_registry d ON d.id=p.device_registry_id
        JOIN device_bindings b ON b.id=p.device_binding_id
    `).Scan(&task.ID, &task.DeviceID, &task.DeviceSN, &task.InternalAppID,
		&task.SizeBytes, &task.SHA256, &task.Status, &task.ObjectKey, &task.ImageURL, &task.Attempts, &task.Expired, &task.Revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return media.PetPhotoTask{}, media.ErrPetPhotoTaskNotFound
	}
	if err != nil {
		return media.PetPhotoTask{}, fmt.Errorf("claim pet photo: %w", err)
	}
	return task, nil
}

func (repository *MediaRepository) SetPetPhotoUploading(ctx context.Context, id, key string) error {
	result, err := repository.pool.Exec(ctx, `
        UPDATE device_pet_photos SET status='uploading',object_key=$2,
            next_attempt_at=now()+interval '5 seconds'
        WHERE id=$1::uuid AND status='pending' AND expires_at>now()
    `, id, key)
	if err != nil {
		return fmt.Errorf("set pet photo uploading: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("pet photo pending transaction not found")
	}
	return nil
}

func (repository *MediaRepository) SetPetPhotoVerified(ctx context.Context, id, key, imageURL string) error {
	result, err := repository.pool.Exec(ctx, `
        UPDATE device_pet_photos SET status='verified',object_key=$2,image_url=$3,
            next_attempt_at=now()
        WHERE id=$1::uuid AND status='uploading' AND object_key=$2 AND expires_at>now()
    `, id, key, imageURL)
	if err != nil {
		return fmt.Errorf("set pet photo verified: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("pet photo upload transaction not found")
	}
	return nil
}

func (repository *MediaRepository) RetryPetPhoto(ctx context.Context, id, reason string) error {
	result, err := repository.pool.Exec(ctx, `
        UPDATE device_pet_photos SET
            status=CASE WHEN status='uploading' AND attempts%3=0 AND expires_at>now() THEN 'pending' ELSE status END,
            next_attempt_at=now()+(LEAST(attempts*5,300)::text || ' seconds')::interval
        WHERE id=$1::uuid AND status IN ('pending','uploading','verified','finalized','expiring')
    `, id)
	if err != nil {
		return fmt.Errorf("retry pet photo %s: %w", reason, err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("pet photo retry transaction not found")
	}
	return nil
}

func (repository *MediaRepository) GetPetPhoto(ctx context.Context, bindingID, deviceSN, mediaID string) (media.PetPhoto, error) {
	var photo media.PetPhoto
	err := repository.pool.QueryRow(ctx, `
        SELECT p.id::text,p.object_key,p.sha256,p.size_bytes
        FROM device_pet_photos p
        JOIN device_registry d ON d.id=p.device_registry_id AND d.device_sn=$2 AND d.status='active'
        JOIN device_bindings b ON b.id=p.device_binding_id AND b.id=$1::uuid AND b.status='active'
        WHERE p.id=$3::uuid AND p.status IN ('verified','finalized','cleared') AND p.expires_at>now()
    `, bindingID, deviceSN, mediaID).Scan(&photo.ID, &photo.ObjectKey, &photo.SHA256, &photo.SizeBytes)
	if errors.Is(err, pgx.ErrNoRows) {
		return media.PetPhoto{}, media.ErrPetPhotoNotFound
	}
	if err != nil {
		return media.PetPhoto{}, fmt.Errorf("read bound pet photo: %w", err)
	}
	return photo, nil
}

func (repository *MediaRepository) SetPetPhotoCleared(ctx context.Context, id string) error {
	result, err := repository.pool.Exec(ctx, `
        UPDATE device_pet_photos SET status='cleared' WHERE id=$1::uuid AND status='finalized'
    `, id)
	if err != nil {
		return fmt.Errorf("clear pet photo device cache: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("finalized pet photo not found")
	}
	return nil
}

func (repository *MediaRepository) SetPetPhotoDead(ctx context.Context, id string) error {
	result, err := repository.pool.Exec(ctx, `UPDATE device_pet_photos SET status='dead' WHERE id=$1::uuid AND status='expiring'`, id)
	if err != nil {
		return fmt.Errorf("expire unreported pet photo: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("expiring pet photo transaction not found")
	}
	return nil
}

func (repository *MediaRepository) SetPetPhotoExpired(ctx context.Context, id string) error {
	result, err := repository.pool.Exec(ctx, `UPDATE device_pet_photos SET status='expired' WHERE id=$1::uuid AND status IN ('cleared','finalized')`, id)
	if err != nil {
		return fmt.Errorf("expire private pet photo resource: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("cleared pet photo transaction not found")
	}
	return nil
}
