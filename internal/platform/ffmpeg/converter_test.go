package ffmpeg

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestConverterAppliesAnIndependentProcessingTimeout(t *testing.T) {
	t.Parallel()
	command := filepath.Join(t.TempDir(), "slow-ffmpeg")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nexec sleep 5\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	converter, err := NewConverter(command)
	if err != nil {
		t.Fatal(err)
	}
	converter.timeout = 25 * time.Millisecond

	started := time.Now()
	if _, err := converter.H264ToJPEG(context.Background(), []byte{0x00, 0x00, 0x00, 0x01, 0x65}); err == nil {
		t.Fatal("H264ToJPEG() must time out independently of the request context")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("H264ToJPEG() timeout took %s", elapsed)
	}
}

func TestConverterRejectsOversizedJPEGOutput(t *testing.T) {
	t.Parallel()
	command := filepath.Join(t.TempDir(), "large-output-ffmpeg")
	script := "#!/bin/sh\ncat >/dev/null\nprintf '\\377\\330'\ni=0\nwhile [ $i -lt 100 ]; do printf '0123456789'; i=$((i+1)); done\nprintf '\\377\\331'\n"
	if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	converter, err := NewConverter(command)
	if err != nil {
		t.Fatal(err)
	}
	converter.maxOutputBytes = 128

	_, err = converter.H264ToJPEG(context.Background(), []byte{0x00, 0x00, 0x00, 0x01, 0x65})
	if err == nil || !strings.Contains(err.Error(), "output exceeds") {
		t.Fatalf("H264ToJPEG() error = %v", err)
	}
}

func TestConverterLimitsConcurrentProcesses(t *testing.T) {
	t.Parallel()
	command := filepath.Join(t.TempDir(), "fake-ffmpeg")
	if err := os.WriteFile(command, []byte("#!/bin/sh\ncat >/dev/null\nprintf '\\377\\330ok\\377\\331'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	converter, err := NewConverter(command)
	if err != nil {
		t.Fatal(err)
	}
	converter.slots <- struct{}{}
	defer func() { <-converter.slots }()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	if _, err := converter.H264ToJPEG(ctx, []byte{0x00, 0x00, 0x00, 0x01, 0x65}); err == nil {
		t.Fatal("H264ToJPEG() must respect cancellation while waiting for a conversion slot")
	}
}
