package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/provider"
)

type messagingBridgeStub struct {
	mu             sync.Mutex
	published      []provider.DeviceMessage
	subscriptions  []provider.DeviceSubscription
	handles        []*messagingBridgeHandleStub
	subscribeErrAt int
	subscribeErr   error
}

func (stub *messagingBridgeStub) Publish(_ context.Context, message provider.DeviceMessage) error {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.published = append(stub.published, message)
	return nil
}

func (stub *messagingBridgeStub) Subscribe(_ context.Context, subscription provider.DeviceSubscription) (provider.SubscriptionHandle, error) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.subscribeErr != nil && len(stub.subscriptions) == stub.subscribeErrAt {
		return nil, stub.subscribeErr
	}
	stub.subscriptions = append(stub.subscriptions, subscription)
	handle := &messagingBridgeHandleStub{}
	stub.handles = append(stub.handles, handle)
	return handle, nil
}

func (stub *messagingBridgeStub) Health(context.Context) error { return nil }

type messagingBridgeHandleStub struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (handle *messagingBridgeHandleStub) Close() error {
	handle.mu.Lock()
	defer handle.mu.Unlock()
	handle.calls++
	return handle.err
}

func (handle *messagingBridgeHandleStub) closeCalls() int {
	handle.mu.Lock()
	defer handle.mu.Unlock()
	return handle.calls
}

func TestDevicePublisherAddsShortTTLAndCopiesPayload(t *testing.T) {
	t.Parallel()

	stub := &messagingBridgeStub{}
	now := func() time.Time { return time.Date(2026, 9, 1, 8, 0, 0, 0, time.FixedZone("CST", 8*60*60)) }
	publisher, err := NewDevicePublisher(stub, now)
	if err != nil {
		t.Fatalf("NewDevicePublisher() error = %v", err)
	}
	payload := []byte(`{"action":"feed"}`)
	if err := publisher.Publish(context.Background(), "cmd/cn-staging/fdr-0001/req", payload); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	payload[0] = 'X'
	if len(stub.published) != 1 {
		t.Fatalf("published messages = %d, want 1", len(stub.published))
	}
	message := stub.published[0]
	if string(message.Payload) != `{"action":"feed"}` {
		t.Fatalf("published payload = %q, want copied original", message.Payload)
	}
	wantExpiry := time.Date(2026, 9, 1, 0, 0, 30, 0, time.UTC)
	if !message.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("expires_at = %s, want %s", message.ExpiresAt, wantExpiry)
	}
}

func TestDevicePublisherRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	if _, err := NewDevicePublisher(nil, nil); err == nil {
		t.Fatal("NewDevicePublisher(nil) succeeded")
	}
	stub := &messagingBridgeStub{}
	publisher, err := NewDevicePublisher(stub, nil)
	if err != nil {
		t.Fatalf("NewDevicePublisher() error = %v", err)
	}
	for name, test := range map[string]struct {
		ctx   context.Context
		topic string
	}{
		"nil context": {ctx: nil, topic: "cmd/cn-staging/fdr-0001/req"},
		"blank topic": {ctx: context.Background(), topic: "  "},
	} {
		t.Run(name, func(t *testing.T) {
			if err := publisher.Publish(test.ctx, test.topic, []byte("{}")); err == nil {
				t.Fatalf("Publish(%q) succeeded", name)
			}
		})
	}
}

func TestSubscribeDeviceMessagesRegistersCanonicalFiltersAndForwardsCopies(t *testing.T) {
	t.Parallel()

	stub := &messagingBridgeStub{}
	var gotCtx context.Context
	var gotTopic string
	var gotPayload []byte
	handler := func(ctx context.Context, topic string, payload []byte) error {
		gotCtx, gotTopic, gotPayload = ctx, topic, payload
		return nil
	}
	subscriptions, err := SubscribeDeviceMessages(context.Background(), stub, "cn-staging", handler)
	if err != nil {
		t.Fatalf("SubscribeDeviceMessages() error = %v", err)
	}
	wantFilters := []string{
		"cmd/cn-staging/+/res",
		"dt/cn-staging/+/status",
		"dt/cn-staging/+/lwt",
		"dt/cn-staging/+/event",
	}
	if len(stub.subscriptions) != len(wantFilters) {
		t.Fatalf("subscription count = %d, want %d", len(stub.subscriptions), len(wantFilters))
	}
	for index, wantFilter := range wantFilters {
		if stub.subscriptions[index].TopicFilter != wantFilter {
			t.Fatalf("subscription[%d] filter = %q, want %q", index, stub.subscriptions[index].TopicFilter, wantFilter)
		}
	}
	callbackContext := context.WithValue(context.Background(), "test", "message")
	payload := []byte(`{"code":0}`)
	if err := stub.subscriptions[0].Handler(callbackContext, provider.DeviceMessage{
		Topic: "cmd/cn-staging/fdr-0001/res", Payload: payload,
	}); err != nil {
		t.Fatalf("subscription handler error = %v", err)
	}
	payload[0] = 'X'
	if gotCtx != callbackContext || gotTopic != "cmd/cn-staging/fdr-0001/res" || string(gotPayload) != `{"code":0}` {
		t.Fatalf("forwarded message = ctx:%v topic:%q payload:%q", gotCtx, gotTopic, gotPayload)
	}
	if subscriptions == nil {
		t.Fatal("SubscribeDeviceMessages() returned nil subscription set")
	}
}

func TestSubscribeDeviceMessagesRejectsInvalidTopicPrefixBeforeProvider(t *testing.T) {
	t.Parallel()

	stub := &messagingBridgeStub{}
	for _, topicPrefix := range []string{"", "cn/staging", "../cn", strings.Repeat("a", 129)} {
		if _, err := SubscribeDeviceMessages(context.Background(), stub, topicPrefix, func(context.Context, string, []byte) error { return nil }); err == nil {
			t.Fatalf("SubscribeDeviceMessages(%q) succeeded", topicPrefix)
		}
	}
	if len(stub.subscriptions) != 0 {
		t.Fatalf("provider subscriptions = %d, want 0", len(stub.subscriptions))
	}
}

func TestSubscribeDeviceMessagesClosesCreatedHandlesOnFailure(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("subscribe failed")
	stub := &messagingBridgeStub{subscribeErrAt: 2, subscribeErr: wantErr}
	_, err := SubscribeDeviceMessages(context.Background(), stub, "cn-staging", func(context.Context, string, []byte) error { return nil })
	if !errors.Is(err, wantErr) {
		t.Fatalf("SubscribeDeviceMessages() error = %v, want %v", err, wantErr)
	}
	if len(stub.handles) != 2 {
		t.Fatalf("created handles = %d, want 2", len(stub.handles))
	}
	for index, handle := range stub.handles {
		if calls := handle.closeCalls(); calls != 1 {
			t.Fatalf("handle[%d] close calls = %d, want 1", index, calls)
		}
	}
}

func TestSubscriptionSetCloseIsIdempotentAndConcurrent(t *testing.T) {
	t.Parallel()

	stub := &messagingBridgeStub{}
	subscriptions, err := SubscribeDeviceMessages(context.Background(), stub, "cn-staging", func(context.Context, string, []byte) error { return nil })
	if err != nil {
		t.Fatalf("SubscribeDeviceMessages() error = %v", err)
	}
	var wait sync.WaitGroup
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_ = subscriptions.Close()
		}()
	}
	wait.Wait()
	for index, handle := range stub.handles {
		if calls := handle.closeCalls(); calls != 1 {
			t.Fatalf("handle[%d] close calls = %d, want 1", index, calls)
		}
	}
}
