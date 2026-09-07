package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/device"
)

const (
	mediaUploadTTL      = 15 * time.Minute
	mediaDownloadTTL    = 15 * time.Minute
	maxFeedAudioSize    = 1 << 20
	maxSnapshotH264Size = 8 << 20
)

var (
	ErrSnapshotNotFound   = errors.New("snapshot not found")
	ErrSnapshotProcessing = errors.New("snapshot processing failed")
	ErrRecordingNotFound  = errors.New("recording not found")
	ErrRecordingRejected  = errors.New("recording start rejected")
)

type ObjectStore interface {
	PutURL(context.Context, string, string, time.Duration) (string, error)
	GetURL(context.Context, string, time.Duration) (string, error)
	Upload(context.Context, string, string, []byte) error
	Download(context.Context, string, int64) ([]byte, error)
	Exists(context.Context, string) (bool, error)
}

type SnapshotConverter interface {
	H264ToJPEG(context.Context, []byte) ([]byte, error)
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
	TransitionSnapshotStatus(context.Context, string, string, string) (bool, error)
	GetSnapshot(context.Context, string, string, string) (Snapshot, error)
	CreateRecording(context.Context, Recording) error
	SetRecordingStatus(context.Context, string, string) error
	GetRecording(context.Context, string, string, string) (Recording, error)
	ListRecordings(context.Context, string, string, int, string, string) ([]Recording, string, error)
}

type AssetService struct {
	store       ObjectStore
	commander   DPCommander
	repository  AssetRepository
	converter   SnapshotConverter
	environment string
	newID       func() (string, error)
	locks       snapshotLocks
}

func NewAssetService(store ObjectStore, commander DPCommander, repository AssetRepository, environment string, converter SnapshotConverter, newID func() (string, error)) *AssetService {
	if newID == nil {
		newID = randomUUID
	}
	return &AssetService{store: store, commander: commander, repository: repository, converter: converter, environment: environment, newID: newID}
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
	if _, err := service.commander.SendDPCommands(ctx, internalAppID, deviceID, deviceSN,
		[]device.CommandInput{{Code: "snapshot", Value: value, CommandID: id}}, waitACK); err != nil {
		_ = service.repository.SetSnapshotStatus(ctx, id, "failed")
		return SnapshotResult{}, err
	}
	if err := service.repository.SetSnapshotStatus(ctx, id, "uploading"); err != nil {
		return SnapshotResult{}, err
	}
	snapshot.Status = "uploading"
	return service.resolveSnapshot(ctx, snapshot)
}

func (service *AssetService) GetSnapshot(ctx context.Context, bindingID, deviceSN, id string) (SnapshotResult, error) {
	unlock := service.locks.lock(id)
	defer unlock()
	snapshot, err := service.repository.GetSnapshot(ctx, bindingID, deviceSN, id)
	if err != nil {
		return SnapshotResult{}, err
	}
	if snapshot.Status == "failed" || snapshot.Status == "expired" {
		return SnapshotResult{SnapshotID: id, Status: snapshot.Status}, nil
	}
	return service.resolveSnapshot(ctx, snapshot)
}

func (service *AssetService) resolveSnapshot(ctx context.Context, snapshot Snapshot) (SnapshotResult, error) {
	result := SnapshotResult{SnapshotID: snapshot.ID, Status: snapshot.Status}
	jpegKey, err := snapshotJPEGKey(snapshot.ObjectKey)
	if err != nil {
		return service.failSnapshot(ctx, snapshot, err)
	}
	jpegExists, err := service.store.Exists(ctx, jpegKey)
	if err != nil {
		return snapshotProcessingError(fmt.Errorf("check JPEG snapshot: %w", err))
	}
	if !jpegExists {
		h264Exists, err := service.store.Exists(ctx, snapshot.ObjectKey)
		if err != nil {
			return snapshotProcessingError(fmt.Errorf("check H.264 snapshot: %w", err))
		}
		if !h264Exists {
			return result, nil
		}
		if service.converter == nil {
			return service.failSnapshot(ctx, snapshot, fmt.Errorf("snapshot converter is unavailable"))
		}
		h264, err := service.store.Download(ctx, snapshot.ObjectKey, maxSnapshotH264Size)
		if err != nil {
			return snapshotProcessingError(fmt.Errorf("download H.264 snapshot: %w", err))
		}
		jpeg, err := service.converter.H264ToJPEG(ctx, h264)
		if err != nil {
			return snapshotProcessingError(err)
		}
		if err := service.store.Upload(ctx, jpegKey, "image/jpeg", jpeg); err != nil {
			return snapshotProcessingError(fmt.Errorf("upload JPEG snapshot: %w", err))
		}
	}
	url, err := service.store.GetURL(ctx, jpegKey, mediaDownloadTTL)
	if err != nil {
		return snapshotProcessingError(fmt.Errorf("presign JPEG snapshot download: %w", err))
	}
	transitioned, err := service.repository.TransitionSnapshotStatus(ctx, snapshot.ID, "uploading", "ready")
	if err != nil {
		return SnapshotResult{}, err
	}
	if !transitioned && snapshot.Status != "ready" {
		current, err := service.repository.GetSnapshot(ctx, snapshot.BindingID, snapshot.DeviceSN, snapshot.ID)
		if err != nil {
			return SnapshotResult{}, err
		}
		if current.Status != "ready" {
			return SnapshotResult{SnapshotID: snapshot.ID, Status: current.Status}, nil
		}
	}
	return SnapshotResult{
		SnapshotID: snapshot.ID, Status: "ready", URL: url,
		ExpiresAt: time.Now().UTC().Add(mediaDownloadTTL),
	}, nil
}

func (service *AssetService) failSnapshot(ctx context.Context, snapshot Snapshot, cause error) (SnapshotResult, error) {
	_, _ = service.repository.TransitionSnapshotStatus(ctx, snapshot.ID, "uploading", "failed")
	return snapshotProcessingError(cause)
}

func snapshotProcessingError(cause error) (SnapshotResult, error) {
	return SnapshotResult{}, fmt.Errorf("%w: %w", ErrSnapshotProcessing, cause)
}

func snapshotJPEGKey(h264Key string) (string, error) {
	if !strings.HasSuffix(h264Key, ".h264") {
		return "", fmt.Errorf("snapshot object key is invalid")
	}
	return strings.TrimSuffix(h264Key, ".h264") + ".jpg", nil
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
	commandResult, err := service.commander.SendDPCommands(ctx, internalAppID, deviceID, deviceSN,
		[]device.CommandInput{{Code: "record_clip", Value: value, CommandID: id}}, true)
	if err != nil {
		_ = service.repository.SetRecordingStatus(ctx, id, "failed")
		return Recording{}, err
	}
	if len(commandResult.Results) != 1 || commandResult.Results[0].Status != "success" {
		_ = service.repository.SetRecordingStatus(ctx, id, "failed")
		return Recording{}, ErrRecordingRejected
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
	bindingID, deviceSN, dateFilter, typeFilter, cursor string,
	limit int,
) (LocalRecordingPage, error) {
	if bindingID == "" || deviceSN == "" {
		return LocalRecordingPage{}, ErrInvalidInput
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		return LocalRecordingPage{}, ErrInvalidInput
	}
	repository, ok := service.repository.(LocalRecordingRepository)
	if !ok {
		return LocalRecordingPage{}, fmt.Errorf("local recording repository is unavailable")
	}
	projections, nextCursor, err := repository.ListLocalRecordingProjections(
		ctx, bindingID, deviceSN, limit, cursor, dateFilter, typeFilter,
	)
	if err != nil {
		return LocalRecordingPage{}, err
	}
	items := make([]LocalRecordingListItem, 0, len(projections))
	for _, projection := range projections {
		item := projection.LocalRecordingListItem
		if item.Thumbnail.Status == "ready" && projection.ThumbnailObjectKey != "" {
			item.Thumbnail.URL = "/api/v1/openapi/devices/" + url.PathEscape(deviceSN) +
				"/recordings/local/" + url.PathEscape(item.RecordingID) + "/thumbnail"
			item.Thumbnail.ExpiresAt = time.Now().UTC().Add(mediaDownloadTTL).Format(time.RFC3339)
		}
		items = append(items, item)
	}
	var nextCursorValue *string
	if nextCursor != "" {
		nextCursorValue = &nextCursor
	}
	return LocalRecordingPage{
		Contract:   LocalRecordingCatalogContractV1,
		Items:      items,
		NextCursor: nextCursorValue,
	}, nil
}

func (service *AssetService) ListLegacyLocalRecordings(
	ctx context.Context,
	internalAppID, deviceID, deviceSN, date string,
	offset, limit int,
) (map[string]any, error) {
	if !service.valid(deviceID, deviceSN) || internalAppID == "" || offset < 0 || limit < 1 || limit > 50 {
		return nil, ErrInvalidInput
	}
	value := map[string]any{"op": "list", "offset": offset, "limit": limit}
	if date != "" {
		value["date"] = date
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode legacy local recording request: %w", err)
	}
	result, err := service.commander.SendDPCommands(
		ctx, internalAppID, deviceID, deviceSN,
		[]device.CommandInput{{Code: "sd_files", Value: payload}}, true,
	)
	if err != nil {
		return nil, err
	}
	if len(result.Results) != 1 {
		return nil, fmt.Errorf("legacy local recording response is incomplete")
	}
	return result.Results[0].Data, nil
}

type LocalThumbnailContent struct {
	Payload []byte
	ETag    string
}

func (service *AssetService) GetLocalRecordingThumbnail(
	ctx context.Context, bindingID, deviceSN, recordingID string,
) (LocalThumbnailContent, error) {
	if bindingID == "" || deviceSN == "" || !recordingIDPattern.MatchString(recordingID) {
		return LocalThumbnailContent{}, ErrInvalidInput
	}
	repository, ok := service.repository.(LocalRecordingRepository)
	if !ok {
		return LocalThumbnailContent{}, fmt.Errorf("local recording repository is unavailable")
	}
	objectKey, etag, err := repository.GetLocalRecordingThumbnail(
		ctx, bindingID, deviceSN, recordingID,
	)
	if err != nil {
		return LocalThumbnailContent{}, err
	}
	payload, err := service.store.Download(ctx, objectKey, 256*1024)
	if err != nil {
		return LocalThumbnailContent{}, err
	}
	if !ValidateLocalThumbnailJPEG(payload, etag) {
		return LocalThumbnailContent{}, fmt.Errorf("local recording thumbnail failed integrity validation")
	}
	return LocalThumbnailContent{Payload: payload, ETag: etag}, nil
}

func (service *AssetService) UploadFeedAudio(
	ctx context.Context,
	internalAppID, deviceID, deviceSN, contentType string,
	payload []byte,
) (map[string]any, error) {
	if !service.valid(deviceID, deviceSN) || internalAppID == "" || len(payload) == 0 || len(payload) > maxFeedAudioSize ||
		(!strings.HasPrefix(contentType, "audio/") && contentType != "application/octet-stream") {
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
	if _, err := service.commander.SendDPCommands(ctx, internalAppID, deviceID, deviceSN,
		[]device.CommandInput{{Code: "audio_update", Value: value, CommandID: id}}, true); err != nil {
		return nil, err
	}
	return map[string]any{"status": "accepted", "sha256": sha256Hex, "file_size": len(payload)}, nil
}

func (service *AssetService) valid(deviceID, deviceSN string) bool {
	return service != nil && service.store != nil && service.commander != nil && service.repository != nil &&
		strings.TrimSpace(service.environment) != "" && strings.TrimSpace(deviceID) != "" && strings.TrimSpace(deviceSN) != ""
}
