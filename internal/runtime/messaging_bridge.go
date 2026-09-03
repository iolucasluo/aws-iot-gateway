package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/provider"
)

const defaultDeviceMessageTTL = 30 * time.Second

var deviceMessageTopicPrefixPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// MessageHandler is the narrow runtime-to-domain boundary for device messages.
// The runtime owns provider and transport details; domain modules own payload
// validation and persistence.
type MessageHandler func(context.Context, string, []byte) error

// DevicePublisher adapts the provider-neutral messaging port to the existing
// device module Publisher interface. It supplies the transport TTL required by
// providers without exposing provider SDKs to the device module.
type DevicePublisher struct {
	client provider.DeviceMessaging
	now    func() time.Time
}

var _ interface {
	Publish(context.Context, string, []byte) error
} = (*DevicePublisher)(nil)

func NewDevicePublisher(client provider.DeviceMessaging, now func() time.Time) (*DevicePublisher, error) {
	if client == nil {
		return nil, errors.New("device messaging provider is required")
	}
	if now == nil {
		now = time.Now
	}
	return &DevicePublisher{client: client, now: now}, nil
}

// Publish creates a short-lived provider message and copies the payload so a
// caller cannot mutate bytes after handing them to the provider.
func (publisher *DevicePublisher) Publish(ctx context.Context, topic string, payload []byte) error {
	if publisher == nil || publisher.client == nil || ctx == nil || strings.TrimSpace(topic) == "" {
		return errors.New("invalid device publish request")
	}
	message := provider.DeviceMessage{
		Topic:     topic,
		Payload:   append([]byte(nil), payload...),
		ExpiresAt: publisher.now().UTC().Add(defaultDeviceMessageTTL),
	}
	tracePublish(message.Topic, message.Payload)
	if err := publisher.client.Publish(ctx, message); err != nil {
		return fmt.Errorf("publish device message: %w", err)
	}
	return nil
}

func tracePublish(topic string, payload []byte) {
	if os.Getenv("EXPLOREX_MQTT_TRACE") != "1" {
		return
	}
	var envelope struct {
		Action    string `json:"action"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		log.Printf("mqttbridge: publish topic=%s bytes=%d decode_error=%v", topic, len(payload), err)
		return
	}
	log.Printf("mqttbridge: publish topic=%s action=%s session=%s bytes=%d", topic, envelope.Action, envelope.SessionID, len(payload))
}

// SubscriptionSet owns all device message subscriptions created by the
// bridge. Close is safe to call repeatedly and concurrently.
type SubscriptionSet struct {
	handles   []provider.SubscriptionHandle
	closeOnce sync.Once
	closeErr  error
}

func (subscriptions *SubscriptionSet) Close() error {
	if subscriptions == nil {
		return nil
	}
	subscriptions.closeOnce.Do(func() {
		for index := len(subscriptions.handles) - 1; index >= 0; index-- {
			handle := subscriptions.handles[index]
			if handle == nil {
				continue
			}
			if err := handle.Close(); err != nil && subscriptions.closeErr == nil {
				subscriptions.closeErr = err
			}
		}
	})
	return subscriptions.closeErr
}

// SubscribeDeviceMessages wires the canonical device response and telemetry
// topics to one generic handler. Provider adapters remain responsible for
// transport authentication and their own namespace checks.
func SubscribeDeviceMessages(
	ctx context.Context,
	client provider.DeviceMessaging,
	topicPrefix string,
	handler MessageHandler,
) (*SubscriptionSet, error) {
	if ctx == nil || client == nil || handler == nil || !deviceMessageTopicPrefixPattern.MatchString(topicPrefix) {
		return nil, errors.New("invalid device message subscription")
	}
	filters := []string{
		"cmd/" + topicPrefix + "/+/res",
		"dt/" + topicPrefix + "/+/status",
		"dt/" + topicPrefix + "/+/lwt",
		"dt/" + topicPrefix + "/+/event",
	}
	handles := make([]provider.SubscriptionHandle, 0, len(filters))
	for _, filter := range filters {
		subscription := provider.DeviceSubscription{
			TopicFilter: filter,
			Handler: func(messageCtx context.Context, message provider.DeviceMessage) error {
				if messageCtx == nil {
					messageCtx = ctx
				}
				return handler(messageCtx, message.Topic, append([]byte(nil), message.Payload...))
			},
		}
		handle, err := client.Subscribe(ctx, subscription)
		if err != nil {
			closeSubscriptionHandles(handles)
			return nil, fmt.Errorf("subscribe device message filter %q: %w", filter, err)
		}
		if handle == nil {
			closeSubscriptionHandles(handles)
			return nil, fmt.Errorf("subscribe device message filter %q: provider returned nil handle", filter)
		}
		handles = append(handles, handle)
	}
	return &SubscriptionSet{handles: handles}, nil
}

func closeSubscriptionHandles(handles []provider.SubscriptionHandle) {
	for index := len(handles) - 1; index >= 0; index-- {
		if handles[index] != nil {
			_ = handles[index].Close()
		}
	}
}

// NewDevicePublisher exposes the bridge for the selected messaging runtime.
func (runtime *MessagingRuntime) NewDevicePublisher(now func() time.Time) (*DevicePublisher, error) {
	if runtime == nil || runtime.Client == nil {
		return nil, errors.New("messaging runtime is not configured")
	}
	return NewDevicePublisher(runtime.Client, now)
}

// SubscribeDeviceMessages exposes canonical device message subscriptions for
// the selected messaging runtime.
func (runtime *MessagingRuntime) SubscribeDeviceMessages(ctx context.Context, handler MessageHandler) (*SubscriptionSet, error) {
	if runtime == nil || runtime.Client == nil || runtime.TopicPrefix == "" {
		return nil, errors.New("messaging runtime is not configured")
	}
	return SubscribeDeviceMessages(ctx, runtime.Client, runtime.TopicPrefix, handler)
}
