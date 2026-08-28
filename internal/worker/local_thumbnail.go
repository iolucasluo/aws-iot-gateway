package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/device"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/media"
)

const (
	localThumbnailUploadTTL = 15 * time.Minute
	localThumbnailMaxBytes  = int64(256 * 1024)
)

type LocalThumbnailStore interface {
	PutURLWithSize(context.Context, string, string, int64, time.Duration) (string, error)
	Stat(context.Context, string) (int64, string, error)
	Download(context.Context, string, int64) ([]byte, error)
	Delete(context.Context, string) error
}

type LocalThumbnailWorker struct {
	repository  media.LocalThumbnailRepository
	store       LocalThumbnailStore
	commander   media.Commander
	environment string
}

func NewLocalThumbnailWorker(
	repository media.LocalThumbnailRepository,
	store LocalThumbnailStore,
	commander media.Commander,
	environment string,
) (*LocalThumbnailWorker, error) {
	if repository == nil || store == nil || commander == nil || strings.TrimSpace(environment) == "" {
		return nil, fmt.Errorf("local thumbnail worker requires repository, store, commander, and environment")
	}
	return &LocalThumbnailWorker{
		repository: repository, store: store, commander: commander, environment: environment,
	}, nil
}

func (worker *LocalThumbnailWorker) PollOnce(ctx context.Context) error {
	if worker == nil {
		return nil
	}
	task, err := worker.repository.ClaimLocalThumbnail(ctx)
	if errors.Is(err, media.ErrLocalThumbnailTaskNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if task.Operation == "delete" {
		if task.ObjectKey == "" {
			return worker.retry(ctx, task, fmt.Errorf("local thumbnail delete key is missing"))
		}
		if err := worker.store.Delete(ctx, task.ObjectKey); err != nil {
			return worker.retry(ctx, task, fmt.Errorf("delete local thumbnail object: %w", err))
		}
		if err := worker.repository.CompleteLocalThumbnailDeletion(
			ctx, task.OutboxID, task.RecordingID,
		); err != nil {
			return fmt.Errorf("complete local thumbnail deletion: %w", err)
		}
		return nil
	}
	if task.Operation != "upload" {
		return worker.retry(ctx, task, fmt.Errorf("unknown local thumbnail operation"))
	}
	if task.SizeBytes <= 0 || task.SizeBytes > localThumbnailMaxBytes ||
		!strings.HasPrefix(task.ETag, "sha256:") || len(task.ETag) != 71 {
		return worker.retry(ctx, task, fmt.Errorf("local thumbnail metadata is invalid"))
	}
	objectKey := worker.environment + "/local-recording-thumbnails/" + task.DeviceID + "/" +
		task.RecordingID + "/" + strings.TrimPrefix(task.ETag, "sha256:") + ".jpg"
	uploadURL, err := worker.store.PutURLWithSize(
		ctx, objectKey, "image/jpeg", task.SizeBytes, localThumbnailUploadTTL,
	)
	if err != nil {
		return worker.retry(ctx, task, fmt.Errorf("presign local thumbnail upload: %w", err))
	}
	value, err := json.Marshal(map[string]any{
		"contract":     "sd-thumbnail.v1",
		"op":           "upload",
		"recording_id": task.RecordingID,
		"revision":     task.Revision,
		"upload_url":   uploadURL,
		"etag":         task.ETag,
	})
	if err != nil {
		return worker.retry(ctx, task, fmt.Errorf("encode local thumbnail command: %w", err))
	}
	result, err := worker.commander.SendCommands(
		ctx, task.InternalAppID, task.DeviceID, task.DeviceSN,
		[]device.CommandInput{{
			Code: "sd_thumbnail_v1", Value: value, RedactFields: []string{"upload_url"},
		}}, true,
	)
	if err != nil {
		return worker.retry(ctx, task, fmt.Errorf("send local thumbnail upload command: %w", err))
	}
	if len(result.Results) != 1 || result.Results[0].Status != "success" {
		return worker.retry(ctx, task, fmt.Errorf("device rejected local thumbnail upload"))
	}
	size, contentType, err := worker.store.Stat(ctx, objectKey)
	if err != nil {
		return worker.retry(ctx, task, fmt.Errorf("stat local thumbnail object: %w", err))
	}
	if size != task.SizeBytes || contentType != "image/jpeg" {
		return worker.retry(ctx, task, fmt.Errorf("local thumbnail object metadata mismatch"))
	}
	payload, err := worker.store.Download(ctx, objectKey, localThumbnailMaxBytes)
	if err != nil {
		return worker.retry(ctx, task, fmt.Errorf("download local thumbnail object: %w", err))
	}
	if int64(len(payload)) != task.SizeBytes ||
		!media.ValidateLocalThumbnailJPEG(payload, task.ETag) {
		return worker.retry(ctx, task, fmt.Errorf("local thumbnail JPEG/hash is invalid"))
	}
	if err := worker.repository.CompleteLocalThumbnail(ctx, task.OutboxID, task.RecordingID, objectKey); err != nil {
		return fmt.Errorf("complete local thumbnail: %w", err)
	}
	return nil
}

func (worker *LocalThumbnailWorker) retry(
	ctx context.Context, task media.LocalThumbnailTask, cause error,
) error {
	if err := worker.repository.RetryLocalThumbnail(ctx, task.OutboxID, cause.Error(), task.Attempts); err != nil {
		return fmt.Errorf("%v; persist retry: %w", cause, err)
	}
	return cause
}
