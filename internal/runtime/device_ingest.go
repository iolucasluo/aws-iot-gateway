package runtime

import (
	"context"
	"errors"
	"fmt"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/events"
)

// StartDeviceIngest connects the selected messaging provider and wires the
// canonical device ACK/status/LWT/event topics to the existing domain ingest
// service. It is shared by domestic compositions and does not know about any
// provider SDK or transport-specific client.
func StartDeviceIngest(
	ctx context.Context,
	messaging *MessagingRuntime,
	ackSink events.ACKSink,
	repository events.IngestRepository,
) (*SubscriptionSet, error) {
	if ctx == nil || messaging == nil || messaging.Client == nil {
		return nil, errors.New("device ingest messaging runtime is not configured")
	}
	if err := messaging.Health(ctx); err != nil {
		return nil, fmt.Errorf("connect device ingest messaging: %w", err)
	}
	ingestService, err := events.NewIngestService(messaging.TopicPrefix, ackSink, repository)
	if err != nil {
		return nil, fmt.Errorf("configure device ingest: %w", err)
	}
	subscriptions, err := messaging.SubscribeDeviceMessages(ctx, ingestService.HandleMQTT)
	if err != nil {
		return nil, fmt.Errorf("subscribe device ingest: %w", err)
	}
	return subscriptions, nil
}
