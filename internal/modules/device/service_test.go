package device

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/ota"
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

type productDPDefinitionLoaderStub struct {
	definitions []ProductDPDefinition
	err         error
}

func (loader productDPDefinitionLoaderStub) LoadProductDPDefinitions(_ context.Context, _ string) ([]ProductDPDefinition, error) {
	return loader.definitions, loader.err
}

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

func TestGetDeviceInfoProjectionUsesProviderNeutralCommand(t *testing.T) {
	t.Parallel()
	publisher := &publisherStub{}
	repository := &commandRepositoryStub{}
	service := NewService(publisher, ackWaiterStub{ack: map[string]any{
		"code": float64(0), "device_config": map[string]any{
			"pid": "feeder-x1", "device_sn": "fdr-0001",
		},
		"wifi_mac": "48:a4:fd:7d:6f:0b", "version": "dev-cn-20260902d",
		"build": float64(790), "ip": "192.168.11.228",
	}}, repository, "cn-staging", func() (string, error) { return "device-info-session", nil })

	projection, err := service.GetDeviceInfoProjection(context.Background(), "demo-local", "device-id", "fdr-0001", "feeder-x1")
	if err != nil {
		t.Fatalf("GetDeviceInfoProjection() error = %v", err)
	}
	if projection["sn"] != "fdr-0001" || projection["pid"] != "feeder-x1" || projection["online"] != true ||
		projection["wifi_mac"] != "48:a4:fd:7d:6f:0b" || projection["version"] != "dev-cn-20260902d" ||
		projection["build"] != float64(790) || projection["ip"] != "192.168.11.228" {
		t.Fatalf("projection = %#v", projection)
	}
	var payload map[string]any
	if err := json.Unmarshal(publisher.payload, &payload); err != nil {
		t.Fatalf("decode command payload: %v", err)
	}
	if payload["action"] != "device_info" || publisher.topic != "cmd/cn-staging/fdr-0001/req" {
		t.Fatalf("command = topic %q payload %#v", publisher.topic, payload)
	}
}

func TestSendDPCommandsRejectsUnknownDPBeforePersistenceOrPublish(t *testing.T) {
	t.Parallel()
	publisher := &publisherStub{}
	repository := &commandRepositoryStub{}
	service := NewServiceWithDP(publisher, ackWaiterStub{}, repository, "aws-iot-gateway-staging", nil,
		productDPDefinitionLoaderStub{definitions: []ProductDPDefinition{{
			Code: "video_res", Type: "object", Direction: "wo", Status: "active",
		}}})

	_, err := service.SendDPCommands(context.Background(), "livpet-staging", "device-id", "fdr-0001",
		[]CommandInput{{Code: "not_registered", Value: json.RawMessage(`{}`)}}, false)
	if !errors.Is(err, ErrDPUnknown) || !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("SendDPCommands() error = %v, want unknown DP invalid input", err)
	}
	if len(repository.commands) != 0 || len(publisher.payload) != 0 {
		t.Fatalf("unknown DP mutated repository=%#v or published=%s", repository.commands, publisher.payload)
	}
}

func TestSendLocalMediaCommandKeepsTemporaryCredentialsOutOfCommandRepository(t *testing.T) {
	t.Parallel()
	publisher := &publisherStub{}
	repository := &commandRepositoryStub{}
	service := NewService(publisher, ackWaiterStub{ack: map[string]any{
		"action": "local_media", "session_id": "local-media-session-0001", "code": float64(0),
		"state": "ready",
	}}, repository, "aws-iot-gateway-staging", nil)
	value := json.RawMessage(`{"contract_version":"local-media.v1","op":"open","protocol":"onvif","revision":1718000000000,"lease_seconds":300,"expires_at_epoch_sec":1718000300,"credential_generation":"generation_00000001","username":"onvif_user","password":"abcdefghijklmnopqrstuvwxyzABCDEF012345"}`)

	result, err := service.SendLocalMediaCommand(
		context.Background(), "livpet-staging", "fdr-0001", "local-media-session-0001", value,
	)
	if err != nil {
		t.Fatalf("SendLocalMediaCommand() error = %v", err)
	}
	if len(repository.commands) != 0 {
		t.Fatalf("ephemeral local media secret was persisted: %#v", repository.commands)
	}
	var payload map[string]any
	if err := json.Unmarshal(publisher.payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload["action"] != "local_media" || payload["session_id"] != "local-media-session-0001" ||
		payload["password"] != "abcdefghijklmnopqrstuvwxyzABCDEF012345" {
		t.Fatalf("published payload = %#v", payload)
	}
	if result.Status != "success" || result.CommandID != "local-media-session-0001" ||
		result.Data["state"] != "ready" {
		t.Fatalf("result = %#v", result)
	}
}

func TestSendCommandsRejectsLocalMediaBeforePersistenceOrPublish(t *testing.T) {
	t.Parallel()
	publisher := &publisherStub{}
	repository := &commandRepositoryStub{}
	service := NewService(publisher, ackWaiterStub{}, repository,
		"aws-iot-gateway-staging", func() (string, error) { return "must-not-run", nil })

	_, err := service.SendCommands(
		context.Background(), "livpet-staging", "device-id", "fdr-0001",
		[]CommandInput{{Code: "local_media", Value: json.RawMessage(`{"password":"must-not-persist"}`)}}, true,
	)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("SendCommands() error = %v, want ErrInvalidInput", err)
	}
	if len(repository.commands) != 0 || len(publisher.payload) != 0 {
		t.Fatalf("generic local media mutated repository=%#v or published=%s", repository.commands, publisher.payload)
	}
}

func TestSendCommandsRejectsOTAInstallBeforePersistenceOrPublish(t *testing.T) {
	t.Parallel()
	publisher := &publisherStub{}
	repository := &commandRepositoryStub{}
	service := NewService(publisher, ackWaiterStub{}, repository,
		"aws-iot-gateway-staging", func() (string, error) { return "must-not-run", nil })

	_, err := service.SendCommands(
		context.Background(), "livpet-staging", "device-id", "fdr-0001",
		[]CommandInput{{Code: "ota_install", Value: json.RawMessage(`{"download_url":"must-not-publish"}`)}}, true,
	)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("SendCommands() error = %v, want ErrInvalidInput", err)
	}
	if len(repository.commands) != 0 || len(publisher.payload) != 0 {
		t.Fatalf("generic OTA mutated repository=%#v or published=%s", repository.commands, publisher.payload)
	}
}

func TestSendOTACommandPublishesTypedPayloadAndRequiresMatchingAcceptedACK(t *testing.T) {
	t.Parallel()
	publisher := &publisherStub{}
	repository := &commandRepositoryStub{}
	service := NewService(publisher, ackWaiterStub{ack: map[string]any{
		"action": "ota_install", "session_id": "deployment-id", "code": float64(0),
		"deployment_id": "deployment-id", "status": "accepted",
	}}, repository, "aws-iot-gateway-staging", nil)
	command := ota.Command{
		DeploymentID: "deployment-id", DeviceID: "device-id", DeviceSN: "fdr-0001",
		PID: "feeder-x1", Version: "v1.2.3", FirmwareBuild: 42,
		DownloadURL: "https://download.example/ota.tar.gz", SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		FileSize: 1234, ReleaseStatement: "signed-statement", Signature: "c2lnbmF0dXJl",
	}

	if err := service.SendOTACommand(context.Background(), command); err != nil {
		t.Fatalf("SendOTACommand() error = %v", err)
	}
	if len(repository.commands) != 0 {
		t.Fatalf("OTA secrets/URLs were persisted in generic commands: %#v", repository.commands)
	}
	var payload map[string]any
	if err := json.Unmarshal(publisher.payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload["action"] != "ota_install" || payload["schema"] != "explorex-device-ota-v1" ||
		payload["session_id"] != "deployment-id" || payload["deployment_id"] != "deployment-id" ||
		payload["firmware_build"] != float64(42) {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestSendCommandsRejectsReservedLocalMediaSessionBeforePersistenceOrPublish(t *testing.T) {
	t.Parallel()
	publisher := &publisherStub{}
	repository := &commandRepositoryStub{}
	service := NewService(publisher, ackWaiterStub{}, repository,
		"aws-iot-gateway-staging", func() (string, error) { return "must-not-run", nil })

	_, err := service.SendCommands(
		context.Background(), "livpet-staging", "device-id", "fdr-0001",
		[]CommandInput{{
			Code: "feed", CommandID: "lm_0123456789abcdef0123456789abcdef",
			Value: json.RawMessage(`{"portions":1}`),
		}}, false,
	)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("SendCommands() error = %v, want ErrInvalidInput", err)
	}
	if len(repository.commands) != 0 || len(publisher.payload) != 0 {
		t.Fatalf("reserved session mutated repository=%#v or published=%s", repository.commands, publisher.payload)
	}
}

func TestSendCommandsRedactsEphemeralFieldsOnlyFromPersistence(t *testing.T) {
	t.Parallel()
	publisher := &publisherStub{}
	repository := &commandRepositoryStub{}
	service := NewService(publisher, ackWaiterStub{}, repository,
		"aws-iot-gateway-staging", func() (string, error) { return "session-sensitive", nil })

	_, err := service.SendCommands(context.Background(), "livpet-staging", "device-id", "fdr-0001", []CommandInput{{
		Code:         "sd_thumbnail_v1",
		Value:        json.RawMessage(`{"recording_id":"rec_0123456789abcdef0123456789abcdef","upload_url":"https://private.example/signed"}`),
		RedactFields: []string{"upload_url"},
	}}, false)
	if err != nil {
		t.Fatalf("SendCommands() error = %v", err)
	}
	if !bytes.Contains(publisher.payload, []byte("https://private.example/signed")) {
		t.Fatalf("published payload lost upload URL: %s", publisher.payload)
	}
	if len(repository.commands) != 1 {
		t.Fatalf("commands = %#v", repository.commands)
	}
	persisted, err := json.Marshal(repository.commands[0].Payload)
	if err != nil {
		t.Fatalf("marshal persisted payload: %v", err)
	}
	if bytes.Contains(persisted, []byte("upload_url")) || bytes.Contains(persisted, []byte("private.example")) {
		t.Fatalf("persisted command leaked presigned URL: %s", persisted)
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
		"pan_normalized": 0.45, "tilt_normalized": -0.25, "moving": true, "calibrated": true,
	}}, repository, "aws-iot-gateway-staging", func() (string, error) { return "session-ptz", nil })

	status, err := service.GetPTZStatus(context.Background(), "livpet-staging", "device-id", "fdr-0001")
	if err != nil {
		t.Fatalf("GetPTZStatus() error = %v", err)
	}
	if status.Pan != 1800 || status.Tilt != -500 || status.LimitH != 4000 || status.LimitV != 2000 ||
		status.PanNormalized != 0.45 || status.TiltNormalized != -0.25 || !status.Moving || !status.Calibrated {
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
