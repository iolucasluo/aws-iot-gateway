package ffmpeg

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	defaultConversionTimeout = 10 * time.Second
	maxH264InputBytes        = 8 << 20
	defaultMaxJPEGBytes      = 8 << 20
	maxFFmpegErrorBytes      = 8 << 10
	maxDecodedPixels         = 4096 * 4096
	maxFFmpegAllocationBytes = 64 << 20
)

type Converter struct {
	binary         string
	timeout        time.Duration
	maxOutputBytes int
	slots          chan struct{}
}

func NewConverter(binary string) (*Converter, error) {
	binary = strings.TrimSpace(binary)
	if binary == "" {
		return nil, fmt.Errorf("ffmpeg binary is required")
	}
	resolved, err := exec.LookPath(binary)
	if err != nil {
		return nil, fmt.Errorf("locate ffmpeg binary: %w", err)
	}
	return &Converter{
		binary: resolved, timeout: defaultConversionTimeout,
		maxOutputBytes: defaultMaxJPEGBytes, slots: make(chan struct{}, 1),
	}, nil
}

func (converter *Converter) H264ToJPEG(ctx context.Context, payload []byte) ([]byte, error) {
	if converter == nil || converter.binary == "" || converter.timeout <= 0 ||
		converter.maxOutputBytes <= 0 || converter.slots == nil || len(payload) == 0 || len(payload) > maxH264InputBytes {
		return nil, fmt.Errorf("convert H.264 snapshot: invalid input")
	}
	processingCtx, cancel := context.WithTimeout(ctx, converter.timeout)
	defer cancel()
	select {
	case converter.slots <- struct{}{}:
		defer func() { <-converter.slots }()
	case <-processingCtx.Done():
		return nil, fmt.Errorf("convert H.264 snapshot: wait for processing slot: %w", processingCtx.Err())
	}

	command := exec.CommandContext(processingCtx, converter.binary,
		"-nostdin", "-hide_banner", "-loglevel", "error",
		"-max_alloc", strconv.Itoa(maxFFmpegAllocationBytes),
		"-filter_threads", "1",
		"-threads", "1", "-max_pixels", strconv.Itoa(maxDecodedPixels),
		"-f", "h264", "-i", "pipe:0",
		"-frames:v", "1", "-threads", "1", "-f", "image2pipe", "-vcodec", "mjpeg", "pipe:1",
	)
	command.WaitDelay = time.Second
	command.Stdin = bytes.NewReader(payload)
	output := &cappedBuffer{limit: converter.maxOutputBytes}
	stderr := &cappedBuffer{limit: maxFFmpegErrorBytes}
	command.Stdout = output
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		if processingCtx.Err() != nil {
			return nil, fmt.Errorf("convert H.264 snapshot: %w", processingCtx.Err())
		}
		return nil, fmt.Errorf("convert H.264 snapshot: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if output.overflow {
		return nil, fmt.Errorf("convert H.264 snapshot: output exceeds %d bytes", converter.maxOutputBytes)
	}
	jpeg := output.Bytes()
	if len(jpeg) < 4 || jpeg[0] != 0xff || jpeg[1] != 0xd8 || jpeg[len(jpeg)-2] != 0xff || jpeg[len(jpeg)-1] != 0xd9 {
		return nil, fmt.Errorf("convert H.264 snapshot: ffmpeg returned invalid JPEG")
	}
	return append([]byte(nil), jpeg...), nil
}

type cappedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (buffer *cappedBuffer) Write(payload []byte) (int, error) {
	written := len(payload)
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining > 0 {
		if len(payload) > remaining {
			buffer.overflow = true
			payload = payload[:remaining]
		}
		_, _ = buffer.buffer.Write(payload)
	} else if len(payload) > 0 {
		buffer.overflow = true
	}
	return written, nil
}

func (buffer *cappedBuffer) Bytes() []byte  { return buffer.buffer.Bytes() }
func (buffer *cappedBuffer) String() string { return buffer.buffer.String() }
