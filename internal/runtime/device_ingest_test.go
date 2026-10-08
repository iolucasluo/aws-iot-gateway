package runtime

import (
	"context"
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/events"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/provider"
)

type deviceIngestMessagingStub struct {
	healthCalls    int
	subscriptions  []provider.DeviceSubscription
	handlesCreated int
}

func (stub *deviceIngestMessagingStub) Publish(context.Context, provider.DeviceMessage) error {
	return nil
}

func (stub *deviceIngestMessagingStub) Subscribe(_ context.Context, subscription provider.DeviceSubscription) (provider.SubscriptionHandle, error) {
	stub.subscriptions = append(stub.subscriptions, subscription)
	stub.handlesCreated++
	return deviceIngestHandleStub{}, nil
}

func (stub *deviceIngestMessagingStub) Health(context.Context) error {
	stub.healthCalls++
	return nil
}

type deviceIngestHandleStub struct{}

func (deviceIngestHandleStub) Close() error { return nil }

type deviceIngestACKStub struct {
	deviceSN string
	session  string
	ack      map[string]any
}

func (stub *deviceIngestACKStub) Store(_ context.Context, deviceSN, sessionID string, ack map[string]any, _ time.Duration) error {
	stub.deviceSN, stub.session, stub.ack = deviceSN, sessionID, ack
	return nil
}

type deviceIngestRepositoryStub struct {
	event      events.Event
	deviceSN   string
	online     bool
	observedAt time.Time
}

func (stub *deviceIngestRepositoryStub) Store(_ context.Context, event events.Event) error {
	stub.event = event
	return nil
}

func (stub *deviceIngestRepositoryStub) UpdatePresence(_ context.Context, deviceSN string, online bool, observedAt time.Time) error {
	stub.deviceSN, stub.online, stub.observedAt = deviceSN, online, observedAt
	return nil
}

func TestStartDeviceIngestConnectsAndRoutesCanonicalMessages(t *testing.T) {
	client := &deviceIngestMessagingStub{}
	messaging := &MessagingRuntime{Client: client, TopicPrefix: "cn-staging"}
	ackSink := &deviceIngestACKStub{}
	repository := &deviceIngestRepositoryStub{}

	subscriptions, err := StartDeviceIngest(context.Background(), messaging, ackSink, repository)
	if err != nil {
		t.Fatalf("StartDeviceIngest() error = %v", err)
	}
	if subscriptions == nil || client.healthCalls != 1 || len(client.subscriptions) != 4 {
		t.Fatalf("runtime setup = subscriptions:%v health:%d filters:%d", subscriptions != nil, client.healthCalls, len(client.subscriptions))
	}

	for _, subscription := range client.subscriptions {
		switch subscription.TopicFilter {
		case "dt/cn-staging/+/status":
			if err := subscription.Handler(context.Background(), provider.DeviceMessage{
				Topic: "dt/cn-staging/fdr-0001/status", Payload: []byte(`{"online":true}`),
			}); err != nil {
				t.Fatalf("status handler error = %v", err)
			}
		case "cmd/cn-staging/+/res":
			if err := subscription.Handler(context.Background(), provider.DeviceMessage{
				Topic: "cmd/cn-staging/fdr-0001/res", Payload: []byte(`{"session_id":"session-1","action":"feed","code":0}`),
			}); err != nil {
				t.Fatalf("ACK handler error = %v", err)
			}
		}
	}
	if repository.deviceSN != "fdr-0001" || !repository.online || repository.observedAt.IsZero() {
		t.Fatalf("presence = %q/%t/%s", repository.deviceSN, repository.online, repository.observedAt)
	}
	if ackSink.deviceSN != "fdr-0001" || ackSink.session != "session-1" || ackSink.ack["action"] != "feed" {
		t.Fatalf("ACK = %#v", ackSink)
	}
}

func TestStartDeviceIngestRejectsUnconfiguredRuntime(t *testing.T) {
	if _, err := StartDeviceIngest(context.Background(), nil, &deviceIngestACKStub{}, &deviceIngestRepositoryStub{}); err == nil {
		t.Fatal("StartDeviceIngest(nil) succeeded")
	}
}

type devicePetPhotoWorkflowStub struct{ ready int }

func (stub *devicePetPhotoWorkflowStub) CaptureReady(context.Context, events.Event) error {
	stub.ready++
	return nil
}
func (stub *devicePetPhotoWorkflowStub) VerifyFinal(context.Context, events.Event) error { return nil }

func TestDomesticDeviceIngestRoutesInternalPetPhotoReadyPrivately(t *testing.T) {
	client := &deviceIngestMessagingStub{}
	messaging := &MessagingRuntime{Client: client, TopicPrefix: "cn-staging"}
	repository := &deviceIngestRepositoryStub{}
	workflow := &devicePetPhotoWorkflowStub{}
	subscriptions, err := StartDeviceIngest(context.Background(), messaging, &deviceIngestACKStub{}, repository, workflow)
	if err != nil {
		t.Fatal(err)
	}
	defer subscriptions.Close()
	for _, subscription := range client.subscriptions {
		if subscription.TopicFilter != "dt/cn-staging/+/event" {
			continue
		}
		err = subscription.Handler(context.Background(), provider.DeviceMessage{
			Topic:   "dt/cn-staging/fdr-0001/event",
			Payload: []byte(`{"event_id":"018f0000-0000-7000-8000-000000000011","event_type":"pet_photo_ready_v1","boot_id":"5ce0b7f2-1f0a-4ed7-9f06-bf6ce8393148","event_seq":11,"data":{"trigger_id":"018f0000-0000-7000-8000-000000000012","model_id":"0x0A00002C","size_bytes":100,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if workflow.ready != 1 || repository.event.ID != "" {
		t.Fatalf("private ready leaked: workflow=%d event=%#v", workflow.ready, repository.event)
	}
}
