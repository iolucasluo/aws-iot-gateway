package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	gatewayruntime "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/runtime"
)

const (
	mqttClientIDEnv    = "GATEWAY_MQTT_CLIENT_ID"
	mqttTopicPrefixEnv = "GATEWAY_MQTT_TOPIC_PREFIX"
)

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
	parsed, _ := url.Parse(messaging.Endpoint)
	_, _ = fmt.Fprintf(stdout, "MQTT provider smoke: OK provider_instance=%s host=%s client_id=%s topic_prefix=%s\n",
		messaging.InstanceID, parsed.Host, strings.TrimSpace(getenv(mqttClientIDEnv)), messaging.TopicPrefix)
	return 0
}
