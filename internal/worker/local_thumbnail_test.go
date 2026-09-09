package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/device"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/media"
)

var jpeg320x180 = []byte{
	0xff, 0xd8,
	0xff, 0xe0, 0x00, 0x04, 0x00, 0x00,
	0xff, 0xc0, 0x00, 0x0b, 0x08, 0x00, 0xb4, 0x01, 0x40, 0x01, 0x01, 0x11,
	0xff, 0xd9,
}

type localThumbnailRepositoryStub struct {
	task          media.LocalThumbnailTask
	claimErr      error
	completedKey  string
	completedID   string
	retryMessage  string
	retryAttempts int
	deleted       bool
}

func (repository *localThumbnailRepositoryStub) ClaimLocalThumbnail(context.Context) (media.LocalThumbnailTask, error) {
	return repository.task, repository.claimErr
}
func (repository *localThumbnailRepositoryStub) CompleteLocalThumbnail(_ context.Context, _, recordingID, objectKey string) error {
	repository.completedID, repository.completedKey = recordingID, objectKey
	return nil
}
func (repository *localThumbnailRepositoryStub) CompleteLocalThumbnailDeletion(_ context.Context, _, recordingID string) error {
	repository.completedID, repository.deleted = recordingID, true
	return nil
}
func (repository *localThumbnailRepositoryStub) RetryLocalThumbnail(_ context.Context, _ string, message string, attempts int) error {
	repository.retryMessage, repository.retryAttempts = message, attempts
	return nil
}

type localThumbnailStoreStub struct {
	payload     []byte
	putKey      string
	contentType string
	putSize     int64
	statSize    int64
	statType    string
	deletedKey  string
}

func (store *localThumbnailStoreStub) PutURLWithSize(_ context.Context, key, contentType string, size int64, ttl time.Duration) (string, error) {
	store.putKey, store.contentType, store.putSize = key, contentType, size
	if ttl != 15*time.Minute {
		return "", errors.New("unexpected ttl")
	}
	return "https://uploads.example/private?signed=true", nil
}
func (store *localThumbnailStoreStub) Stat(context.Context, string) (int64, string, error) {
	return store.statSize, store.statType, nil
}
func (store *localThumbnailStoreStub) Download(context.Context, string, int64) ([]byte, error) {
	return append([]byte(nil), store.payload...), nil
}
func (store *localThumbnailStoreStub) Delete(_ context.Context, key string) error {
	store.deletedKey = key
	return nil
}

type localThumbnailCommanderStub struct {
	input device.CommandInput
}

func (commander *localThumbnailCommanderStub) SendCommands(
	_ context.Context, internalAppID, deviceID, deviceSN string,
	inputs []device.CommandInput, wait bool,
) (device.SendCommandsResult, error) {
	if internalAppID == "" || deviceID == "" || deviceSN == "" || !wait || len(inputs) != 1 {
		return device.SendCommandsResult{}, errors.New("invalid command context")
	}
	commander.input = inputs[0]
	return device.SendCommandsResult{Results: []device.CommandResult{{Status: "success"}}}, nil
}

func newThumbnailTask(payload []byte) media.LocalThumbnailTask {
	digest := sha256.Sum256(payload)
	return media.LocalThumbnailTask{
		Operation:   "upload",
		OutboxID:    "018f0000-0000-7000-8000-000000000090",
		RecordingID: "rec_0123456789abcdef0123456789abcdef",
		Revision:    "rev_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		ETag:        "sha256:" + hex.EncodeToString(digest[:]), SizeBytes: int64(len(payload)),
		DeviceID: "018f0000-0000-7000-8000-000000000010", DeviceSN: "fdr-0001",
		InternalAppID: "livpet-staging", Attempts: 1,
	}
}

func TestLocalThumbnailWorkerUploadsAndVerifiesPrivateJPEG(t *testing.T) {
	t.Parallel()
	task := newThumbnailTask(jpeg320x180)
	repository := &localThumbnailRepositoryStub{task: task}
	store := &localThumbnailStoreStub{
		payload: jpeg320x180, statSize: int64(len(jpeg320x180)), statType: "image/jpeg",
	}
	commander := &localThumbnailCommanderStub{}
	worker, err := NewLocalThumbnailWorker(repository, store, commander, "staging")
	if err != nil {
		t.Fatalf("NewLocalThumbnailWorker() error = %v", err)
	}
	if err := worker.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}
	if repository.completedID != task.RecordingID || repository.completedKey != store.putKey || repository.retryMessage != "" {
		t.Fatalf("repository = %#v", repository)
	}
	if store.contentType != "image/jpeg" || store.putSize != task.SizeBytes ||
		!strings.Contains(store.putKey, "/local-recording-thumbnails/") ||
		strings.Contains(store.putKey, "video.h264") {
		t.Fatalf("store key=%q contentType=%q", store.putKey, store.contentType)
	}
	if commander.input.Code != "sd_thumbnail_v1" || commander.input.CommandID != "" {
		t.Fatalf("command = %#v", commander.input)
	}
	if len(commander.input.RedactFields) != 1 || commander.input.RedactFields[0] != "upload_url" {
		t.Fatalf("command redaction = %#v", commander.input.RedactFields)
	}
	var payload map[string]any
	if err := json.Unmarshal(commander.input.Value, &payload); err != nil {
		t.Fatalf("decode command: %v", err)
	}
	if payload["recording_id"] != task.RecordingID || payload["revision"] != task.Revision ||
		payload["etag"] != task.ETag || payload["contract"] != "sd-thumbnail.v1" {
		t.Fatalf("command payload = %#v", payload)
	}
}

func TestLocalThumbnailWorkerRetriesHashMismatchWithoutMarkingReady(t *testing.T) {
	t.Parallel()
	task := newThumbnailTask(jpeg320x180)
	bad := append([]byte(nil), jpeg320x180...)
	bad[len(bad)-1] ^= 1
	repository := &localThumbnailRepositoryStub{task: task}
	store := &localThumbnailStoreStub{
		payload: bad, statSize: int64(len(bad)), statType: "image/jpeg",
	}
	worker, _ := NewLocalThumbnailWorker(repository, store, &localThumbnailCommanderStub{}, "staging")
	if err := worker.PollOnce(context.Background()); err == nil {
		t.Fatal("PollOnce() accepted invalid thumbnail")
	}
	if repository.completedKey != "" || repository.retryMessage == "" || repository.retryAttempts != task.Attempts {
		t.Fatalf("repository = %#v", repository)
	}
}

func TestLocalThumbnailWorkerDeletesPrivateObjectAsynchronously(t *testing.T) {
	t.Parallel()
	task := newThumbnailTask(jpeg320x180)
	task.Operation = "delete"
	task.ObjectKey = "staging/local-recording-thumbnails/device/recording/hash.jpg"
	repository := &localThumbnailRepositoryStub{task: task}
	store := &localThumbnailStoreStub{}
	worker, _ := NewLocalThumbnailWorker(repository, store, &localThumbnailCommanderStub{}, "staging")
	if err := worker.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce(delete) error = %v", err)
	}
	if store.deletedKey != task.ObjectKey || !repository.deleted ||
		repository.completedID != task.RecordingID {
		t.Fatalf("store=%#v repository=%#v", store, repository)
	}
}
