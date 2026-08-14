package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/device"
)

const (
	mediaUploadTTL   = 15 * time.Minute
	mediaDownloadTTL = 15 * time.Minute
	maxFeedAudioSize = 1 << 20
)

var (
	ErrSnapshotNotFound  = errors.New("snapshot not found")
	ErrRecordingNotFound = errors.New("recording not found")
)

type ObjectStore interface {
	PutURL(context.Context, string, string, time.Duration) (string, error)
	GetURL(context.Context, string, time.Duration) (string, error)
	Upload(context.Context, string, string, []byte) error
	Exists(context.Context, string) (bool, error)
}

type Snapshot struct {
	ID        string
	DeviceID  string
	DeviceSN  string
	BindingID string
	ObjectKey string
	Status    string
	ExpiresAt time.Time
}

type SnapshotResult struct {
	SnapshotID string    `json:"snapshot_id"`
	Status     string    `json:"status"`
	URL        string    `json:"url,omitempty"`
	ExpiresAt  time.Time `json:"expires_at,omitempty"`
}

type Recording struct {
	ID              string    `json:"id"`
	DeviceID        string    `json:"-"`
	DeviceSN        string    `json:"device_sn"`
	BindingID       string    `json:"-"`
	ObjectKey       string    `json:"-"`
	Status          string    `json:"status"`
	DurationSeconds int       `json:"duration_requested"`
	CreatedAt       time.Time `json:"created_at"`
	URL             string    `json:"url,omitempty"`
}

type AssetRepository interface {
	CreateSnapshot(context.Context, Snapshot) error
	SetSnapshotStatus(context.Context, string, string) error
	GetSnapshot(context.Context, string, string, string) (Snapshot, error)
	CreateRecording(context.Context, Recording) error
	GetRecording(context.Context, string, string, string) (Recording, error)
	ListRecordings(context.Context, string, string, int, string, string) ([]Recording, string, error)
}

type AssetService struct {
	store       ObjectStore
	commander   Commander
	repository  AssetRepository
	environment string
	newID       func() (string, error)
}

func NewAssetService(store ObjectStore, commander Commander, repository AssetRepository, environment string, newID func() (string, error)) *AssetService {
	if newID == nil {
		newID = randomUUID
	}
	return &AssetService{store: store, commander: commander, repository: repository, environment: environment, newID: newID}
}

func (service *AssetService) CreateSnapshot(
	ctx context.Context,
	internalAppID, deviceID, bindingID, deviceSN string,
	waitACK bool,
) (SnapshotResult, error) {
	if !service.valid(deviceID, deviceSN) || bindingID == "" || internalAppID == "" {
		return SnapshotResult{}, ErrInvalidInput
	}
	id, err := service.newID()
	if err != nil {
		return SnapshotResult{}, fmt.Errorf("generate snapshot id: %w", err)
	}
	key := service.environment + "/snapshots/" + deviceID + "/" + id + ".h264"
	snapshot := Snapshot{
		ID: id, DeviceID: deviceID, DeviceSN: deviceSN, BindingID: bindingID,
		ObjectKey: key, Status: "pending", ExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	}
	if err := service.repository.CreateSnapshot(ctx, snapshot); err != nil {
		return SnapshotResult{}, err
	}
	uploadURL, err := service.store.PutURL(ctx, key, "video/h264", mediaUploadTTL)
	if err != nil {
		_ = service.repository.SetSnapshotStatus(ctx, id, "failed")
		return SnapshotResult{}, err
	}
	value, _ := json.Marshal(map[string]any{"upload_url": uploadURL})
	if _, err := service.commander.SendCommands(ctx, internalAppID, deviceID, deviceSN,
		[]device.CommandInput{{Code: "snapshot", Value: value, CommandID: id}}, waitACK); err != nil {
		_ = service.repository.SetSnapshotStatus(ctx, id, "failed")
		return SnapshotResult{}, err
	}
	status := "uploading"
	result := SnapshotResult{SnapshotID: id, Status: status}
	if exists, _ := service.store.Exists(ctx, key); exists {
		status = "ready"
		result.Status = status
		result.URL, err = service.store.GetURL(ctx, key, mediaDownloadTTL)
		if err != nil {
			return SnapshotResult{}, err
		}
		result.ExpiresAt = time.Now().UTC().Add(mediaDownloadTTL)
	}
	if err := service.repository.SetSnapshotStatus(ctx, id, status); err != nil {
		return SnapshotResult{}, err
	}
	return result, nil
}

func (service *AssetService) GetSnapshot(ctx context.Context, bindingID, deviceSN, id string) (SnapshotResult, error) {
	snapshot, err := service.repository.GetSnapshot(ctx, bindingID, deviceSN, id)
	if err != nil {
		return SnapshotResult{}, err
	}
	if exists, _ := service.store.Exists(ctx, snapshot.ObjectKey); !exists {
		return SnapshotResult{SnapshotID: id, Status: snapshot.Status}, nil
	}
	url, err := service.store.GetURL(ctx, snapshot.ObjectKey, mediaDownloadTTL)
	if err != nil {
		return SnapshotResult{}, err
	}
	_ = service.repository.SetSnapshotStatus(ctx, id, "ready")
	return SnapshotResult{SnapshotID: id, Status: "ready", URL: url, ExpiresAt: time.Now().UTC().Add(mediaDownloadTTL)}, nil
}

func (service *AssetService) CreateRecording(
	ctx context.Context,
	internalAppID, deviceID, bindingID, deviceSN string,
	duration int,
) (Recording, error) {
	if !service.valid(deviceID, deviceSN) || internalAppID == "" || bindingID == "" || duration <= 0 {
		return Recording{}, ErrInvalidInput
	}
	if duration < 5 {
		duration = 5
	}
	if duration > 60 {
		duration = 60
	}
	id, err := service.newID()
	if err != nil {
		return Recording{}, fmt.Errorf("generate recording id: %w", err)
	}
	key := service.environment + "/recordings/" + deviceID + "/" + id + ".h264"
	uploadURL, err := service.store.PutURL(ctx, key, "video/h264", time.Duration(duration+600)*time.Second)
	if err != nil {
		return Recording{}, err
	}
	recording := Recording{
		ID: id, DeviceID: deviceID, DeviceSN: deviceSN, BindingID: bindingID,
		ObjectKey: key, Status: "pending", DurationSeconds: duration, CreatedAt: time.Now().UTC(),
	}
	if err := service.repository.CreateRecording(ctx, recording); err != nil {
		return Recording{}, err
	}
	value, _ := json.Marshal(map[string]any{"duration": duration, "upload_url": uploadURL})
	if _, err := service.commander.SendCommands(ctx, internalAppID, deviceID, deviceSN,
		[]device.CommandInput{{Code: "record_clip", Value: value, CommandID: id}}, false); err != nil {
		return Recording{}, err
	}
	return recording, nil
}

func (service *AssetService) GetRecording(ctx context.Context, bindingID, deviceSN, id string) (Recording, error) {
	recording, err := service.repository.GetRecording(ctx, bindingID, deviceSN, id)
	if err != nil {
		return Recording{}, err
	}
	if recording.Status == "ready" {
		recording.URL, _ = service.store.GetURL(ctx, recording.ObjectKey, mediaDownloadTTL)
	}
	return recording, nil
}

func (service *AssetService) ListRecordings(
	ctx context.Context,
	bindingID, deviceSN string,
	limit int,
	cursor, status string,
) ([]Recording, string, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	items, nextCursor, err := service.repository.ListRecordings(ctx, bindingID, deviceSN, limit, cursor, status)
	if err != nil {
		return nil, "", err
	}
	for index := range items {
		if items[index].Status == "ready" {
			items[index].URL, _ = service.store.GetURL(ctx, items[index].ObjectKey, mediaDownloadTTL)
		}
	}
	return items, nextCursor, nil
}

func (service *AssetService) ListLocalRecordings(
	ctx context.Context,
	internalAppID, deviceID, deviceSN, date string,
	offset, limit int,
) (map[string]any, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	value := map[string]any{"op": "list", "offset": offset, "limit": limit}
	if date != "" {
		value["date"] = date
	}
	payload, _ := json.Marshal(value)
	result, err := service.commander.SendCommands(ctx, internalAppID, deviceID, deviceSN,
		[]device.CommandInput{{Code: "sd_files", Value: payload}}, true)
	if err != nil {
		return nil, err
	}
	if len(result.Results) != 1 {
		return nil, fmt.Errorf("local recording response is incomplete")
	}
	return result.Results[0].Data, nil
}

func (service *AssetService) UploadFeedAudio(
	ctx context.Context,
	internalAppID, deviceID, deviceSN, contentType string,
	payload []byte,
) (map[string]any, error) {
	if !service.valid(deviceID, deviceSN) || internalAppID == "" || len(payload) == 0 || len(payload) > maxFeedAudioSize || !strings.HasPrefix(contentType, "audio/") {
		return nil, ErrInvalidInput
	}
	id, err := service.newID()
	if err != nil {
		return nil, fmt.Errorf("generate feed audio id: %w", err)
	}
	key := service.environment + "/feed-audio/" + deviceID + "/" + id
	if err := service.store.Upload(ctx, key, contentType, payload); err != nil {
		return nil, err
	}
	digest := sha256.Sum256(payload)
	downloadURL, err := service.store.GetURL(ctx, key, mediaDownloadTTL)
	if err != nil {
		return nil, err
	}
	sha256Hex := hex.EncodeToString(digest[:])
	value, _ := json.Marshal(map[string]any{
		"download_url": downloadURL, "sha256": sha256Hex, "file_size": len(payload),
	})
	if _, err := service.commander.SendCommands(ctx, internalAppID, deviceID, deviceSN,
		[]device.CommandInput{{Code: "audio_update", Value: value, CommandID: id}}, true); err != nil {
		return nil, err
	}
	return map[string]any{"status": "accepted", "sha256": sha256Hex, "file_size": len(payload)}, nil
}

func (service *AssetService) valid(deviceID, deviceSN string) bool {
	return service != nil && service.store != nil && service.commander != nil && service.repository != nil &&
		strings.TrimSpace(service.environment) != "" && strings.TrimSpace(deviceID) != "" && strings.TrimSpace(deviceSN) != ""
}
