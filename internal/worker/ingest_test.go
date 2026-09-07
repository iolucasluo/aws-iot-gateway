package worker

import (
	"context"
	"testing"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/provider"
)

type queueStub struct {
	messages []provider.QueueMessage
	acked    []string
}

func (client *queueStub) Receive(context.Context) ([]provider.QueueMessage, error) {
	return client.messages, nil
}

func (client *queueStub) Ack(_ context.Context, message provider.QueueMessage) error {
	client.acked = append(client.acked, message.LeaseToken)
	return nil
}

func (client *queueStub) Health(context.Context) error {
	return nil
}

type ingestHandlerStub struct {
	failBody string
	handled  []string
}

func (handler *ingestHandlerStub) Handle(_ context.Context, body []byte) error {
	handler.handled = append(handler.handled, string(body))
	if string(body) == handler.failBody {
		return context.DeadlineExceeded
	}
	return nil
}

func TestIngestWorkerDeletesOnlySuccessfullyPersistedMessages(t *testing.T) {
	t.Parallel()
	client := &queueStub{messages: []provider.QueueMessage{
		{Body: []byte("good"), LeaseToken: "receipt-good"},
		{Body: []byte("bad"), LeaseToken: "receipt-bad"},
	}}
	handler := &ingestHandlerStub{failBody: "bad"}
	worker, err := NewIngestWorker(client, handler)
	if err != nil {
		t.Fatalf("NewIngestWorker() error = %v", err)
	}
	if err := worker.PollOnce(context.Background()); err == nil {
		t.Fatal("PollOnce() error = nil, want partial failure")
	}
	if len(client.acked) != 1 || client.acked[0] != "receipt-good" {
		t.Fatalf("acked = %#v", client.acked)
	}
}
