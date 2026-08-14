package device

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type publisherStub struct {
	topic   string
	payload []byte
	err     error
}

func (publisher *publisherStub) Publish(_ context.Context, topic string, payload []byte) error {
	publisher.topic = topic
	publisher.payload = append([]byte(nil), payload...)
	return publisher.err
}

type ackWaiterStub struct {
	ack map[string]any
	err error
}

func (waiter ackWaiterStub) Wait(context.Context, string, string, time.Duration) (map[string]any, error) {
	return waiter.ack, waiter.err
}

type commandRepositoryStub struct{ commands []Command }

func (repository *commandRepositoryStub) Create(_ context.Context, command Command) error {
	repository.commands = append(repository.commands, command)
	return nil
}

func (repository *commandRepositoryStub) Complete(_ context.Context, id, status string, ack map[string]any, errorCode string) error {
	for index := range repository.commands {
		if repository.commands[index].ID == id {
			repository.commands[index].Status = status
			repository.commands[index].ACK = ack
			repository.commands[index].ErrorCode = errorCode
		}
	}
	return nil
}

func TestSendCommandsPreservesLivPetPayloadAndResponseShape(t *testing.T) {
	t.Parallel()
	publisher := &publisherStub{}
	repository := &commandRepositoryStub{}
	service := NewService(publisher, ackWaiterStub{ack: map[string]any{"session_id": "session-1", "code": float64(0), "schedules": []any{}}}, repository,
		"aws-iot-gateway-staging", func() (string, error) { return "session-1", nil })

	result, err := service.SendCommands(context.Background(), "livpet-staging", "device-id", "fdr-0001", []CommandInput{{
		Code: "feed", Value: json.RawMessage(`{"portions":2}`),
	}}, true)
	if err != nil {
		t.Fatalf("SendCommands() error = %v", err)
	}
	if publisher.topic != "cmd/aws-iot-gateway-staging/fdr-0001/req" {
		t.Fatalf("topic = %q", publisher.topic)
	}
	var payload map[string]any
	if err := json.Unmarshal(publisher.payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload["action"] != "feed" || payload["session_id"] != "session-1" || payload["portions"] != float64(2) {
		t.Fatalf("payload = %#v", payload)
	}
	if len(result.Results) != 1 || result.Results[0].Status != "success" || result.Results[0].Code != "feed" || result.Results[0].CommandID != "session-1" {
		t.Fatalf("result = %#v", result)
	}
	if len(repository.commands) != 1 || repository.commands[0].Status != "acked" {
		t.Fatalf("commands = %#v", repository.commands)
	}
}

func TestSendCommandsMapsBusyAndOfflineWithoutPretendingSuccess(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name string
		ack  map[string]any
		err  error
		want error
	}{
		{name: "busy", ack: map[string]any{"session_id": "session-1", "code": float64(-2)}, want: ErrDeviceBusy},
		{name: "offline", err: ErrACKTimeout, want: ErrDeviceOffline},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			service := NewService(&publisherStub{}, ackWaiterStub{ack: testCase.ack, err: testCase.err}, &commandRepositoryStub{},
				"aws-iot-gateway-staging", func() (string, error) { return "session-1", nil })
			_, err := service.SendCommands(context.Background(), "livpet-staging", "device-id", "fdr-0001", []CommandInput{{Code: "feed", Value: json.RawMessage(`{}`)}}, true)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("SendCommands() error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestSendCommandsWithoutACKReturnsAcceptedResult(t *testing.T) {
	t.Parallel()
	service := NewService(&publisherStub{}, ackWaiterStub{err: errors.New("must not wait")}, &commandRepositoryStub{},
		"aws-iot-gateway-staging", func() (string, error) { return "session-1", nil })
	result, err := service.SendCommands(context.Background(), "livpet-staging", "device-id", "fdr-0001", []CommandInput{{Code: "led", Value: json.RawMessage(`true`)}}, false)
	if err != nil {
		t.Fatalf("SendCommands() error = %v", err)
	}
	if result.Results[0].Status != "accepted" {
		t.Fatalf("result = %#v", result)
	}
}
