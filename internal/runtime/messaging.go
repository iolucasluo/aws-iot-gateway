package runtime

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/mqttbroker"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/provider"
)

const (
	mqttClientIDEnv       = "GATEWAY_MQTT_CLIENT_ID"
	mqttTopicPrefixEnv    = "GATEWAY_MQTT_TOPIC_PREFIX"
	mqttCAFileEnv         = "GATEWAY_MQTT_CA_FILE"
	mqttClientCertFileEnv = "GATEWAY_MQTT_CLIENT_CERT_FILE"
	mqttClientKeyFileEnv  = "GATEWAY_MQTT_CLIENT_KEY_FILE"
)

// MessagingRuntime is the selected capability instance and its provider-neutral
// client. The adapter is created but not connected by BuildMessaging so callers
// can finish all local composition checks before doing network I/O.
type MessagingRuntime struct {
	InstanceID  string
	TopicPrefix string
	Endpoint    string
	Client      provider.DeviceMessaging
	close       func()
	closeOnce   sync.Once
}

// BuildMessaging selects exactly the composition's messaging binding and
// constructs its statically registered adapter. Unsupported drivers fail before
// any network connection or fallback to another provider.
func BuildMessaging(composition provider.Composition, getenv func(string) string) (*MessagingRuntime, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	instanceID := strings.TrimSpace(composition.Bindings["messaging"])
	if instanceID == "" {
		return nil, fmt.Errorf("messaging binding is required")
	}
	instance, ok := composition.Instances[instanceID]
	if !ok || instance.Capability != provider.CapabilityMessaging {
		return nil, fmt.Errorf("messaging binding %q is invalid", instanceID)
	}
	switch instance.Driver {
	case "mqtt_broker":
		config, err := mqttConfig(instance.Endpoint, getenv)
		if err != nil {
			return nil, fmt.Errorf("configure messaging %q: %w", instanceID, err)
		}
		client, err := mqttbroker.NewClient(config)
		if err != nil {
			return nil, fmt.Errorf("configure messaging %q: %w", instanceID, err)
		}
		return &MessagingRuntime{
			InstanceID: instanceID, TopicPrefix: config.TopicPrefix, Endpoint: config.Endpoint,
			Client: client, close: client.Close,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported messaging driver %q", instance.Driver)
	}
}

func mqttConfig(endpoint string, getenv func(string) string) (mqttbroker.Config, error) {
	clientID := strings.TrimSpace(getenv(mqttClientIDEnv))
	topicPrefix := strings.TrimSpace(getenv(mqttTopicPrefixEnv))
	if clientID == "" || topicPrefix == "" {
		return mqttbroker.Config{}, fmt.Errorf("%s and %s are required", mqttClientIDEnv, mqttTopicPrefixEnv)
	}
	return mqttbroker.Config{
		Endpoint:       endpoint,
		ClientID:       clientID,
		TopicPrefix:    topicPrefix,
		CAFile:         strings.TrimSpace(getenv(mqttCAFileEnv)),
		ClientCertFile: strings.TrimSpace(getenv(mqttClientCertFileEnv)),
		ClientKeyFile:  strings.TrimSpace(getenv(mqttClientKeyFileEnv)),
	}, nil
}

// Health delegates readiness to the selected provider without exposing its
// SDK or transport details to the caller.
func (runtime *MessagingRuntime) Health(ctx context.Context) error {
	if runtime == nil || runtime.Client == nil {
		return fmt.Errorf("messaging runtime is not configured")
	}
	return runtime.Client.Health(ctx)
}

// Close releases the selected adapter. It is safe to call on a nil or already
// closed runtime.
func (runtime *MessagingRuntime) Close() {
	if runtime == nil {
		return
	}
	runtime.closeOnce.Do(func() {
		if runtime.close != nil {
			runtime.close()
		}
	})
}
