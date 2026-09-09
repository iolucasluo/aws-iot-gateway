package awssqs

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/provider"
)

type sqsAPIStub struct {
	deleted string
}

func (stub *sqsAPIStub) ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	return &sqs.ReceiveMessageOutput{Messages: []types.Message{{
		MessageId: aws.String("message-1"), Body: aws.String("payload"), ReceiptHandle: aws.String("lease-1"),
	}}}, nil
}

func (stub *sqsAPIStub) DeleteMessage(_ context.Context, input *sqs.DeleteMessageInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	stub.deleted = aws.ToString(input.ReceiptHandle)
	return &sqs.DeleteMessageOutput{}, nil
}

func TestClientMapsSQSLeaseToProviderNeutralQueueMessage(t *testing.T) {
	stub := &sqsAPIStub{}
	client, err := New(stub, "https://sqs.example/queue")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	messages, err := client.Receive(context.Background())
	if err != nil {
		t.Fatalf("Receive() error = %v", err)
	}
	if len(messages) != 1 || messages[0].ID != "message-1" || string(messages[0].Body) != "payload" || messages[0].LeaseToken != "lease-1" {
		t.Fatalf("messages = %#v", messages)
	}
	if err := client.Ack(context.Background(), provider.QueueMessage{LeaseToken: messages[0].LeaseToken}); err != nil {
		t.Fatalf("Ack() error = %v", err)
	}
	if stub.deleted != "lease-1" {
		t.Fatalf("deleted lease = %q", stub.deleted)
	}
}

func TestClientRejectsMissingLeaseToken(t *testing.T) {
	client, err := New(&sqsAPIStub{}, "https://sqs.example/queue")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := client.Ack(context.Background(), provider.QueueMessage{}); err == nil {
		t.Fatal("Ack() accepted missing lease token")
	}
}
