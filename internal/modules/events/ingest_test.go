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
}

func (sink *ackSinkStub) Store(_ context.Context, deviceSN, sessionID string, ack map[string]any, _ time.Duration) error {
	sink.deviceSN, sink.session, sink.ack = deviceSN, sessionID, ack
	return nil
}

type eventRepositoryStub struct{ event Event }

func (repository *eventRepositoryStub) Store(_ context.Context, event Event) error {
	repository.event = event
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

func TestIngestRejectsCrossEnvironmentAndMalformedIdentity(t *testing.T) {
	t.Parallel()
	service, err := NewIngestService("aws-iot-gateway-staging", &ackSinkStub{}, &eventRepositoryStub{})
	if err != nil {
		t.Fatalf("NewIngestService() error = %v", err)
	}
	for _, payload := range []string{
		`{"topic":"cmd/another-env/fdr-0001/res","session_id":"session-1","code":0}`,
		`{"topic":"dt/aws-iot-gateway-staging/fdr-0001/event","event_id":"not-a-uuid","event_type":"feed_done","boot_id":"boot","event_seq":1,"data":{}}`,
	} {
		if err := service.Handle(context.Background(), []byte(payload)); err == nil {
			t.Fatalf("Handle() accepted %s", payload)
		}
	}
}
