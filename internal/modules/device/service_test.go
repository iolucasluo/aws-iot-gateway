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

type shadowPublisherStub struct {
	publisherStub
	shadow []byte
}

func (publisher *shadowPublisherStub) GetShadow(context.Context, string) ([]byte, error) {
	return append([]byte(nil), publisher.shadow...), publisher.err
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

func TestGetShadowReturnsReportedProjectionWithoutAWSMetadata(t *testing.T) {
	t.Parallel()
	publisher := &shadowPublisherStub{shadow: []byte(`{
		"state":{"reported":{"sn":"fdr-0001","pid":"feeder-x1","wifi_mac":"48:a4:fd:7d:6f:0b","online":true}},
		"metadata":{"reported":{"online":{"timestamp":1787121756}}},
		"version":156,"timestamp":1787121756
	}`)}
	service := NewService(publisher, ackWaiterStub{}, &commandRepositoryStub{},
		"aws-iot-gateway-staging", func() (string, error) { return "session-1", nil })

	shadow, err := service.GetShadow(context.Background(), "fdr-0001")
	if err != nil {
		t.Fatalf("GetShadow() error = %v", err)
	}
	projection, ok := shadow.(map[string]any)
	if !ok {
		t.Fatalf("GetShadow() = %#v, want reported projection", shadow)
	}
	if projection["sn"] != "fdr-0001" || projection["wifi_mac"] != "48:a4:fd:7d:6f:0b" || projection["online"] != true {
		t.Fatalf("reported projection = %#v", projection)
	}
	if _, leaked := projection["state"]; leaked {
		t.Fatalf("reported projection leaked AWS envelope: %#v", projection)
	}
}

func TestGetShadowRejectsIncompleteReportedProjection(t *testing.T) {
	t.Parallel()
	for _, payload := range []string{
		`{"state":{}}`,
		`{"state":{"reported":{"sn":"fdr-0001","pid":"feeder-x1"}}}`,
		`{"state":{"reported":{"sn":"","pid":"feeder-x1","online":true}}}`,
	} {
		publisher := &shadowPublisherStub{shadow: []byte(payload)}
		service := NewService(publisher, ackWaiterStub{}, &commandRepositoryStub{},
			"aws-iot-gateway-staging", func() (string, error) { return "session-1", nil })
		if _, err := service.GetShadow(context.Background(), "fdr-0001"); err == nil {
			t.Fatalf("GetShadow() accepted %s", payload)
		}
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
		{name: "fractional code", ack: map[string]any{"session_id": "session-1", "code": 0.5}, want: ErrInvalidDeviceResponse},
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

func TestSendPTZMoveToWithoutACKPassesNormalizedTargetAndReturnsAcceptedNotCompleted(t *testing.T) {
	t.Parallel()
	publisher := &publisherStub{}
	repository := &commandRepositoryStub{}
	service := NewService(publisher, ackWaiterStub{err: errors.New("must not wait")}, repository,
		"aws-iot-gateway-staging", func() (string, error) { return "session-1", nil })
	result, err := service.SendCommands(context.Background(), "livpet-staging", "device-id", "fdr-0001", []CommandInput{{
		Code: "ptz", Value: json.RawMessage(`{"op":"move_to","pan_normalized":0.45,"tilt_normalized":-0.25}`),
	}}, false)
	if err != nil {
		t.Fatalf("SendCommands() error = %v", err)
	}
	if len(result.Results) != 1 || result.Results[0].Status != "accepted" || result.Results[0].Code != "ptz" {
		t.Fatalf("result = %#v", result)
	}
	var payload map[string]any
	if err := json.Unmarshal(publisher.payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload["action"] != "ptz" || payload["op"] != "move_to" || payload["session_id"] != "session-1" ||
		payload["pan_normalized"] != 0.45 || payload["tilt_normalized"] != -0.25 {
		t.Fatalf("payload = %#v", payload)
	}
	if len(repository.commands) != 1 || repository.commands[0].Status != "published" {
		t.Fatalf("commands = %#v", repository.commands)
	}
}

func TestGetPTZStatusPublishesFixedQueryAndReturnsValidatedDeviceState(t *testing.T) {
	t.Parallel()
	publisher := &publisherStub{}
	repository := &commandRepositoryStub{}
	service := NewService(publisher, ackWaiterStub{ack: map[string]any{
		"action": "ptz", "session_id": "session-ptz", "code": float64(0), "op": "get_position",
		"pan": float64(1800), "tilt": float64(-500), "limit_h": float64(4000), "limit_v": float64(2000),
		"pan_normalized": 0.45, "tilt_normalized": -0.25, "moving": true,
	}}, repository, "aws-iot-gateway-staging", func() (string, error) { return "session-ptz", nil })

	status, err := service.GetPTZStatus(context.Background(), "livpet-staging", "device-id", "fdr-0001")
	if err != nil {
		t.Fatalf("GetPTZStatus() error = %v", err)
	}
	if status.Pan != 1800 || status.Tilt != -500 || status.LimitH != 4000 || status.LimitV != 2000 ||
		status.PanNormalized != 0.45 || status.TiltNormalized != -0.25 || !status.Moving {
		t.Fatalf("status = %#v", status)
	}
	var payload map[string]any
	if err := json.Unmarshal(publisher.payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload["action"] != "ptz" || payload["op"] != "get_position" || payload["session_id"] != "session-ptz" || len(payload) != 3 {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestGetPTZStatusRejectsMalformedDeviceACK(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name string
		ack  map[string]any
	}{
		{name: "missing moving", ack: map[string]any{"action": "ptz", "session_id": "session-ptz", "code": float64(0), "op": "get_position", "pan": float64(0), "tilt": float64(0), "limit_h": float64(1), "limit_v": float64(1), "pan_normalized": 0.0, "tilt_normalized": 0.0}},
		{name: "normalized out of range", ack: map[string]any{"action": "ptz", "session_id": "session-ptz", "code": float64(0), "op": "get_position", "pan": float64(0), "tilt": float64(0), "limit_h": float64(1), "limit_v": float64(1), "pan_normalized": 1.1, "tilt_normalized": 0.0, "moving": false}},
		{name: "fractional steps", ack: map[string]any{"action": "ptz", "session_id": "session-ptz", "code": float64(0), "op": "get_position", "pan": 0.5, "tilt": float64(0), "limit_h": float64(1), "limit_v": float64(1), "pan_normalized": 0.0, "tilt_normalized": 0.0, "moving": false}},
		{name: "integer overflow", ack: map[string]any{"action": "ptz", "session_id": "session-ptz", "code": float64(0), "op": "get_position", "pan": 9223372036854775808.0, "tilt": float64(0), "limit_h": float64(1), "limit_v": float64(1), "pan_normalized": 0.0, "tilt_normalized": 0.0, "moving": false}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			service := NewService(&publisherStub{}, ackWaiterStub{ack: testCase.ack}, &commandRepositoryStub{},
				"aws-iot-gateway-staging", func() (string, error) { return "session-ptz", nil })
			_, err := service.GetPTZStatus(context.Background(), "livpet-staging", "device-id", "fdr-0001")
			if !errors.Is(err, ErrInvalidDeviceResponse) {
				t.Fatalf("GetPTZStatus() error = %v, want %v", err, ErrInvalidDeviceResponse)
			}
		})
	}
}
