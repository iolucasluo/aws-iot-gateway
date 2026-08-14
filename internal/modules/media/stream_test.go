package media

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/device"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/awskvs"
)

type kvsStub struct{}

func (kvsStub) EnsureChannel(_ context.Context, name string) (awskvs.Channel, error) {
	return awskvs.Channel{Name: name, ARN: "arn:channel"}, nil
}

func (kvsStub) Playback(_ context.Context, channel awskvs.Channel, viewerID string, _ time.Duration) (awskvs.PlaybackConfig, error) {
	return awskvs.PlaybackConfig{
		Provider: "aws_kvs", ChannelName: channel.Name, ChannelARN: channel.ARN,
		ClientID: viewerID, WSSURL: "wss://signal.example/live?signed=true",
		ICEServers: []awskvs.ICEServer{{URLs: "stun:stun.example:443"}},
		ExpiresAt:  time.Date(2026, 8, 13, 13, 5, 0, 0, time.UTC),
	}, nil
}

type commanderStub struct {
	input device.CommandInput
	ack   map[string]any
}

func (commander *commanderStub) SendCommands(_ context.Context, _, _, _ string, commands []device.CommandInput, _ bool) (device.SendCommandsResult, error) {
	commander.input = commands[0]
	return device.SendCommandsResult{Results: []device.CommandResult{{Status: "success", Data: commander.ack}}}, nil
}

type streamRepositoryStub struct{ stream Stream }

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
