package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

type SQSClient interface {
	ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(context.Context, *sqs.DeleteMessageInput, ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
}

type IngestHandler interface {
	Handle(context.Context, []byte) error
}

type IngestWorker struct {
	client   SQSClient
	queueURL string
	handler  IngestHandler
}

func NewIngestWorker(client SQSClient, queueURL string, handler IngestHandler) (*IngestWorker, error) {
	if client == nil || strings.TrimSpace(queueURL) == "" || handler == nil {
		return nil, fmt.Errorf("ingest worker requires SQS, queue URL, and handler")
	}
	return &IngestWorker{client: client, queueURL: queueURL, handler: handler}, nil
}

func (worker *IngestWorker) PollOnce(ctx context.Context) error {
	result, err := worker.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl: aws.String(worker.queueURL), MaxNumberOfMessages: 10, WaitTimeSeconds: 20,
	})
	if err != nil {
		return fmt.Errorf("receive IoT ingest messages: %w", err)
	}
	var failures []error
	for _, message := range result.Messages {
		body := aws.ToString(message.Body)
		receipt := aws.ToString(message.ReceiptHandle)
		if body == "" || receipt == "" {
			failures = append(failures, fmt.Errorf("SQS ingest message is incomplete"))
			continue
		}
		if err := worker.handler.Handle(ctx, []byte(body)); err != nil {
			failures = append(failures, fmt.Errorf("handle IoT ingest message: %w", err))
			continue
		}
		if _, err := worker.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
			QueueUrl: aws.String(worker.queueURL), ReceiptHandle: aws.String(receipt),
		}); err != nil {
			failures = append(failures, fmt.Errorf("delete IoT ingest message: %w", err))
		}
	}
	return errors.Join(failures...)
}
