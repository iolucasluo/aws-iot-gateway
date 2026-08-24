package media

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/device"
)

type localMediaCommanderStub struct {
	sessionID string
	value     json.RawMessage
	ack       map[string]any
	err       error
}

func (commander *localMediaCommanderStub) SendLocalMediaCommand(
	_ context.Context, _, _, sessionID string, value json.RawMessage,
) (device.CommandResult, error) {
	commander.sessionID = sessionID
	commander.value = append(json.RawMessage(nil), value...)
	if commander.err != nil {
		return device.CommandResult{}, commander.err
	}
	ack := make(map[string]any, len(commander.ack))
	for key, entry := range commander.ack {
		ack[key] = entry
	}
	ack["session_id"] = sessionID
	return device.CommandResult{
		Code: "local_media", Status: "success", CommandID: sessionID, Data: ack,
	}, nil
}

type localMediaRevisionStub struct{ next int64 }

func (store *localMediaRevisionStub) Next(_ context.Context, _ string, _ int64) (int64, error) {
	store.next++
	return store.next, nil
}

type localMediaProductPolicyStub struct{ supported bool }

func (policy localMediaProductPolicyStub) ProductSupports(_ context.Context, _, capability string) (bool, error) {
	return policy.supported && capability == LocalMediaContractVersion, nil
}

func fixedLocalMediaCredentials() (LocalMediaCredentials, error) {
	return LocalMediaCredentials{
		SessionID:  "local_media_session_000001",
		Generation: "generation_000000000001",
		Username:   "onvif_user",
		Password:   "abcdefghijklmnopqrstuvwxyzABCDEF012345",
	}, nil
}

func successfulLocalMediaACK(revision int64, state string) map[string]any {
	ack := map[string]any{
		"action": "local_media", "session_id": "local_media_session_000001", "code": float64(0),
		"contract_version": LocalMediaContractVersion, "protocol": "onvif",
		"result": "applied", "state": state, "revision": float64(revision),
	}
	if state == "ready" {
		ack["expires_at_epoch_sec"] = float64(1_718_000_300)
		ack["credential_generation"] = "generation_000000000001"
		ack["credential_fingerprint"] = "6817555eaff81377795f73a709a82ae1b4dec9d3655b24cb92a7312776d57b9f"
		ack["onvif_endpoint"] = "http://192.168.10.20:8081/onvif/device_service"
		ack["rtsp_base_uri"] = "rtsp://192.168.10.20:554"
	}
	return ack
}

func TestLocalMediaCreateUsesTypedEphemeralCommandAndReturnsDirectLANSession(t *testing.T) {
	t.Parallel()
	commander := &localMediaCommanderStub{ack: successfulLocalMediaACK(1_718_000_000_001, "ready")}
	revisions := &localMediaRevisionStub{next: 1_718_000_000_000}
	service := NewLocalMediaService(
		commander, revisions, localMediaProductPolicyStub{supported: true},
		func() time.Time { return time.Unix(1_718_000_000, 0).UTC() }, fixedLocalMediaCredentials,
	)

	result, err := service.Create(context.Background(), "livpet-staging", "feeder-x1", "fdr-0001", 300)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	var command map[string]any
	if err := json.Unmarshal(commander.value, &command); err != nil {
		t.Fatalf("decode local media command: %v", err)
	}
	if commander.sessionID != "local_media_session_000001" || command["op"] != "open" ||
		command["password"] != "abcdefghijklmnopqrstuvwxyzABCDEF012345" ||
		command["revision"] != float64(1_718_000_000_001) {
		t.Fatalf("command = %#v, session = %q", command, commander.sessionID)
	}
	if result.ContractVersion != LocalMediaContractVersion || result.Protocol != "onvif" ||
		result.SessionID != "local_media_session_000001" || result.State != "ready" ||
		result.Credentials.Username != "onvif_user" ||
		result.Credentials.Password != "abcdefghijklmnopqrstuvwxyzABCDEF012345" ||
		result.ONVIFEndpoint != "http://192.168.10.20:8081/onvif/device_service" ||
		result.RTSPBaseURI != "rtsp://192.168.10.20:554" {
		t.Fatalf("result = %#v", result)
	}
}

func TestLocalMediaCreateFailsClosedForUnsupportedProductOrUntrustedLocator(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name      string
		supported bool
		ack       map[string]any
		want      error
	}{
		{name: "unsupported product", supported: false, ack: successfulLocalMediaACK(1_718_000_000_001, "ready"), want: ErrLocalMediaUnsupported},
		{name: "public locator", supported: true, ack: func() map[string]any {
			ack := successfulLocalMediaACK(1_718_000_000_001, "ready")
			ack["onvif_endpoint"] = "http://203.0.113.20:8081/onvif/device_service"
			ack["rtsp_base_uri"] = "rtsp://203.0.113.20:554"
			return ack
		}(), want: ErrInvalidLocalMediaResponse},
		{name: "credential fingerprint mismatch", supported: true, ack: func() map[string]any {
			ack := successfulLocalMediaACK(1_718_000_000_001, "ready")
			ack["credential_fingerprint"] = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
			return ack
		}(), want: ErrInvalidLocalMediaResponse},
		{name: "private IPv6 locator outside board contract", supported: true, ack: func() map[string]any {
			ack := successfulLocalMediaACK(1_718_000_000_001, "ready")
			ack["onvif_endpoint"] = "http://[fd00::20]:8081/onvif/device_service"
			ack["rtsp_base_uri"] = "rtsp://[fd00::20]:554"
			return ack
		}(), want: ErrInvalidLocalMediaResponse},
		{name: "ACK reflects forbidden credential material", supported: true, ack: func() map[string]any {
			ack := successfulLocalMediaACK(1_718_000_000_001, "ready")
			ack["password"] = "must-not-be-reflected"
			return ack
		}(), want: ErrInvalidLocalMediaResponse},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			commander := &localMediaCommanderStub{ack: testCase.ack}
			service := NewLocalMediaService(
				commander, &localMediaRevisionStub{next: 1_718_000_000_000},
				localMediaProductPolicyStub{supported: testCase.supported},
				func() time.Time { return time.Unix(1_718_000_000, 0).UTC() }, fixedLocalMediaCredentials,
			)
			_, err := service.Create(context.Background(), "livpet-staging", "feeder-x1", "fdr-0001", 300)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("Create() error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestLocalMediaCloseTargetsTheCredentialGeneration(t *testing.T) {
	t.Parallel()
	commander := &localMediaCommanderStub{ack: successfulLocalMediaACK(1_718_000_000_001, "disabled")}
	service := NewLocalMediaService(
		commander, &localMediaRevisionStub{next: 1_718_000_000_000},
		localMediaProductPolicyStub{supported: true},
		func() time.Time { return time.Unix(1_718_000_000, 0).UTC() }, nil,
	)

	result, err := service.Close(
		context.Background(), "livpet-staging", "feeder-x1", "fdr-0001", "generation_000000000001",
	)
	if err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	var command map[string]any
	if err := json.Unmarshal(commander.value, &command); err != nil {
		t.Fatalf("decode close command: %v", err)
	}
	if command["op"] != "close" || command["target_credential_generation"] != "generation_000000000001" ||
		command["revision"] != float64(1_718_000_000_001) || result.State != "disabled" {
		t.Fatalf("close command = %#v, result = %#v", command, result)
	}
}

func TestLocalMediaCloseRejectsACKWithUnexpectedFields(t *testing.T) {
	t.Parallel()
	ack := successfulLocalMediaACK(1_718_000_000_001, "disabled")
	ack["password"] = "must-not-be-reflected"
	service := NewLocalMediaService(
		&localMediaCommanderStub{ack: ack},
		&localMediaRevisionStub{next: 1_718_000_000_000},
		localMediaProductPolicyStub{supported: true},
		func() time.Time { return time.Unix(1_718_000_000, 0).UTC() }, nil,
	)

	_, err := service.Close(
		context.Background(), "livpet-staging", "feeder-x1", "fdr-0001",
		"generation_000000000001",
	)
	if !errors.Is(err, ErrInvalidLocalMediaResponse) {
		t.Fatalf("Close() error = %v, want ErrInvalidLocalMediaResponse", err)
	}
}

func TestLocalMediaAcceptsExactDuplicateACKForMQTTQoS1Redelivery(t *testing.T) {
	t.Parallel()

	openACK := successfulLocalMediaACK(1_718_000_000_001, "ready")
	openACK["result"] = "duplicate"
	openService := NewLocalMediaService(
		&localMediaCommanderStub{ack: openACK},
		&localMediaRevisionStub{next: 1_718_000_000_000},
		localMediaProductPolicyStub{supported: true},
		func() time.Time { return time.Unix(1_718_000_000, 0).UTC() }, fixedLocalMediaCredentials,
	)
	if _, err := openService.Create(
		context.Background(), "livpet-staging", "feeder-x1", "fdr-0001", 300,
	); err != nil {
		t.Fatalf("Create() duplicate ACK error = %v", err)
	}

	closeACK := successfulLocalMediaACK(1_718_000_000_001, "disabled")
	closeACK["result"] = "duplicate"
	closeService := NewLocalMediaService(
		&localMediaCommanderStub{ack: closeACK},
		&localMediaRevisionStub{next: 1_718_000_000_000},
		localMediaProductPolicyStub{supported: true},
		func() time.Time { return time.Unix(1_718_000_000, 0).UTC() }, nil,
	)
	if _, err := closeService.Close(
		context.Background(), "livpet-staging", "feeder-x1", "fdr-0001",
		"generation_000000000001",
	); err != nil {
		t.Fatalf("Close() duplicate ACK error = %v", err)
	}
}
