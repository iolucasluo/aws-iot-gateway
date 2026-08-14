package media

import (
	"context"
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

type commanderStub struct{ input device.CommandInput }

func (commander *commanderStub) SendCommands(_ context.Context, _, _, _ string, commands []device.CommandInput, _ bool) (device.SendCommandsResult, error) {
	commander.input = commands[0]
	return device.SendCommandsResult{Results: []device.CommandResult{{Status: "success", Data: map[string]any{"code": float64(0)}}}}, nil
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
	commander := &commanderStub{}
	repository := &streamRepositoryStub{}
	service := NewStreamService(kvsStub{}, commander, repository, "aws-iot-gateway-staging-", func() (string, error) { return "viewer-1", nil })
	result, err := service.Create(context.Background(), "livpet-staging", "device-id", "binding-id", "fdr-0001")
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
}
