package worker

import (
	"context"
	"errors"
	"fmt"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/provider"
)

type IngestHandler interface {
	Handle(context.Context, []byte) error
}

type IngestWorker struct {
	queue   provider.EventQueue
	handler IngestHandler
}

func NewIngestWorker(queue provider.EventQueue, handler IngestHandler) (*IngestWorker, error) {
	if queue == nil || handler == nil {
		return nil, fmt.Errorf("ingest worker requires an event queue and handler")
	}
	return &IngestWorker{queue: queue, handler: handler}, nil
}

func (worker *IngestWorker) PollOnce(ctx context.Context) error {
	messages, err := worker.queue.Receive(ctx)
	if err != nil {
		return fmt.Errorf("receive ingest messages: %w", err)
	}
	var failures []error
	for _, message := range messages {
		if len(message.Body) == 0 || message.LeaseToken == "" {
			failures = append(failures, fmt.Errorf("ingest message is incomplete"))
			continue
		}
		if err := worker.handler.Handle(ctx, message.Body); err != nil {
			failures = append(failures, fmt.Errorf("handle IoT ingest message: %w", err))
			continue
		}
		if err := worker.queue.Ack(ctx, message); err != nil {
			failures = append(failures, fmt.Errorf("acknowledge ingest message: %w", err))
		}
	}
	return errors.Join(failures...)
}
