package media

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/device"
)

type objectStoreStub struct {
	putKey      string
	uploadKey   string
	uploadType  string
	downloadKey string
	payload     []byte
	exists      bool
	objects     map[string][]byte
}

func (store *objectStoreStub) PutURL(_ context.Context, key, _ string, _ time.Duration) (string, error) {
	store.putKey = key
	return "https://uploads.example/object?signed=true", nil
}
func (store *objectStoreStub) GetURL(_ context.Context, key string, _ time.Duration) (string, error) {
	return "https://downloads.example/" + key + "?signed=true", nil
}
func (store *objectStoreStub) Upload(_ context.Context, key, contentType string, payload []byte) error {
	store.uploadKey, store.uploadType, store.payload = key, contentType, append([]byte(nil), payload...)
	if store.objects != nil {
		store.objects[key] = append([]byte(nil), payload...)
	}
	return nil
}
func (store *objectStoreStub) Download(_ context.Context, key string, _ int64) ([]byte, error) {
	store.downloadKey = key
	return append([]byte(nil), store.objects[key]...), nil
}
func (store *objectStoreStub) Exists(_ context.Context, key string) (bool, error) {
	if store.objects != nil {
		_, ok := store.objects[key]
		return ok, nil
	}
	return store.exists, nil
}

type snapshotConverterStub struct {
	input  []byte
	output []byte
}

func (converter *snapshotConverterStub) H264ToJPEG(_ context.Context, payload []byte) ([]byte, error) {
	converter.input = append([]byte(nil), payload...)
	return append([]byte(nil), converter.output...), nil
}

type assetRepositoryStub struct {
	snapshot  Snapshot
	recording Recording
}

func (repository *assetRepositoryStub) CreateSnapshot(_ context.Context, snapshot Snapshot) error {
	repository.snapshot = snapshot
	return nil
}
func (repository *assetRepositoryStub) SetSnapshotStatus(_ context.Context, _, status string) error {
	repository.snapshot.Status = status
	return nil
}
func (repository *assetRepositoryStub) GetSnapshot(_ context.Context, bindingID, deviceSN, id string) (Snapshot, error) {
	if repository.snapshot.BindingID != bindingID || repository.snapshot.DeviceSN != deviceSN || repository.snapshot.ID != id {
		return Snapshot{}, ErrSnapshotNotFound
	}
	return repository.snapshot, nil
}
func (repository *assetRepositoryStub) CreateRecording(_ context.Context, recording Recording) error {
	repository.recording = recording
	return nil
}
func (repository *assetRepositoryStub) SetRecordingStatus(_ context.Context, _ string, status string) error {
	repository.recording.Status = status
	return nil
}
func (repository *assetRepositoryStub) GetRecording(context.Context, string, string, string) (Recording, error) {
	return repository.recording, nil
}
func (repository *assetRepositoryStub) ListRecordings(context.Context, string, string, int, string, string) ([]Recording, string, error) {
	return []Recording{repository.recording}, "", nil
}

func TestGetSnapshotConvertsUploadedH264ToJPEG(t *testing.T) {
	t.Parallel()
	const sourceKey = "staging/snapshots/device-uuid/018f0000-0000-7000-8000-000000000099.h264"
	store := &objectStoreStub{objects: map[string][]byte{sourceKey: []byte("h264-idr")}}
	converter := &snapshotConverterStub{output: []byte("jpeg-frame")}
	repository := &assetRepositoryStub{snapshot: Snapshot{
		ID: "018f0000-0000-7000-8000-000000000099", DeviceID: "device-uuid", DeviceSN: "fdr-0001",
		BindingID: "binding-id", ObjectKey: sourceKey, Status: "uploading",
	}}
	service := NewAssetService(store, &commanderStub{}, repository, "staging", converter, nil)

	result, err := service.GetSnapshot(context.Background(), "binding-id", "fdr-0001", repository.snapshot.ID)
	if err != nil {
		t.Fatalf("GetSnapshot() error = %v", err)
	}
	if result.Status != "ready" || !strings.Contains(result.URL, ".jpg?") {
		t.Fatalf("GetSnapshot() result = %#v", result)
	}
	if store.downloadKey != sourceKey || store.uploadKey != strings.TrimSuffix(sourceKey, ".h264")+".jpg" || store.uploadType != "image/jpeg" {
		t.Fatalf("downloadKey=%q uploadKey=%q uploadType=%q", store.downloadKey, store.uploadKey, store.uploadType)
	}
	if !bytes.Equal(converter.input, []byte("h264-idr")) || !bytes.Equal(store.payload, []byte("jpeg-frame")) {
		t.Fatalf("converter input=%q uploaded payload=%q", converter.input, store.payload)
	}
	if repository.snapshot.Status != "ready" {
		t.Fatalf("snapshot status = %q", repository.snapshot.Status)
	}
}

func TestCreateSnapshotUsesEnvironmentAndDeviceScopedPresignedUpload(t *testing.T) {
	t.Parallel()
	store := &objectStoreStub{exists: true}
	repository := &assetRepositoryStub{}
	service := NewAssetService(store, &commanderStub{}, repository, "staging", &snapshotConverterStub{output: []byte("jpeg-frame")}, func() (string, error) { return "018f0000-0000-7000-8000-000000000099", nil })
	result, err := service.CreateSnapshot(context.Background(), "livpet-staging", "device-uuid", "binding-id", "fdr-0001", true)
	if err != nil {
		t.Fatalf("CreateSnapshot() error = %v", err)
	}
	if !strings.HasPrefix(store.putKey, "staging/snapshots/device-uuid/") || repository.snapshot.Status != "ready" || result.URL == "" {
		t.Fatalf("putKey=%q snapshot=%#v result=%#v", store.putKey, repository.snapshot, result)
	}
}

func TestUploadFeedAudioHashesStoredBytesAndCommandsDevice(t *testing.T) {
	t.Parallel()
	for _, contentType := range []string{"audio/pcm", "application/octet-stream"} {
		t.Run(contentType, func(t *testing.T) {
			store := &objectStoreStub{}
			commander := &commanderStub{}
			service := NewAssetService(store, commander, &assetRepositoryStub{}, "staging", nil, func() (string, error) { return "018f0000-0000-7000-8000-000000000098", nil })
			result, err := service.UploadFeedAudio(context.Background(), "livpet-staging", "device-uuid", "fdr-0001", contentType, []byte("pcm-bytes"))
			if err != nil {
				t.Fatalf("UploadFeedAudio() error = %v", err)
			}
			if !strings.Contains(store.uploadKey, "/feed-audio/") || string(store.payload) != "pcm-bytes" || commander.input.Code != "audio_update" || result["sha256"] == "" {
				t.Fatalf("key=%q command=%#v result=%#v", store.uploadKey, commander.input, result)
			}
		})
	}
}

func TestCreateRecordingWaitsForInitialDeviceACK(t *testing.T) {
	t.Parallel()
	commander := &commanderStub{}
	service := NewAssetService(&objectStoreStub{}, commander, &assetRepositoryStub{}, "staging", nil, func() (string, error) {
		return "018f0000-0000-7000-8000-000000000097", nil
	})
	if _, err := service.CreateRecording(context.Background(), "livpet-staging", "device-uuid", "binding-id", "fdr-0001", 5); err != nil {
		t.Fatalf("CreateRecording() error = %v", err)
	}
	if !commander.wait {
		t.Fatal("CreateRecording() must wait for the initial device ACK")
	}
}

func TestCreateRecordingRejectsFailedInitialDeviceACK(t *testing.T) {
	t.Parallel()
	commander := &commanderStub{result: &device.SendCommandsResult{Results: []device.CommandResult{{
		Code: "record_clip", Status: "error", Reason: "device rejected command with code 2",
	}}}}
	repository := &assetRepositoryStub{}
	service := NewAssetService(&objectStoreStub{}, commander, repository, "staging", nil, func() (string, error) {
		return "018f0000-0000-7000-8000-000000000096", nil
	})
	if _, err := service.CreateRecording(context.Background(), "livpet-staging", "device-uuid", "binding-id", "fdr-0001", 5); err == nil {
		t.Fatal("CreateRecording() must reject a failed initial device ACK")
	}
	if repository.recording.Status != "failed" {
		t.Fatalf("recording status = %q, want failed", repository.recording.Status)
	}
}

var _ Commander = (*commanderStub)(nil)
var _ = device.CommandInput{}
