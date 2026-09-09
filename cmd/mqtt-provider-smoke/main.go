package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	gatewayruntime "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/runtime"
)

const (
	mqttClientIDEnv       = "GATEWAY_MQTT_CLIENT_ID"
	mqttTopicPrefixEnv    = "GATEWAY_MQTT_TOPIC_PREFIX"
	mqttBridgeWaitModeEnv = "GATEWAY_MQTT_SMOKE_WAIT_FOR_DEVICE_MESSAGE"
)

type bridgeSubscription interface {
	Close() error
}

type bridgeMessage struct {
	topic        string
	payloadBytes int
}

func main() {
	os.Exit(run(os.Getenv, os.Stdout, os.Stderr))
}

func run(getenv func(string) string, stdout, stderr io.Writer) int {
	composition, err := gatewayruntime.LoadProviderComposition(getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "MQTT provider smoke: FAIL: %v\n", err)
		return 1
	}
	messaging, err := gatewayruntime.BuildMessaging(composition, getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "MQTT provider smoke: FAIL: %v\n", err)
		return 1
	}
	defer messaging.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := messaging.Health(ctx); err != nil {
		_, _ = fmt.Fprintf(stderr, "MQTT provider smoke: FAIL: %v\n", err)
		return 1
	}
	if bridgeWaitEnabled(getenv) {
		message, err := waitForBridgeMessage(ctx, func(handler gatewayruntime.MessageHandler) (bridgeSubscription, error) {
			return messaging.SubscribeDeviceMessages(ctx, handler)
		})
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "MQTT provider smoke: FAIL: %v\n", err)
			return 1
		}
		_, _ = fmt.Fprintf(stdout, "MQTT provider bridge: OK topic=%s payload_bytes=%d\n", message.topic, message.payloadBytes)
	}
	parsed, _ := url.Parse(messaging.Endpoint)
	_, _ = fmt.Fprintf(stdout, "MQTT provider smoke: OK provider_instance=%s host=%s client_id=%s topic_prefix=%s\n",
		messaging.InstanceID, parsed.Host, strings.TrimSpace(getenv(mqttClientIDEnv)), messaging.TopicPrefix)
	return 0
}

func bridgeWaitEnabled(getenv func(string) string) bool {
	return getenv != nil && strings.TrimSpace(getenv(mqttBridgeWaitModeEnv)) == "1"
}

func waitForBridgeMessage(
	ctx context.Context,
	subscribe func(gatewayruntime.MessageHandler) (bridgeSubscription, error),
) (bridgeMessage, error) {
	if ctx == nil || subscribe == nil {
		return bridgeMessage{}, errors.New("bridge wait is not configured")
	}
	messages := make(chan bridgeMessage, 1)
	subscription, err := subscribe(func(_ context.Context, topic string, payload []byte) error {
		select {
		case messages <- bridgeMessage{topic: topic, payloadBytes: len(payload)}:
		default:
		}
		return nil
	})
	if err != nil {
		return bridgeMessage{}, fmt.Errorf("subscribe device bridge: %w", err)
	}
	if subscription == nil {
		return bridgeMessage{}, errors.New("subscribe device bridge: provider returned nil handle")
	}
	var message bridgeMessage
	var waitErr error
	select {
	case message = <-messages:
	case <-ctx.Done():
		waitErr = ctx.Err()
	}
	closeErr := subscription.Close()
	if waitErr != nil {
		return bridgeMessage{}, waitErr
	}
	if closeErr != nil {
		return bridgeMessage{}, fmt.Errorf("close device bridge: %w", closeErr)
	}
	return message, nil
}
