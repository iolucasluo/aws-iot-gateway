package media

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func sdPlaybackOps() []any {
	return []any{"load", "play", "pause", "resume", "seek", "skip", "stop", "get_state"}
}

// A rejected sd-playback.v1 negotiation must name the field that failed, otherwise the
// only signal left to operators and viewers is a bare 409 stream_rejected.
func TestParsePlaybackControlReportsTheOffendingField(t *testing.T) {
	accepted := StreamCapabilities{
		VideoReceive: true,
		DataChannels: []string{ControlDataChannelProtocol},
	}

	cases := []struct {
		name   string
		value  any
		expect string
	}{
		{
			name: "unsupported contract",
			value: map[string]any{
				"contract": "sd-playback.v2", "ops": sdPlaybackOps(),
				"progress_interval_ms": float64(500), "max_concurrent_sessions": float64(1),
				"supports_rate": false,
			},
			expect: "contract",
		},
		{
			name: "progress interval",
			value: map[string]any{
				"contract": "sd-playback.v1", "ops": sdPlaybackOps(),
				"progress_interval_ms": float64(250), "max_concurrent_sessions": float64(1),
				"supports_rate": false,
			},
			expect: "progress_interval_ms",
		},
		{
			name: "concurrent sessions",
			value: map[string]any{
				"contract": "sd-playback.v1", "ops": sdPlaybackOps(),
				"progress_interval_ms": float64(500), "max_concurrent_sessions": float64(2),
				"supports_rate": false,
			},
			expect: "max_concurrent_sessions",
		},
		{
			name: "playback rate",
			value: map[string]any{
				"contract": "sd-playback.v1", "ops": sdPlaybackOps(),
				"progress_interval_ms": float64(500), "max_concurrent_sessions": float64(1),
				"supports_rate": true,
			},
			expect: "supports_rate",
		},
		{
			name: "operation list",
			value: map[string]any{
				"contract": "sd-playback.v1", "ops": []any{"play"},
				"progress_interval_ms": float64(500), "max_concurrent_sessions": float64(1),
				"supports_rate": false,
			},
			expect: "ops",
		},
		{
			name:   "field shape",
			value:  "not-an-object",
			expect: "playback_control",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := parsePlaybackControl(testCase.value, accepted)
			if !errors.Is(err, ErrStreamRejected) {
				t.Fatalf("err = %v, want ErrStreamRejected", err)
			}
			if !strings.Contains(err.Error(), testCase.expect) {
				t.Fatalf("err = %v, want it to name %q so the rejection is locatable",
					err, testCase.expect)
			}
		})
	}
}

// Losing the control data channel is the difference between "playback unsupported" and
// "device is busy", so it must be reported rather than collapsed into a bare rejection.
func TestParsePlaybackControlReportsMissingControlChannel(t *testing.T) {
	value := map[string]any{
		"contract": "sd-playback.v1", "ops": sdPlaybackOps(),
		"progress_interval_ms": float64(500), "max_concurrent_sessions": float64(1),
		"supports_rate": false,
	}

	_, err := parsePlaybackControl(value, StreamCapabilities{VideoReceive: true})
	if !errors.Is(err, ErrStreamRejected) {
		t.Fatalf("err = %v, want ErrStreamRejected", err)
	}
	if !strings.Contains(err.Error(), "data_channel") {
		t.Fatalf("err = %v, want it to name the missing control data channel", err)
	}
}

// The field detail is worthless if the only production caller collapses it into a bare
// rejection: operators and viewers see exactly the error Create returns.
func TestCreateStreamReportsRejectedPlaybackControlField(t *testing.T) {
	t.Parallel()
	ack := map[string]any{
		"code": float64(0), "action": "stream", "type": "webrtc", "start": float64(1),
		"status": "ready", "viewer_client_id": "viewer-playback",
		"capability_contract_version": "webrtc.v1", "firmware_version": "v1.2.3",
		"supported": map[string]any{
			"video_receive": true, "audio_receive": true, "audio_send": true,
			"data_channels": []any{"explorex.control.v1"},
		},
		"accepted": map[string]any{
			"video_receive": true, "audio_receive": false, "audio_send": false,
			"data_channels": []any{ControlDataChannelProtocol},
		},
		"playback_control": map[string]any{
			"contract": "sd-playback.v2", "ops": sdPlaybackOps(),
			"progress_interval_ms": float64(500), "max_concurrent_sessions": float64(1),
			"supports_rate": false,
		},
	}
	service := NewStreamService(kvsStub{}, &commanderStub{ack: ack}, &streamRepositoryStub{},
		"aws-iot-gateway-staging-", func() (string, error) { return "viewer-playback", nil })
	_, err := service.Create(context.Background(), "livpet-staging", "device-id", "binding-id",
		"fdr-0001", StreamCapabilities{VideoReceive: true, DataChannels: []string{ControlDataChannelProtocol}})
	if !errors.Is(err, ErrStreamRejected) {
		t.Fatalf("Create() error = %v, want ErrStreamRejected", err)
	}
	if !strings.Contains(err.Error(), "contract") {
		t.Fatalf("Create() error = %v, want the rejected playback_control field so operators can locate it", err)
	}
}
