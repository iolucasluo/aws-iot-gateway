package events

import (
	"context"
	"testing"
	"time"
)

type ackSinkStub struct {
	deviceSN string
	session  string
	ack      map[string]any
	calls    int
}

func (sink *ackSinkStub) Store(_ context.Context, deviceSN, sessionID string, ack map[string]any, _ time.Duration) error {
	sink.calls++
	sink.deviceSN, sink.session, sink.ack = deviceSN, sessionID, ack
	return nil
}

type eventRepositoryStub struct{ event Event }

func (repository *eventRepositoryStub) Store(_ context.Context, event Event) error {
	repository.event = event
	return nil
}

func (*eventRepositoryStub) UpdatePresence(context.Context, string, bool, time.Time) error {
	return nil
}

type presenceRepositoryStub struct {
	eventRepositoryStub
	deviceSN   string
	online     bool
	observedAt time.Time
}

func (repository *presenceRepositoryStub) UpdatePresence(_ context.Context, deviceSN string, online bool, observedAt time.Time) error {
	repository.deviceSN = deviceSN
	repository.online = online
	repository.observedAt = observedAt
	return nil
}

func TestIngestRoutesCommandACKByExactEnvironmentTopic(t *testing.T) {
	t.Parallel()
	sink := &ackSinkStub{}
	service, err := NewIngestService("aws-iot-gateway-staging", sink, &eventRepositoryStub{})
	if err != nil {
		t.Fatalf("NewIngestService() error = %v", err)
	}
	if err := service.Handle(context.Background(), []byte(`{
		"topic":"cmd/aws-iot-gateway-staging/fdr-0001/res",
		"session_id":"session-1","action":"feed","code":0
	}`)); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if sink.deviceSN != "fdr-0001" || sink.session != "session-1" || sink.ack["action"] != "feed" {
		t.Fatalf("sink = %#v", sink)
	}
}

func TestIngestRejectsLocalMediaACKBeforeSinkWhenPayloadCouldPersistSecrets(t *testing.T) {
	t.Parallel()

	for _, payload := range []string{
		`{"topic":"cmd/aws-iot-gateway-staging/fdr-0001/res","action":"local_media","session_id":"lm_0123456789abcdef0123456789abcdef","code":0,"password":"must-not-persist"}`,
		`{"topic":"cmd/aws-iot-gateway-staging/fdr-0001/res","action":"local_media","session_id":"lm_0123456789abcdef0123456789abcdef","code":0,"unexpected":{"username":"must-not-persist"}}`,
		`{"topic":"cmd/aws-iot-gateway-staging/fdr-0001/res","action":"feed","session_id":"lm_0123456789abcdef0123456789abcdef","code":0,"password":"must-not-persist"}`,
	} {
		sink := &ackSinkStub{}
		service, err := NewIngestService("aws-iot-gateway-staging", sink, &eventRepositoryStub{})
		if err != nil {
			t.Fatalf("NewIngestService() error = %v", err)
		}
		if err := service.Handle(context.Background(), []byte(payload)); err == nil {
			t.Fatalf("Handle() accepted secret-bearing local-media ACK: %s", payload)
		}
		if sink.calls != 0 || sink.ack != nil {
			t.Fatalf("secret-bearing local-media ACK reached sink: calls=%d ack=%#v", sink.calls, sink.ack)
		}
	}
}

func TestIngestStoresOnlyContractShapedSecretFreeLocalMediaACK(t *testing.T) {
	t.Parallel()

	sink := &ackSinkStub{}
	service, err := NewIngestService("aws-iot-gateway-staging", sink, &eventRepositoryStub{})
	if err != nil {
		t.Fatalf("NewIngestService() error = %v", err)
	}
	payload := []byte(`{
		"topic":"cmd/aws-iot-gateway-staging/fdr-0001/res",
		"action":"local_media","session_id":"lm_0123456789abcdef0123456789abcdef","code":0,
		"contract_version":"local-media.v1","protocol":"onvif","result":"applied",
		"state":"ready","revision":1718000000001,"expires_at_epoch_sec":1718000300,
		"credential_generation":"generation_000000000001",
		"credential_fingerprint":"6817555eaff81377795f73a709a82ae1b4dec9d3655b24cb92a7312776d57b9f",
		"onvif_endpoint":"http://192.168.10.20:8081/onvif/device_service",
		"rtsp_base_uri":"rtsp://192.168.10.20:554"
	}`)
	if err := service.Handle(context.Background(), payload); err != nil {
		t.Fatalf("Handle() valid local-media ACK error = %v", err)
	}
	if sink.calls != 1 || sink.ack["state"] != "ready" {
		t.Fatalf("valid local-media ACK was not stored once: calls=%d ack=%#v", sink.calls, sink.ack)
	}
}

func TestIngestStoresCanonicalDeviceEvent(t *testing.T) {
	t.Parallel()
	repository := &eventRepositoryStub{}
	service, err := NewIngestService("aws-iot-gateway-staging", &ackSinkStub{}, repository)
	if err != nil {
		t.Fatalf("NewIngestService() error = %v", err)
	}
	if err := service.Handle(context.Background(), []byte(`{
		"topic":"dt/aws-iot-gateway-staging/fdr-0001/event",
		"event_id":"018f0000-0000-7000-8000-000000000050",
		"event_type":"feed_done","occurred_at":1718000000000,
		"boot_id":"5ce0b7f2-1f0a-4ed7-9f06-bf6ce8393148","event_seq":7,
		"monotonic_ms":1234,"time_quality":"trusted","time_source":"sntp",
		"data":{"portions":2}
	}`)); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if repository.event.DeviceSN != "fdr-0001" || repository.event.EventType != "feed_done" ||
		repository.event.EventSeq != 7 || repository.event.Data["portions"] != float64(2) || repository.event.OccurredAt == nil {
		t.Fatalf("event = %#v", repository.event)
	}
}

func TestIngestRoutesStatusAndLWTToPresenceRepository(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name           string
		payload        string
		wantOnline     bool
		wantObservedAt int64
	}{
		{
			name: "status heartbeat", wantOnline: true, wantObservedAt: 1786704179000,
			payload: `{"topic":"dt/aws-iot-gateway-staging/fdr-0001/status","online":true,"received_at_ms":1786704179000}`,
		},
		{
			name: "last will", wantOnline: false, wantObservedAt: 1786704239000,
			payload: `{"topic":"dt/aws-iot-gateway-staging/fdr-0001/lwt","online":false,"received_at_ms":1786704239000}`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repository := &presenceRepositoryStub{}
			service, err := NewIngestService("aws-iot-gateway-staging", &ackSinkStub{}, repository)
			if err != nil {
				t.Fatalf("NewIngestService() error = %v", err)
			}
			if err := service.Handle(context.Background(), []byte(testCase.payload)); err != nil {
				t.Fatalf("Handle() error = %v", err)
			}
			if repository.deviceSN != "fdr-0001" || repository.online != testCase.wantOnline ||
				repository.observedAt.UnixMilli() != testCase.wantObservedAt {
				t.Fatalf("presence = %q/%t/%s", repository.deviceSN, repository.online, repository.observedAt)
			}
		})
	}
}

func TestIngestRejectsCrossEnvironmentAndMalformedIdentity(t *testing.T) {
	t.Parallel()
	service, err := NewIngestService("aws-iot-gateway-staging", &ackSinkStub{}, &eventRepositoryStub{})
	if err != nil {
		t.Fatalf("NewIngestService() error = %v", err)
	}
	for _, payload := range []string{
		`{"topic":"cmd/another-env/fdr-0001/res","session_id":"session-1","code":0}`,
		`{"topic":"dt/aws-iot-gateway-staging/fdr-0001/event","event_id":"not-a-uuid","event_type":"feed_done","boot_id":"boot","event_seq":1,"data":{}}`,
		`{"topic":"dt/aws-iot-gateway-staging/fdr-0001/status","online":false,"received_at_ms":1786704179000}`,
		`{"topic":"dt/aws-iot-gateway-staging/fdr-0001/lwt","online":false}`,
	} {
		if err := service.Handle(context.Background(), []byte(payload)); err == nil {
			t.Fatalf("Handle() accepted %s", payload)
		}
	}
}

func TestIngestHandlesDirectMQTTCommandACK(t *testing.T) {
	t.Parallel()

	sink := &ackSinkStub{}
	service, err := NewIngestService("aws-iot-gateway-staging", sink, &eventRepositoryStub{})
	if err != nil {
		t.Fatalf("NewIngestService() error = %v", err)
	}
	payload := []byte(`{"topic":"cmd/another-env/attacker/res","session_id":"session-direct","action":"device_info","code":0}`)
	if err := service.HandleMQTT(context.Background(),
		"cmd/aws-iot-gateway-staging/fdr-direct/res", payload); err != nil {
		t.Fatalf("HandleMQTT() error = %v", err)
	}
	if sink.deviceSN != "fdr-direct" || sink.session != "session-direct" || sink.ack["action"] != "device_info" {
		t.Fatalf("sink = %#v", sink)
	}
	if _, exists := sink.ack["topic"]; exists {
		t.Fatalf("ACK retained transport topic: %#v", sink.ack)
	}
}

func TestIngestUsesGatewayReceiptTimeForDirectMQTTPresence(t *testing.T) {
	t.Parallel()

	repository := &presenceRepositoryStub{}
	service, err := NewIngestService("aws-iot-gateway-staging", &ackSinkStub{}, repository)
	if err != nil {
		t.Fatalf("NewIngestService() error = %v", err)
	}
	beforeMS := time.Now().UTC().UnixMilli()
	if err := service.HandleMQTT(context.Background(),
		"dt/aws-iot-gateway-staging/fdr-direct/status", []byte(`{"online":true,"received_at_ms":1}`)); err != nil {
		t.Fatalf("HandleMQTT() error = %v", err)
	}
	afterMS := time.Now().UTC().UnixMilli()
	if repository.deviceSN != "fdr-direct" || !repository.online ||
		repository.observedAt.UnixMilli() < beforeMS || repository.observedAt.UnixMilli() > afterMS {
		t.Fatalf("presence = %q/%t/%s, want gateway receipt time in [%d,%d]ms",
			repository.deviceSN, repository.online, repository.observedAt, beforeMS, afterMS)
	}
}
