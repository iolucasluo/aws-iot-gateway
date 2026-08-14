package worker

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

type sqsStub struct {
	messages []types.Message
	deleted  []string
}

func (client *sqsStub) ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	return &sqs.ReceiveMessageOutput{Messages: client.messages}, nil
}

func (client *sqsStub) DeleteMessage(_ context.Context, input *sqs.DeleteMessageInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	client.deleted = append(client.deleted, aws.ToString(input.ReceiptHandle))
	return &sqs.DeleteMessageOutput{}, nil
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
	client := &sqsStub{messages: []types.Message{
		{Body: aws.String("good"), ReceiptHandle: aws.String("receipt-good")},
		{Body: aws.String("bad"), ReceiptHandle: aws.String("receipt-bad")},
	}}
	handler := &ingestHandlerStub{failBody: "bad"}
	worker, err := NewIngestWorker(client, "https://sqs.example/queue", handler)
	if err != nil {
		t.Fatalf("NewIngestWorker() error = %v", err)
	}
	if err := worker.PollOnce(context.Background()); err == nil {
		t.Fatal("PollOnce() error = nil, want partial failure")
	}
	if len(client.deleted) != 1 || client.deleted[0] != "receipt-good" {
		t.Fatalf("deleted = %#v", client.deleted)
	}
}
