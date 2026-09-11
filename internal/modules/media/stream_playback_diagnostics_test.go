package media

import (
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
