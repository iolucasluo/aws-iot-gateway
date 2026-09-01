package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/mqttbroker"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/provider"
	gatewayruntime "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/runtime"
)

const (
	mqttClientIDEnv       = "GATEWAY_MQTT_CLIENT_ID"
	mqttTopicPrefixEnv    = "GATEWAY_MQTT_TOPIC_PREFIX"
	mqttCAFileEnv         = "GATEWAY_MQTT_CA_FILE"
	mqttClientCertFileEnv = "GATEWAY_MQTT_CLIENT_CERT_FILE"
	mqttClientKeyFileEnv  = "GATEWAY_MQTT_CLIENT_KEY_FILE"
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
	instanceID, instance, err := messagingInstance(composition)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "MQTT provider smoke: FAIL: %v\n", err)
		return 1
	}
	config, err := mqttConfig(instance.Endpoint, getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "MQTT provider smoke: FAIL: %v\n", err)
		return 1
	}
	client, err := mqttbroker.NewClient(config)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "MQTT provider smoke: FAIL: %v\n", err)
		return 1
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		_, _ = fmt.Fprintf(stderr, "MQTT provider smoke: FAIL: %v\n", err)
		return 1
	}
	parsed, _ := url.Parse(config.Endpoint)
	_, _ = fmt.Fprintf(stdout, "MQTT provider smoke: OK provider_instance=%s host=%s client_id=%s topic_prefix=%s\n",
		instanceID, parsed.Host, config.ClientID, config.TopicPrefix)
	return 0
}

func messagingInstance(composition provider.Composition) (string, provider.Instance, error) {
	instanceID := strings.TrimSpace(composition.Bindings["messaging"])
	if instanceID == "" {
		return "", provider.Instance{}, fmt.Errorf("messaging binding is required")
	}
	instance, ok := composition.Instances[instanceID]
	if !ok || instance.Capability != provider.CapabilityMessaging {
		return "", provider.Instance{}, fmt.Errorf("messaging binding %q is invalid", instanceID)
	}
	if instance.Driver != "mqtt_broker" {
		return "", provider.Instance{}, fmt.Errorf("messaging binding %q must use mqtt_broker, got %q", instanceID, instance.Driver)
	}
	return instanceID, instance, nil
}

func mqttConfig(endpoint string, getenv func(string) string) (mqttbroker.Config, error) {
	if strings.TrimSpace(getenv(mqttClientIDEnv)) == "" || strings.TrimSpace(getenv(mqttTopicPrefixEnv)) == "" {
		return mqttbroker.Config{}, fmt.Errorf("%s and %s are required", mqttClientIDEnv, mqttTopicPrefixEnv)
	}
	return mqttbroker.Config{
		Endpoint:       endpoint,
		ClientID:       strings.TrimSpace(getenv(mqttClientIDEnv)),
		TopicPrefix:    strings.TrimSpace(getenv(mqttTopicPrefixEnv)),
		CAFile:         strings.TrimSpace(getenv(mqttCAFileEnv)),
		ClientCertFile: strings.TrimSpace(getenv(mqttClientCertFileEnv)),
		ClientKeyFile:  strings.TrimSpace(getenv(mqttClientKeyFileEnv)),
	}, nil
}
