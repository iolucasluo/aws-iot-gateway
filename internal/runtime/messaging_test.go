package runtime

import (
	"strings"
	"sync"
	"testing"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/provider"
)

func TestBuildMessagingRejectsUnsupportedDriverBeforeTLSConfiguration(t *testing.T) {
	t.Parallel()

	composition := provider.Composition{
		Bindings: map[string]string{"messaging": "messaging.overseas"},
		Instances: map[string]provider.Instance{
			"messaging.overseas": {
				Capability: provider.CapabilityMessaging,
				Driver:     "aws_iot_mqtt",
				Endpoint:   "mqtts://iot.example.invalid:8883",
			},
		},
	}
	_, err := BuildMessaging(composition, func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "unsupported messaging driver") {
		t.Fatalf("BuildMessaging() error = %v, want unsupported driver failure", err)
	}
}

func TestBuildMessagingRequiresOperatorOwnedMQTTTLSMaterial(t *testing.T) {
	t.Parallel()

	composition := provider.Composition{
		Bindings: map[string]string{"messaging": "messaging.cn_staging"},
		Instances: map[string]provider.Instance{
			"messaging.cn_staging": {
				Capability: provider.CapabilityMessaging,
				Driver:     "mqtt_broker",
				Endpoint:   "mqtts://127.0.0.1:51884",
			},
		},
	}
	env := map[string]string{
		"GATEWAY_MQTT_CLIENT_ID":    "gateway-cn-staging",
		"GATEWAY_MQTT_TOPIC_PREFIX": "cn-staging",
	}
	_, err := BuildMessaging(composition, func(key string) string { return env[key] })
	if err == nil || !strings.Contains(err.Error(), "CAFile") {
		t.Fatalf("BuildMessaging() error = %v, want missing CAFile failure", err)
	}
}

func TestBuildMessagingRequiresMessagingBinding(t *testing.T) {
	t.Parallel()

	_, err := BuildMessaging(provider.Composition{}, func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "messaging binding") {
		t.Fatalf("BuildMessaging() error = %v, want missing binding failure", err)
	}
}

func TestMessagingRuntimeCloseIsIdempotent(t *testing.T) {
	t.Parallel()

	var calls int
	runtime := &MessagingRuntime{close: func() { calls++ }}
	var wait sync.WaitGroup
	for i := 0; i < 4; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			runtime.Close()
		}()
	}
	wait.Wait()
	if calls != 1 {
		t.Fatalf("close calls = %d, want 1", calls)
	}
}
