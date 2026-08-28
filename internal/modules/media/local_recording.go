package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"
)

const (
	LocalRecordingCatalogContractV1       = "sd-recording-catalog.v1"
	LocalThumbnailContractV1              = "sd-thumbnail.v1"
	maxJSONSafeInteger              int64 = 9007199254740991
)

var (
	recordingIDPattern = regexp.MustCompile(`^rec_[0-9a-f]{32}$`)
	revisionPattern    = regexp.MustCompile(`^rev_[0-9a-f]{64}$`)
	sha256ETagPattern  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	deviceEventPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

type RecordingFinalizedV1 struct {
	Contract      string                    `json:"contract"`
	Type          string                    `json:"type"`
	DeviceEventID string                    `json:"-"`
	Recording     LocalRecordingMetadataV1  `json:"recording"`
	Thumbnail     LocalRecordingThumbnailV1 `json:"thumbnail"`
}

type LocalRecordingMetadataV1 struct {
	Version       int    `json:"version"`
	RecordingID   string `json:"recording_id"`
	Revision      string `json:"revision"`
	StartAt       string `json:"start_at"`
	EndAt         string `json:"end_at"`
	DurationMS    int64  `json:"duration_ms"`
	SizeBytes     int64  `json:"size_bytes"`
	Type          string `json:"type"`
	Codec         string `json:"codec"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	FPSNum        int    `json:"fps_num"`
	FPSDen        int    `json:"fps_den"`
	Complete      bool   `json:"complete"`
	Seekable      bool   `json:"seekable"`
	ThumbnailETag string `json:"thumbnail_etag"`
	IndexVersion  int    `json:"index_version"`
}

type LocalRecordingThumbnailV1 struct {
	Status      string `json:"status"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	ETag        string `json:"etag"`
}

type LocalRecordingThumbnail struct {
	Status    string `json:"status"`
	ETag      string `json:"etag"`
	URL       string `json:"url,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

type LocalRecordingListItem struct {
	InternalID  string                  `json:"-"`
	RecordingID string                  `json:"recording_id"`
	Revision    string                  `json:"revision"`
	StartAt     string                  `json:"start_at,omitempty"`
	EndAt       string                  `json:"end_at,omitempty"`
	DurationMS  int64                   `json:"duration_ms,omitempty"`
	SizeBytes   int64                   `json:"size_bytes,omitempty"`
	Type        string                  `json:"type,omitempty"`
	Codec       string                  `json:"codec,omitempty"`
	Width       int                     `json:"width,omitempty"`
	Height      int                     `json:"height,omitempty"`
	FPSNum      int                     `json:"fps_num,omitempty"`
	FPSDen      int                     `json:"fps_den,omitempty"`
	Playable    bool                    `json:"playable"`
	Seekable    bool                    `json:"seekable"`
	Thumbnail   LocalRecordingThumbnail `json:"thumbnail"`
}

func ParseRecordingFinalizedV1(deviceEventID string, payload []byte) (RecordingFinalizedV1, error) {
	var event RecordingFinalizedV1
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil {
		return RecordingFinalizedV1{}, fmt.Errorf("decode recording finalized v1: %w", err)
	}
	if err := rejectTrailingJSON(decoder); err != nil {
		return RecordingFinalizedV1{}, err
	}
	event.DeviceEventID = deviceEventID
	if err := event.validate(); err != nil {
		return RecordingFinalizedV1{}, err
	}
	return event, nil
}

func rejectTrailingJSON(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("recording finalized v1 contains trailing JSON")
		}
		return fmt.Errorf("decode recording finalized v1 trailer: %w", err)
	}
	return nil
}

func (event RecordingFinalizedV1) validate() error {
	if event.Contract != LocalRecordingCatalogContractV1 || event.Type != "recording_finalized" ||
		!deviceEventPattern.MatchString(event.DeviceEventID) {
		return errors.New("invalid recording finalized v1 envelope")
	}
	if err := event.Recording.validate(); err != nil {
		return err
	}
	if event.Thumbnail.Status != "pending" || event.Thumbnail.ContentType != "image/jpeg" ||
		event.Thumbnail.SizeBytes <= 0 || event.Thumbnail.SizeBytes > maxJSONSafeInteger ||
		!sha256ETagPattern.MatchString(event.Thumbnail.ETag) ||
		event.Thumbnail.ETag != event.Recording.ThumbnailETag {
		return errors.New("invalid recording thumbnail v1")
	}
	return nil
}

func (recording LocalRecordingMetadataV1) validate() error {
	if recording.Version != 1 || !recordingIDPattern.MatchString(recording.RecordingID) ||
		!revisionPattern.MatchString(recording.Revision) || !recording.Complete ||
		recording.DurationMS < 0 || recording.DurationMS > maxJSONSafeInteger ||
		recording.SizeBytes < 0 || recording.SizeBytes > maxJSONSafeInteger || recording.Width <= 0 ||
		recording.Height <= 0 || recording.FPSNum <= 0 || recording.FPSDen <= 0 ||
		recording.Codec != "h264" || recording.IndexVersion != 1 ||
		!sha256ETagPattern.MatchString(recording.ThumbnailETag) {
		return errors.New("invalid local recording metadata v1")
	}
	if recording.Type != "event" && recording.Type != "manual" && recording.Type != "continuous" {
		return errors.New("invalid local recording type")
	}
	startAt, err := time.Parse(time.RFC3339, recording.StartAt)
	if err != nil {
		return errors.New("invalid local recording start_at")
	}
	endAt, err := time.Parse(time.RFC3339, recording.EndAt)
	if err != nil || endAt.Before(startAt) {
		return errors.New("invalid local recording end_at")
	}
	return nil
}

type LocalRecordingProjection struct {
	LocalRecordingListItem
	DeviceID           string `json:"-"`
	ThumbnailObjectKey string `json:"-"`
}

type LocalRecordingPage struct {
	Contract   string                   `json:"contract"`
	Items      []LocalRecordingListItem `json:"items"`
	NextCursor *string                  `json:"next_cursor"`
}

type LocalRecordingRepository interface {
	ListLocalRecordingProjections(
		context.Context, string, string, int, string, string, string,
	) ([]LocalRecordingProjection, string, error)
	GetLocalRecordingThumbnail(
		context.Context, string, string, string,
	) (string, string, error)
}

type RecordingDeletedV1 struct {
	Contract    string `json:"contract"`
	Type        string `json:"type"`
	RecordingID string `json:"recording_id"`
	Revision    string `json:"revision"`
}

func ParseRecordingDeletedV1(payload []byte) (RecordingDeletedV1, error) {
	var event RecordingDeletedV1
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil {
		return RecordingDeletedV1{}, fmt.Errorf("decode recording deleted v1: %w", err)
	}
	if err := rejectTrailingJSON(decoder); err != nil {
		return RecordingDeletedV1{}, err
	}
	if event.Contract != LocalRecordingCatalogContractV1 || event.Type != "recording_deleted" ||
		!recordingIDPattern.MatchString(event.RecordingID) || !revisionPattern.MatchString(event.Revision) {
		return RecordingDeletedV1{}, errors.New("invalid recording deleted v1")
	}
	return event, nil
}
