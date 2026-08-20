package ffmpeg

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestConverterReturnsJPEGProducedByFFmpeg(t *testing.T) {
	t.Parallel()
	command := filepath.Join(t.TempDir(), "fake-ffmpeg")
	if err := os.WriteFile(command, []byte("#!/bin/sh\ncat >/dev/null\nprintf '\\377\\330converted\\377\\331'\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	converter, err := NewConverter(command)
	if err != nil {
		t.Fatalf("NewConverter() error = %v", err)
	}
	jpeg, err := converter.H264ToJPEG(context.Background(), []byte{0x00, 0x00, 0x00, 0x01, 0x65})
	if err != nil {
		t.Fatalf("H264ToJPEG() error = %v", err)
	}
	if string(jpeg) != "\xff\xd8converted\xff\xd9" {
		t.Fatalf("H264ToJPEG() = %x", jpeg)
	}
}
