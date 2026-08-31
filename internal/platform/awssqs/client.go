package awssqs

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/provider"
)

type API interface {
	ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(context.Context, *sqs.DeleteMessageInput, ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
}

type Client struct {
	client   API
	queueURL string
}

var _ provider.EventQueue = (*Client)(nil)

func New(client API, queueURL string) (*Client, error) {
	if client == nil || strings.TrimSpace(queueURL) == "" {
		return nil, fmt.Errorf("AWS SQS adapter requires client and queue URL")
	}
	return &Client{client: client, queueURL: queueURL}, nil
}

func (client *Client) Receive(ctx context.Context) ([]provider.QueueMessage, error) {
	if client == nil || client.client == nil || client.queueURL == "" {
		return nil, fmt.Errorf("AWS SQS adapter is not configured")
	}
	result, err := client.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl: aws.String(client.queueURL), MaxNumberOfMessages: 10, WaitTimeSeconds: 20,
	})
	if err != nil {
		return nil, fmt.Errorf("receive AWS SQS messages: %w", err)
	}
	messages := make([]provider.QueueMessage, 0, len(result.Messages))
	for _, message := range result.Messages {
		messages = append(messages, provider.QueueMessage{
			ID: aws.ToString(message.MessageId), Body: []byte(aws.ToString(message.Body)),
			LeaseToken: aws.ToString(message.ReceiptHandle),
		})
	}
	return messages, nil
}

func (client *Client) Ack(ctx context.Context, message provider.QueueMessage) error {
	if client == nil || client.client == nil || client.queueURL == "" || message.LeaseToken == "" {
		return fmt.Errorf("AWS SQS acknowledge requires a lease token")
	}
	if _, err := client.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl: aws.String(client.queueURL), ReceiptHandle: aws.String(message.LeaseToken),
	}); err != nil {
		return fmt.Errorf("acknowledge AWS SQS message: %w", err)
	}
	return nil
}

func (client *Client) Health(context.Context) error {
	if client == nil || client.client == nil || client.queueURL == "" {
		return fmt.Errorf("AWS SQS adapter is not configured")
	}
	return nil
}
