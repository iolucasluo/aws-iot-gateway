package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

var ErrLocalReconciliationTaskNotFound = errors.New("local recording reconciliation task not found")

type LocalReconciliationTask struct {
	DeviceID      string
	DeviceSN      string
	InternalAppID string
	Generation    string
	Cursor        string
}

type DeviceCatalogThumbnail struct {
	Status    string `json:"status"`
	ETag      string `json:"etag"`
	SizeBytes int64  `json:"size_bytes"`
}

type DeviceCatalogItem struct {
	RecordingID string                 `json:"recording_id"`
	Revision    string                 `json:"revision"`
	StartAt     string                 `json:"start_at"`
	EndAt       string                 `json:"end_at"`
	DurationMS  int64                  `json:"duration_ms"`
	SizeBytes   int64                  `json:"size_bytes"`
	Type        string                 `json:"type"`
	Codec       string                 `json:"codec"`
	Width       int                    `json:"width"`
	Height      int                    `json:"height"`
	FPSNum      int                    `json:"fps_num"`
	FPSDen      int                    `json:"fps_den"`
	Playable    bool                   `json:"playable"`
	Seekable    bool                   `json:"seekable"`
	Thumbnail   DeviceCatalogThumbnail `json:"thumbnail"`
}

type DeviceCatalogPage struct {
	Contract   string              `json:"contract"`
	Items      []DeviceCatalogItem `json:"items"`
	NextCursor *string             `json:"next_cursor"`
}

type deviceCatalogACK struct {
	Action     string              `json:"action"`
	SessionID  string              `json:"session_id"`
	Code       int                 `json:"code"`
	Contract   string              `json:"contract"`
	Items      []DeviceCatalogItem `json:"items"`
	NextCursor *string             `json:"next_cursor"`
}

type LocalReconciliationRepository interface {
	ClaimLocalReconciliation(context.Context) (LocalReconciliationTask, error)
	ApplyLocalReconciliationPage(context.Context, LocalReconciliationTask, DeviceCatalogPage) error
	RetryLocalReconciliation(context.Context, string, string) error
}

func ParseDeviceCatalogPage(data map[string]any) (DeviceCatalogPage, error) {
	payload, err := json.Marshal(data)
	if err != nil {
		return DeviceCatalogPage{}, fmt.Errorf("encode device catalog page: %w", err)
	}
	var page DeviceCatalogPage
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&page); err != nil {
		return DeviceCatalogPage{}, fmt.Errorf("decode device catalog page: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return DeviceCatalogPage{}, errors.New("device catalog page contains trailing JSON")
	}
	return validateDeviceCatalogPage(page)
}

func ParseDeviceCatalogACK(data map[string]any, expectedSessionID string) (DeviceCatalogPage, error) {
	payload, err := json.Marshal(data)
	if err != nil {
		return DeviceCatalogPage{}, fmt.Errorf("encode device catalog ACK: %w", err)
	}
	var ack deviceCatalogACK
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ack); err != nil {
		return DeviceCatalogPage{}, fmt.Errorf("decode device catalog ACK: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return DeviceCatalogPage{}, errors.New("device catalog ACK contains trailing JSON")
	}
	if ack.Action != "sd_recordings_v1" || ack.SessionID == "" || ack.Code != 0 ||
		(expectedSessionID != "" && ack.SessionID != expectedSessionID) {
		return DeviceCatalogPage{}, errors.New("invalid device catalog ACK envelope")
	}
	return validateDeviceCatalogPage(DeviceCatalogPage{
		Contract: ack.Contract, Items: ack.Items, NextCursor: ack.NextCursor,
	})
}

func validateDeviceCatalogPage(page DeviceCatalogPage) (DeviceCatalogPage, error) {
	if page.Contract != LocalRecordingCatalogContractV1 || len(page.Items) > 10 {
		return DeviceCatalogPage{}, errors.New("invalid device catalog envelope")
	}
	if page.NextCursor != nil && !recordingIDPattern.MatchString(*page.NextCursor) {
		return DeviceCatalogPage{}, errors.New("invalid device catalog cursor")
	}
	for index := range page.Items {
		item := &page.Items[index]
		startAt, startErr := time.Parse(time.RFC3339, item.StartAt)
		endAt, endErr := time.Parse(time.RFC3339, item.EndAt)
		if !recordingIDPattern.MatchString(item.RecordingID) ||
			!revisionPattern.MatchString(item.Revision) || startErr != nil || endErr != nil ||
			endAt.Before(startAt) || item.DurationMS < 0 || item.SizeBytes < 0 ||
			(item.Type != "event" && item.Type != "manual" && item.Type != "continuous") ||
			item.Codec != "h264" || item.Width <= 0 || item.Height <= 0 || item.FPSNum <= 0 ||
			item.FPSDen <= 0 || !item.Playable || !item.Seekable ||
			item.Thumbnail.Status != "pending" || item.Thumbnail.SizeBytes <= 0 ||
			!sha256ETagPattern.MatchString(item.Thumbnail.ETag) {
			return DeviceCatalogPage{}, errors.New("invalid device catalog item")
		}
	}
	return page, nil
}
