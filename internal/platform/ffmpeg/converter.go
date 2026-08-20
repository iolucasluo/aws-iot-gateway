package ffmpeg

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type Converter struct {
	binary string
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
	return &Converter{binary: resolved}, nil
}

func (converter *Converter) H264ToJPEG(ctx context.Context, payload []byte) ([]byte, error) {
	if converter == nil || converter.binary == "" || len(payload) == 0 {
		return nil, fmt.Errorf("convert H.264 snapshot: invalid input")
	}
	command := exec.CommandContext(ctx, converter.binary,
		"-nostdin", "-hide_banner", "-loglevel", "error",
		"-f", "h264", "-i", "pipe:0",
		"-frames:v", "1", "-f", "image2pipe", "-vcodec", "mjpeg", "pipe:1",
	)
	command.Stdin = bytes.NewReader(payload)
	var output bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &output
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("convert H.264 snapshot: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	jpeg := output.Bytes()
	if len(jpeg) < 4 || jpeg[0] != 0xff || jpeg[1] != 0xd8 || jpeg[len(jpeg)-2] != 0xff || jpeg[len(jpeg)-1] != 0xd9 {
		return nil, fmt.Errorf("convert H.264 snapshot: ffmpeg returned invalid JPEG")
	}
	return append([]byte(nil), jpeg...), nil
}
