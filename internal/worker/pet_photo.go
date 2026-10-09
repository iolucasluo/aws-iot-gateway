package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/device"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/media"
)

const petPhotoMaxBytes = 256 * 1024

type PetPhotoWorker struct {
	repository                media.PetPhotoRepository
	store                     LocalThumbnailStore
	commander                 media.Commander
	environment, publicOrigin string
}

func NewPetPhotoWorker(repository media.PetPhotoRepository, store LocalThumbnailStore,
	commander media.Commander, environment, publicOrigin string) (*PetPhotoWorker, error) {
	parsed, err := url.Parse(publicOrigin)
	if repository == nil || store == nil || commander == nil || strings.TrimSpace(environment) == "" ||
		err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("pet photo worker requires private storage and HTTPS public origin")
	}
	return &PetPhotoWorker{repository: repository, store: store, commander: commander,
		environment: environment, publicOrigin: publicOrigin}, nil
}

func (worker *PetPhotoWorker) PollOnce(ctx context.Context) error {
	if worker == nil {
		return nil
	}
	task, err := worker.repository.ClaimPetPhoto(ctx)
	if errors.Is(err, media.ErrPetPhotoTaskNotFound) {
		_, err = worker.repository.PurgePetPhotoTombstones(
			ctx, time.Now().UTC().Add(-7*24*time.Hour), 100)
		return err
	}
	if err != nil {
		return err
	}
	if task.ID == "" || task.DeviceID == "" || task.DeviceSN == "" || task.InternalAppID == "" ||
		task.SizeBytes <= 0 || task.SizeBytes > petPhotoMaxBytes || len(task.SHA256) != 64 {
		return worker.retry(ctx, task.ID, "invalid pet photo task")
	}
	if task.Revoked {
		if task.ObjectKey != "" {
			if err := worker.store.Delete(ctx, task.ObjectKey); err != nil {
				return worker.retry(ctx, task.ID, "revoked pet photo cleanup failed")
			}
		}
		if task.Status == "finalized" || task.Status == "cleared" {
			return worker.repository.SetPetPhotoExpired(ctx, task.ID)
		}
		return worker.repository.SetPetPhotoDead(ctx, task.ID)
	}
	if task.Expired || task.Status == "expiring" {
		switch task.Status {
		case "finalized":
			if task.ObjectKey != "" {
				if err := worker.store.Delete(ctx, task.ObjectKey); err != nil {
					return worker.retry(ctx, task.ID, "expired pet photo object cleanup failed")
				}
			}
			return worker.repository.SetPetPhotoExpiring(ctx, task.ID)
		case "cleared":
			if task.ObjectKey != "" {
				if err := worker.store.Delete(ctx, task.ObjectKey); err != nil {
					return worker.retry(ctx, task.ID, "pet photo expiration cleanup failed")
				}
			}
			return worker.repository.SetPetPhotoExpired(ctx, task.ID)
		default:
			// Expiry is a privacy boundary even when the device is offline: remove
			// the private object before retrying the idempotent local cancel.
			if task.ObjectKey != "" {
				if err := worker.store.Delete(ctx, task.ObjectKey); err != nil {
					return worker.retry(ctx, task.ID, "pet photo cancellation cleanup failed")
				}
			}
			value, err := json.Marshal(map[string]any{
				"contract": "pet-photo.v1", "op": "cancel", "trigger_id": task.ID,
				"media_id": task.ID,
			})
			if err != nil {
				return worker.retry(ctx, task.ID, "pet photo cancellation encoding failed")
			}
			if err := worker.send(ctx, task, device.CommandInput{Code: "pet_photo_v1", Value: value}); err != nil {
				return worker.retry(ctx, task.ID, "pet photo cancellation failed")
			}
			return worker.repository.SetPetPhotoDead(ctx, task.ID)
		}
	}
	switch task.Status {
	case "pending":
		key := worker.environment + "/pet-photos/" + task.DeviceID + "/" + task.ID + ".jpg"
		uploadURL, err := worker.store.PutURLWithSize(ctx, key, "image/jpeg", task.SizeBytes, 15*time.Minute)
		if err != nil {
			return worker.retry(ctx, task.ID, "pet photo presign failed")
		}
		value, err := json.Marshal(map[string]any{
			"contract": "pet-photo.v1", "op": "upload", "trigger_id": task.ID,
			"sha256": task.SHA256, "size_bytes": task.SizeBytes, "upload_url": uploadURL,
		})
		if err != nil {
			return worker.retry(ctx, task.ID, "pet photo command encoding failed")
		}
		if err := worker.send(ctx, task, device.CommandInput{Code: "pet_photo_v1", Value: value,
			RedactFields: []string{"upload_url"}}); err != nil {
			return worker.retry(ctx, task.ID, "pet photo upload command failed")
		}
		return worker.repository.SetPetPhotoUploading(ctx, task.ID, key)
	case "uploading":
		if task.ObjectKey == "" {
			return worker.retry(ctx, task.ID, "pet photo key missing")
		}
		size, contentType, err := worker.store.Stat(ctx, task.ObjectKey)
		if err != nil {
			return worker.retry(ctx, task.ID, "pet photo object not yet available")
		}
		if size != task.SizeBytes || contentType != "image/jpeg" {
			return worker.retry(ctx, task.ID, "pet photo object metadata mismatch")
		}
		payload, err := worker.store.Download(ctx, task.ObjectKey, petPhotoMaxBytes)
		if err != nil || int64(len(payload)) != task.SizeBytes ||
			!media.ValidateLocalThumbnailJPEG(payload, "sha256:"+task.SHA256) {
			return worker.retry(ctx, task.ID, "pet photo JPEG/hash mismatch")
		}
		imageURL := worker.publicOrigin + "/api/v1/openapi/devices/" + task.DeviceSN + "/pet-photos/" + task.ID
		return worker.repository.SetPetPhotoVerified(ctx, task.ID, task.ObjectKey, imageURL)
	case "verified":
		value, err := json.Marshal(map[string]any{
			"contract": "pet-photo.v1", "op": "commit", "trigger_id": task.ID,
			"media_id": task.ID, "image_url": task.ImageURL,
		})
		if err != nil {
			return worker.retry(ctx, task.ID, "pet photo commit encoding failed")
		}
		if err := worker.send(ctx, task, device.CommandInput{Code: "pet_photo_v1", Value: value}); err != nil {
			return worker.retry(ctx, task.ID, "pet photo commit command failed")
		}
		return nil
	case "finalized":
		return worker.acknowledge(ctx, task)
	default:
		return worker.retry(ctx, task.ID, "unexpected pet photo task state")
	}
}

func (worker *PetPhotoWorker) send(ctx context.Context, task media.PetPhotoTask, input device.CommandInput) error {
	result, err := worker.commander.SendCommands(ctx, task.InternalAppID, task.DeviceID, task.DeviceSN,
		[]device.CommandInput{input}, true)
	if err != nil || len(result.Results) != 1 || result.Results[0].Status != "success" {
		return fmt.Errorf("pet photo device command not accepted")
	}
	return nil
}

func (worker *PetPhotoWorker) retry(ctx context.Context, id, reason string) error {
	if err := worker.repository.RetryPetPhoto(ctx, id, reason); err != nil {
		return fmt.Errorf("persist pet photo retry: %w", err)
	}
	return errors.New(reason)
}

func (worker *PetPhotoWorker) acknowledge(ctx context.Context, task media.PetPhotoTask) error {
	value, err := json.Marshal(map[string]any{
		"contract": "pet-photo.v1", "op": "ack", "trigger_id": task.ID, "media_id": task.ID,
	})
	if err != nil {
		return worker.retry(ctx, task.ID, "pet photo acknowledgement encoding failed")
	}
	if err := worker.send(ctx, task, device.CommandInput{Code: "pet_photo_v1", Value: value}); err != nil {
		return worker.retry(ctx, task.ID, "pet photo acknowledgement failed")
	}
	return worker.repository.SetPetPhotoCleared(ctx, task.ID)
}
