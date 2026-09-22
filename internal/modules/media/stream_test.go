package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/device"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/awskvs"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/provider"
)

type kvsStub struct{}

func (kvsStub) EnsureChannel(_ context.Context, name string) (awskvs.Channel, error) {
	return awskvs.Channel{Name: name, ExternalRef: "arn:channel"}, nil
}

func (kvsStub) Playback(_ context.Context, channel awskvs.Channel, viewerID string, _ time.Duration) (awskvs.PlaybackConfig, error) {
	return awskvs.PlaybackConfig{
		Provider: "aws_kvs", ChannelName: channel.Name, ExternalRef: channel.ExternalRef,
		ClientID: viewerID, WSSURL: "wss://signal.example/live?signed=true",
		ICEServers: []awskvs.ICEServer{{URLs: "stun:stun.example:443"}},
		ExpiresAt:  time.Date(2026, 8, 13, 13, 5, 0, 0, time.UTC),
	}, nil
}

type commanderStub struct {
	input   device.CommandInput
	ack     map[string]any
	wait    bool
	calls   int
	dpCalls int
	result  *device.SendCommandsResult
}

func (commander *commanderStub) SendCommands(_ context.Context, _, _, _ string, commands []device.CommandInput, waitACK bool) (device.SendCommandsResult, error) {
	commander.calls++
	commander.input = commands[0]
	commander.wait = waitACK
	if commander.result != nil {
		return *commander.result, nil
	}
	return device.SendCommandsResult{Results: []device.CommandResult{{Status: "success", Data: commander.ack}}}, nil
}

func (commander *commanderStub) SendDPCommands(ctx context.Context, internalAppID, deviceID, deviceSN string, commands []device.CommandInput, waitACK bool) (device.SendCommandsResult, error) {
	commander.dpCalls++
	return commander.SendCommands(ctx, internalAppID, deviceID, deviceSN, commands, waitACK)
}

func TestCreateExactStreamRejectsPartialGrantAndRevokesViewer(t *testing.T) {
	t.Parallel()
	commander := &commanderStub{ack: map[string]any{
		"code": float64(0), "action": "stream", "type": "webrtc", "start": float64(1), "status": "ready",
		"viewer_client_id": "viewer-1", "capability_contract_version": "webrtc.v1", "firmware_version": "v0.0.0",
		"supported": map[string]any{
			"video_receive": true, "audio_receive": true, "audio_send": true, "data_channels": []any{},
		},
		"accepted": map[string]any{
			"video_receive": true, "audio_receive": false, "audio_send": false, "data_channels": []any{},
		},
	}}
	repository := &streamRepositoryStub{}
	service := NewStreamService(kvsStub{}, commander, repository, "aws-iot-gateway-staging-", func() (string, error) { return "viewer-1", nil })
	requested := StreamCapabilities{VideoReceive: true, AudioReceive: true, AudioSend: true, DataChannels: []string{}}

	_, err := service.CreateExact(context.Background(), "livpet-staging", "device-id", "binding-id", "fdr-0001", requested)
	if !errors.Is(err, ErrStreamCapabilityUnavailable) {
		t.Fatalf("CreateExact() error = %v, want ErrStreamCapabilityUnavailable", err)
	}
	if repository.stream.ID != "" {
		t.Fatalf("partial exact grant persisted stream = %#v", repository.stream)
	}
	if commander.calls != 2 || commander.wait {
		t.Fatalf("partial exact grant cleanup calls = %d, last wait = %t", commander.calls, commander.wait)
	}
	var stop map[string]any
	if json.Unmarshal(commander.input.Value, &stop) != nil || stop["start"] != float64(0) || stop["viewer_client_id"] != "viewer-1" {
		t.Fatalf("partial exact grant cleanup = %#v", stop)
	}
}

type streamRepositoryStub struct{ stream Stream }

type realtimeStub struct {
	descriptor provider.RealtimeSessionDescriptor
	prepared   []provider.RealtimeSessionRequest
	closed     []provider.RealtimeSessionRef
}

func (stub *realtimeStub) PrepareSession(_ context.Context, request provider.RealtimeSessionRequest) (provider.RealtimeSessionDescriptor, error) {
	stub.prepared = append(stub.prepared, request)
	return stub.descriptor, nil
}

func (stub *realtimeStub) CloseSession(_ context.Context, session provider.RealtimeSessionRef) error {
	stub.closed = append(stub.closed, session)
	return nil
}

func (stub *realtimeStub) Capabilities() provider.RealtimeCapabilities {
	return provider.RealtimeCapabilities{Protocols: []string{"explorex-signal-v1"}}
}
func (stub *realtimeStub) Health(context.Context) error { return nil }

func TestCreateRealtimeStreamUsesProviderSessionAndRedactsViewerTicketFromDeviceCommand(t *testing.T) {
	t.Parallel()
	realtime := &realtimeStub{descriptor: provider.RealtimeSessionDescriptor{
		SessionID: "session-1", ProviderInstance: "realtime.cn_staging", Protocol: "explorex-signal-v1",
		ClientConfig: map[string]any{
			"wss_url":       "wss://rtc.example/viewer?ticket=viewer-secret",
			"ice_servers":   []any{map[string]any{"urls": []any{"stun:rtc.example:3478", "turn:rtc.example:3478?transport=udp", "turn:rtc.example:3478?transport=tcp"}, "username": "turn-user", "credential": "turn-test-only"}},
			"device_config": map[string]any{"wss_url": "wss://rtc.example/device?ticket=device-secret"},
		},
		Capabilities: map[string]any{"protocols": []any{"explorex-signal-v1"}},
		ExpiresAt:    time.Now().Add(5 * time.Minute).UTC(),
	}}
	commander := &commanderStub{ack: map[string]any{
		"code": float64(0), "action": "stream", "type": "webrtc", "start": float64(1), "status": "ready",
		"viewer_client_id": "session-1", "capability_contract_version": "webrtc.v1", "firmware_version": "dev-cn",
		"supported": map[string]any{"video_receive": true, "audio_receive": false, "audio_send": false, "data_channels": []any{}},
		"accepted":  map[string]any{"video_receive": true, "audio_receive": false, "audio_send": false, "data_channels": []any{}},
	}}
	repository := &streamRepositoryStub{}
	service := NewRealtimeStreamService(realtime, "realtime.cn_staging", commander, repository, func() (string, error) { return "viewer-rt", nil }, nil)
	result, err := service.Create(context.Background(), "demo-local", "device-1", "binding-1", "fdr-1", StreamCapabilities{VideoReceive: true, DataChannels: []string{}})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if result.Provider != "realtime.cn_staging" || result.Protocol != "explorex-signal-v1" || result.StreamID != "session-1" {
		t.Fatalf("result = %#v", result)
	}
	if _, leaked := result.ClientConfig["device_config"]; leaked || strings.Contains(fmt.Sprint(result.ClientConfig), "device-secret") {
		t.Fatalf("viewer config leaked device ticket: %#v", result.ClientConfig)
	}
	if len(realtime.prepared) != 1 || realtime.prepared[0].RequestID != "viewer-rt" {
		t.Fatalf("prepared = %#v", realtime.prepared)
	}
	var command map[string]any
	if err := json.Unmarshal(commander.input.Value, &command); err != nil {
		t.Fatal(err)
	}
	realtimeCommand, ok := command["realtime"].(map[string]any)
	if !ok || realtimeCommand["session_id"] != "session-1" || strings.Contains(string(commander.input.Value), "viewer-secret") {
		t.Fatalf("realtime command = %#v", command)
	}
	deviceConfig, ok := realtimeCommand["device_config"].(map[string]any)
	if !ok || deviceConfig["wss_url"] != "wss://rtc.example/device?ticket=device-secret" {
		t.Fatalf("device config = %#v", realtimeCommand)
	}
	ice, ok := command["ice_servers"].([]any)
	if !ok || len(ice) != 1 {
		t.Fatalf("device command missing ICE servers")
	}
	entry, ok := ice[0].(map[string]any)
	if !ok || entry["username"] != "turn-user" || entry["credential"] != "turn-test-only" {
		t.Fatal("device TURN credentials were not forwarded")
	}
	if len(deviceConfig) != 1 {
		t.Fatal("legacy strict device_config shape changed")
	}
	redacted := strings.Join(commander.input.RedactFields, ",")
	if !strings.Contains(redacted, "ice_servers") || !strings.Contains(redacted, "realtime") {
		t.Fatal("ephemeral realtime credentials must not be persisted")
	}
	// 网关注入字段必须登记出调用方合同范围，否则 DP 校验会拒绝推流。
	if trusted := strings.Join(commander.input.TrustedFields, ","); trusted != "ice_servers,realtime" {
		t.Fatalf("realtime command must declare gateway-injected DP fields, got %q", trusted)
	}
	if err := service.Delete(context.Background(), "demo-local", "device-1", "binding-1", "fdr-1", "session-1"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if len(realtime.closed) != 1 || realtime.closed[0].ID != "session-1" || realtime.closed[0].ProviderInstance != "realtime.cn_staging" {
		t.Fatalf("closed = %#v", realtime.closed)
	}
}

func (repository *streamRepositoryStub) Create(_ context.Context, stream Stream) error {
	repository.stream = stream
	return nil
}

func (repository *streamRepositoryStub) Find(_ context.Context, bindingID, deviceSN, streamID string) (bool, error) {
	return repository.stream.BindingID == bindingID && repository.stream.DeviceSN == deviceSN && repository.stream.ID == streamID, nil
}

func (repository *streamRepositoryStub) Close(_ context.Context, bindingID, deviceSN, streamID string) (bool, error) {
	if repository.stream.BindingID != bindingID || repository.stream.DeviceSN != deviceSN || repository.stream.ID != streamID {
		return false, nil
	}
	repository.stream.Status = "closed"
	return true, nil
}

func TestCreateStreamReturnsShortLivedKVSViewerConfigAfterDeviceACK(t *testing.T) {
	t.Parallel()
	commander := &commanderStub{ack: map[string]any{
		"code": float64(0), "action": "stream", "type": "webrtc", "start": float64(1), "status": "ready",
		"viewer_client_id": "viewer-1", "capability_contract_version": "webrtc.v1",
		"firmware_version": "v0.0.0",
		"supported": map[string]any{
			"video_receive": true, "audio_receive": true, "audio_send": true,
			"data_channels": []any{"explorex.control.v1"},
		},
		"accepted": map[string]any{
			"video_receive": true, "audio_receive": false, "audio_send": false,
			"data_channels": []any{},
		},
	}}
	repository := &streamRepositoryStub{}
	service := NewStreamService(kvsStub{}, commander, repository, "aws-iot-gateway-staging-", func() (string, error) { return "viewer-1", nil })
	requested := StreamCapabilities{VideoReceive: true, DataChannels: []string{}}
	result, err := service.Create(context.Background(), "livpet-staging", "device-id", "binding-id", "fdr-0001", requested)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if result.StreamID != "viewer-1" || result.Signaling.WSSURL == "" || len(result.ICEServers) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if repository.stream.ChannelARN != "arn:channel" || repository.stream.Status != "ready" {
		t.Fatalf("stream = %#v", repository.stream)
	}
	if commander.input.Code != "stream" {
		t.Fatalf("command = %#v", commander.input)
	}
	if commander.dpCalls != 1 {
		t.Fatalf("stream command bypassed product DP contract: dpCalls=%d", commander.dpCalls)
	}
	if result.CapabilityContractVersion != "webrtc.v1" || result.FirmwareVersion != "v0.0.0" ||
		!result.SupportedCapabilities.AudioReceive || !result.GrantedCapabilities.VideoReceive {
		t.Fatalf("capability response = %#v", result)
	}
	var commandValue map[string]any
	if err := json.Unmarshal(commander.input.Value, &commandValue); err != nil {
		t.Fatalf("decode stream command: %v", err)
	}
	capabilities, _ := commandValue["requested_capabilities"].(map[string]any)
	if commandValue["viewer_client_id"] != "viewer-1" || capabilities["video_receive"] != true {
		t.Fatalf("stream command = %#v", commandValue)
	}
}

func TestCreateStreamRejectsUntrustedCapabilityACK(t *testing.T) {
	t.Parallel()
	validACK := func() map[string]any {
		return map[string]any{
			"code": float64(0), "action": "stream", "type": "webrtc", "start": float64(1), "status": "ready",
			"viewer_client_id": "viewer-1", "capability_contract_version": "webrtc.v1", "firmware_version": "v0.0.0",
			"supported": map[string]any{
				"video_receive": true, "audio_receive": true, "audio_send": true,
				"data_channels": []any{"explorex.control.v1"},
			},
			"accepted": map[string]any{
				"video_receive": true, "audio_receive": false, "audio_send": false, "data_channels": []any{},
			},
		}
	}
	for _, testCase := range []struct {
		name string
		edit func(map[string]any)
	}{
		{name: "wrong viewer", edit: func(ack map[string]any) { ack["viewer_client_id"] = "viewer-other" }},
		{name: "missing action", edit: func(ack map[string]any) { delete(ack, "action") }},
		{name: "wrong contract", edit: func(ack map[string]any) { ack["capability_contract_version"] = "webrtc.v2" }},
		{name: "grant outside request", edit: func(ack map[string]any) { ack["accepted"].(map[string]any)["audio_receive"] = true }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ack := validACK()
			testCase.edit(ack)
			service := NewStreamService(kvsStub{}, &commanderStub{ack: ack}, &streamRepositoryStub{}, "aws-iot-gateway-staging-", func() (string, error) { return "viewer-1", nil })
			_, err := service.Create(context.Background(), "livpet-staging", "device-id", "binding-id", "fdr-0001", StreamCapabilities{VideoReceive: true, DataChannels: []string{}})
			if !errors.Is(err, ErrStreamRejected) {
				t.Fatalf("Create() error = %v, want ErrStreamRejected", err)
			}
		})
	}
}

func TestCreateStreamRejectsInvalidRequestedCapabilities(t *testing.T) {
	t.Parallel()
	service := NewStreamService(kvsStub{}, &commanderStub{}, &streamRepositoryStub{}, "aws-iot-gateway-staging-", func() (string, error) { return "viewer-1", nil })
	for _, requested := range []StreamCapabilities{
		{},
		{VideoReceive: true, DataChannels: []string{"unknown.v1"}},
		{VideoReceive: true, DataChannels: []string{"explorex.control.v1", "explorex.control.v1"}},
	} {
		if _, err := service.Create(context.Background(), "livpet-staging", "device-id", "binding-id", "fdr-0001", requested); !errors.Is(err, ErrInvalidCapabilities) {
			t.Fatalf("Create(%#v) error = %v, want ErrInvalidCapabilities", requested, err)
		}
	}
}

func TestCreateStreamReturnsStrictPlaybackCapabilityGrant(t *testing.T) {
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
			"data_channels": []any{"explorex.control.v1"},
		},
		"playback_control": map[string]any{
			"contract":             "sd-playback.v1",
			"ops":                  []any{"load", "play", "pause", "resume", "seek", "skip", "stop", "get_state"},
			"progress_interval_ms": float64(500), "max_concurrent_sessions": float64(1),
			"supports_rate": false,
		},
	}
	service := NewStreamService(kvsStub{}, &commanderStub{ack: ack}, &streamRepositoryStub{},
		"aws-iot-gateway-staging-", func() (string, error) { return "viewer-playback", nil })
	result, err := service.Create(context.Background(), "livpet-staging", "device-id", "binding-id",
		"fdr-0001", StreamCapabilities{VideoReceive: true, DataChannels: []string{ControlDataChannelProtocol}})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if result.PlaybackControl == nil || result.PlaybackControl.Contract != "sd-playback.v1" ||
		len(result.PlaybackControl.Ops) != 8 || result.PlaybackControl.MaxConcurrentSessions != 1 {
		t.Fatalf("playback capability = %#v", result.PlaybackControl)
	}
}

func TestValidateDeviceICEServers(t *testing.T) {
	tests := []struct {
		name    string
		servers []ICEServer
		valid   bool
	}{
		{"legacy-empty", nil, true},
		{"stun", []ICEServer{{URLs: "stun:host:3478"}}, true},
		{"turn-udp-tcp", []ICEServer{{URLs: []any{"turn:host:3478?transport=udp", "turn:host:3478?transport=tcp"}, Username: "u", Credential: "p"}}, true},
		{"secure-turn", []ICEServer{{URLs: "turns:host:5349", Username: "u", Credential: strings.Repeat("a", 256)}}, true},
		{"four-urls", []ICEServer{{URLs: []string{"stun:a", "stun:b", "stun:c", "stun:d"}}}, true},
		{"five-urls", []ICEServer{{URLs: []string{"stun:a", "stun:b", "stun:c", "stun:d", "stun:e"}}}, false},
		{"empty-urls", []ICEServer{{URLs: []any{}}}, false},
		{"nil-urls", []ICEServer{{}}, false},
		{"wrong-type", []ICEServer{{URLs: []any{42}}}, false},
		{"wrong-scheme", []ICEServer{{URLs: "https://host"}}, false},
		{"empty-address", []ICEServer{{URLs: "stun:"}}, false},
		{"space-in-uri", []ICEServer{{URLs: "stun: host"}}, false},
		{"long-uri", []ICEServer{{URLs: "stun:" + strings.Repeat("a", 252)}}, false},
		{"turn-no-credential", []ICEServer{{URLs: "turn:host", Username: "u"}}, false},
		{"turn-no-username", []ICEServer{{URLs: "turn:host", Credential: "p"}}, false},
		{"long-credential", []ICEServer{{URLs: "turn:host", Username: "u", Credential: strings.Repeat("a", 257)}}, false},
		{"escaped-credential", []ICEServer{{URLs: "turn:host", Username: "u", Credential: "p\\q"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDeviceICEServers(tt.servers)
			if (err == nil) != tt.valid {
				t.Fatalf("valid=%v error=%v", tt.valid, err)
			}
		})
	}
}

func TestCreateRealtimeStreamRejectsInvalidICEBeforeCommand(t *testing.T) {
	realtime := &realtimeStub{descriptor: provider.RealtimeSessionDescriptor{
		SessionID: "s", ProviderInstance: "realtime.cn_staging", Protocol: "explorex-signal-v1",
		ClientConfig: map[string]any{
			"wss_url": "ws://host/viewer", "device_config": map[string]any{"wss_url": "ws://host/device"},
			"ice_servers": []any{map[string]any{"urls": "turn:host:3478"}},
		},
	}}
	commander := &commanderStub{}
	service := NewRealtimeStreamService(realtime, "realtime.cn_staging", commander, &streamRepositoryStub{}, func() (string, error) { return "r", nil }, nil)
	_, err := service.Create(context.Background(), "demo", "device", "binding", "fdr-1", StreamCapabilities{VideoReceive: true, DataChannels: []string{}})
	if !errors.Is(err, ErrStreamRejected) || commander.dpCalls != 0 || len(realtime.closed) != 1 {
		t.Fatalf("invalid ICE must reject before MQTT and close session: err=%v calls=%d closed=%d", err, commander.dpCalls, len(realtime.closed))
	}
}

func TestParseICEServersKeepsEmptyWireArray(t *testing.T) {
	for _, input := range []any{nil, []ICEServer(nil), []any{}} {
		servers, err := parseICEServers(input)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(servers)
		if err != nil || string(encoded) != "[]" {
			t.Fatalf("empty ICE must be array: %s %v", encoded, err)
		}
	}
}
