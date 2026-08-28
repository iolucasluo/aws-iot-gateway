package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestParseRecordingFinalizedV1AcceptsCanonicalMetadata(t *testing.T) {
	t.Parallel()
	event, err := ParseRecordingFinalizedV1("018f0000-0000-7000-8000-000000000091", []byte(`{
		"contract":"sd-recording-catalog.v1",
		"type":"recording_finalized",
		"recording":{
			"version":1,
			"recording_id":"rec_0123456789abcdef0123456789abcdef",
			"revision":"rev_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"start_at":"2026-08-26T14:32:00+08:00",
			"end_at":"2026-08-26T14:34:15+08:00",
			"duration_ms":135000,
			"size_bytes":33750000,
			"type":"event",
			"codec":"h264",
			"width":1920,
			"height":1080,
			"fps_num":25,
			"fps_den":1,
			"complete":true,
			"seekable":true,
			"thumbnail_etag":"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"index_version":1
		},
		"thumbnail":{
			"status":"pending",
			"content_type":"image/jpeg",
			"size_bytes":12456,
			"etag":"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
		}
	}`))
	if err != nil {
		t.Fatalf("ParseRecordingFinalizedV1() error = %v", err)
	}
	if event.Recording.RecordingID != "rec_0123456789abcdef0123456789abcdef" || !event.Recording.Seekable {
		t.Fatalf("event = %#v", event)
	}
}

func TestParseRecordingFinalizedV1FailsClosed(t *testing.T) {
	t.Parallel()
	base := `{
		"contract":"sd-recording-catalog.v1","type":"recording_finalized",
		"recording":{
			"version":1,"recording_id":"rec_0123456789abcdef0123456789abcdef",
			"revision":"rev_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"start_at":"2026-08-26T14:32:00+08:00","end_at":"2026-08-26T14:34:15+08:00",
			"duration_ms":135000,"size_bytes":33750000,"type":"event","codec":"h264",
			"width":1920,"height":1080,"fps_num":25,"fps_den":1,"complete":true,
			"seekable":true,"thumbnail_etag":"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"index_version":1
		},
		"thumbnail":{"status":"pending","content_type":"image/jpeg","size_bytes":12456,
		"etag":"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	}`
	for name, payload := range map[string]string{
		"filename identity": strings.Replace(base, `"version":1`, `"version":1,"filename":"legacy.h264"`, 1),
		"uppercase id":      strings.Replace(base, "rec_0123", "rec_A123", 1),
		"incomplete":        strings.Replace(base, `"complete":true`, `"complete":false`, 1),
		"unknown contract":  strings.Replace(base, "sd-recording-catalog.v1", "sd-recording-catalog.v2", 1),
		"unsafe integer":    strings.Replace(base, `"duration_ms":135000`, `"duration_ms":9007199254740992`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseRecordingFinalizedV1("018f0000-0000-7000-8000-000000000091", []byte(payload)); err == nil {
				t.Fatal("ParseRecordingFinalizedV1() accepted invalid payload")
			}
		})
	}
}

func TestLocalRecordingListItemNeverSerializesStorageIdentity(t *testing.T) {
	t.Parallel()
	item := LocalRecordingListItem{
		RecordingID: "rec_0123456789abcdef0123456789abcdef",
		Revision:    "rev_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Playable:    true,
		Seekable:    true,
		Thumbnail: LocalRecordingThumbnail{Status: "pending",
			ETag: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
	}
	payload, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	text := string(payload)
	if strings.Contains(text, "filename") || strings.Contains(text, "object_key") || strings.Contains(text, "/mnt/sd/") {
		t.Fatalf("serialized projection leaked storage identity: %s", text)
	}
}

type localRecordingRepositoryStub struct {
	assetRepositoryStub
	items []LocalRecordingProjection
}

func (repository *localRecordingRepositoryStub) ListLocalRecordingProjections(
	context.Context, string, string, int, string, string, string,
) ([]LocalRecordingProjection, string, error) {
	return repository.items, "cursor-token", nil
}
func (repository *localRecordingRepositoryStub) GetLocalRecordingThumbnail(
	context.Context, string, string, string,
) (string, string, error) {
	return "private/object.jpg", repository.items[0].Thumbnail.ETag, nil
}

func TestListLocalRecordingsSignsOnlyReadyPrivateThumbnail(t *testing.T) {
	t.Parallel()
	const etag = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	repository := &localRecordingRepositoryStub{items: []LocalRecordingProjection{
		{
			LocalRecordingListItem: LocalRecordingListItem{
				RecordingID: "rec_0123456789abcdef0123456789abcdef",
				Revision:    "rev_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				Thumbnail:   LocalRecordingThumbnail{Status: "ready", ETag: etag},
			},
			ThumbnailObjectKey: "staging/local-recording-thumbnails/device/rec/etag.jpg",
		},
		{
			LocalRecordingListItem: LocalRecordingListItem{
				RecordingID: "rec_1123456789abcdef0123456789abcdef",
				Revision:    "rev_1123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				Thumbnail:   LocalRecordingThumbnail{Status: "pending", ETag: etag},
			},
		},
	}}
	service := NewAssetService(&objectStoreStub{}, &commanderStub{}, repository, "staging", nil, nil)
	page, err := service.ListLocalRecordings(
		context.Background(), "binding-id", "fdr-0001", "", "", "", 20,
	)
	if err != nil {
		t.Fatalf("ListLocalRecordings() error = %v", err)
	}
	if page.Contract != LocalRecordingCatalogContractV1 || page.NextCursor == nil ||
		*page.NextCursor != "cursor-token" || len(page.Items) != 2 {
		t.Fatalf("page = %#v", page)
	}
	if !strings.HasPrefix(page.Items[0].Thumbnail.URL, "/api/v1/openapi/devices/") ||
		page.Items[0].Thumbnail.ExpiresAt == "" {
		t.Fatalf("ready thumbnail = %#v", page.Items[0].Thumbnail)
	}
	if page.Items[1].Thumbnail.URL != "" || page.Items[1].Thumbnail.ExpiresAt != "" {
		t.Fatalf("pending thumbnail leaked URL: %#v", page.Items[1].Thumbnail)
	}
	payload, _ := json.Marshal(page)
	if strings.Contains(string(payload), "object_key") {
		t.Fatalf("page leaked private object key field: %s", payload)
	}
}

func TestGetLocalRecordingThumbnailRechecksBindingAndIntegrity(t *testing.T) {
	t.Parallel()
	jpeg := []byte{
		0xff, 0xd8,
		0xff, 0xe0, 0x00, 0x04, 0x00, 0x00,
		0xff, 0xc0, 0x00, 0x0b, 0x08, 0x00, 0xb4, 0x01, 0x40, 0x01, 0x01, 0x11,
		0xff, 0xd9,
	}
	digest := sha256.Sum256(jpeg)
	etag := "sha256:" + hex.EncodeToString(digest[:])
	repository := &localRecordingRepositoryStub{items: []LocalRecordingProjection{{
		LocalRecordingListItem: LocalRecordingListItem{
			RecordingID: "rec_0123456789abcdef0123456789abcdef",
			Thumbnail:   LocalRecordingThumbnail{Status: "ready", ETag: etag},
		},
	}}}
	store := &objectStoreStub{objects: map[string][]byte{"private/object.jpg": jpeg}}
	service := NewAssetService(store, &commanderStub{}, repository, "staging", nil, nil)
	content, err := service.GetLocalRecordingThumbnail(
		context.Background(), "binding-id", "fdr-0001", "rec_0123456789abcdef0123456789abcdef",
	)
	if err != nil {
		t.Fatalf("GetLocalRecordingThumbnail() error = %v", err)
	}
	if !bytes.Equal(content.Payload, jpeg) || content.ETag != etag || store.downloadKey != "private/object.jpg" {
		t.Fatalf("content=%#v downloadKey=%q", content, store.downloadKey)
	}
}

func TestLegacyLocalRecordingOffsetCompatibilityUsesSdFiles(t *testing.T) {
	t.Parallel()
	commander := &commanderStub{ack: map[string]any{
		"code": float64(0), "files": []any{}, "offset": float64(20), "limit": float64(20),
	}}
	service := NewAssetService(&objectStoreStub{}, commander, &localRecordingRepositoryStub{}, "staging", nil, nil)
	result, err := service.ListLegacyLocalRecordings(
		context.Background(), "livpet-staging", "device-id", "fdr-0001", "2026-08-26", 20, 20,
	)
	if err != nil {
		t.Fatalf("ListLegacyLocalRecordings() error = %v", err)
	}
	if result["offset"] != float64(20) || commander.input.Code != "sd_files" || !commander.wait {
		t.Fatalf("result=%#v command=%#v wait=%t", result, commander.input, commander.wait)
	}
	if commander.dpCalls != 1 {
		t.Fatalf("sd_files command bypassed product DP contract: dpCalls=%d", commander.dpCalls)
	}
	var value map[string]any
	if err := json.Unmarshal(commander.input.Value, &value); err != nil ||
		value["op"] != "list" || value["offset"] != float64(20) || value["limit"] != float64(20) ||
		value["date"] != "2026-08-26" {
		t.Fatalf("legacy value=%#v error=%v", value, err)
	}
}
