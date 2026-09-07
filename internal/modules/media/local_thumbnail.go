package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

var ErrLocalThumbnailTaskNotFound = errors.New("local recording thumbnail task not found")

type LocalThumbnailTask struct {
	Operation     string
	OutboxID      string
	RecordingID   string
	Revision      string
	ETag          string
	SizeBytes     int64
	DeviceID      string
	DeviceSN      string
	InternalAppID string
	Attempts      int
	ObjectKey     string
}

type LocalThumbnailRepository interface {
	ClaimLocalThumbnail(context.Context) (LocalThumbnailTask, error)
	CompleteLocalThumbnail(context.Context, string, string, string) error
	CompleteLocalThumbnailDeletion(context.Context, string, string) error
	RetryLocalThumbnail(context.Context, string, string, int) error
}

func ValidateLocalThumbnailJPEG(payload []byte, etag string) bool {
	if len(payload) < 4 || len(payload) > 256*1024 ||
		payload[0] != 0xff || payload[1] != 0xd8 ||
		payload[len(payload)-2] != 0xff || payload[len(payload)-1] != 0xd9 ||
		!localThumbnailJPEGDimensionsAre(payload, 320, 180) {
		return false
	}
	digest := sha256.Sum256(payload)
	return "sha256:"+hex.EncodeToString(digest[:]) == etag
}

func localThumbnailJPEGDimensionsAre(payload []byte, expectedWidth, expectedHeight int) bool {
	for offset := 2; offset+4 <= len(payload); {
		for offset < len(payload) && payload[offset] == 0xff {
			offset++
		}
		if offset >= len(payload) {
			return false
		}
		marker := payload[offset]
		offset++
		if marker == 0xd9 || marker == 0xda {
			return false
		}
		if marker == 0x01 || marker >= 0xd0 && marker <= 0xd7 {
			continue
		}
		if offset+2 > len(payload) {
			return false
		}
		segmentLength := int(payload[offset])<<8 | int(payload[offset+1])
		if segmentLength < 2 || offset+segmentLength > len(payload) {
			return false
		}
		isSOF := marker >= 0xc0 && marker <= 0xc3 || marker >= 0xc5 && marker <= 0xc7 ||
			marker >= 0xc9 && marker <= 0xcb || marker >= 0xcd && marker <= 0xcf
		if isSOF {
			if segmentLength < 7 {
				return false
			}
			height := int(payload[offset+3])<<8 | int(payload[offset+4])
			width := int(payload[offset+5])<<8 | int(payload[offset+6])
			return width == expectedWidth && height == expectedHeight
		}
		offset += segmentLength
	}
	return false
}
