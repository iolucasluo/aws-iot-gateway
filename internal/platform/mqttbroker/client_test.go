package mqttbroker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewClientRequiresMQTTSAndMutualTLSMaterial(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	caPath := filepath.Join(directory, "ca.pem")
	certPath := filepath.Join(directory, "client.pem")
	keyPath := filepath.Join(directory, "client.key")
	for _, path := range []string{caPath, certPath, keyPath} {
		if err := os.WriteFile(path, []byte("not-a-certificate"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	base := Config{
		Endpoint:       "mqtts://127.0.0.1:8883",
		ClientID:       "gateway-cn-staging",
		TopicPrefix:    "cn-staging",
		CAFile:         caPath,
		ClientCertFile: certPath,
		ClientKeyFile:  keyPath,
	}
	if _, err := NewClient(base); err == nil || !strings.Contains(err.Error(), "TLS") {
		t.Fatalf("NewClient() error = %v, want TLS material failure", err)
	}

	base.Endpoint = "mqtt://127.0.0.1:1883"
	if _, err := NewClient(base); err == nil || !strings.Contains(err.Error(), "mqtts") {
		t.Fatalf("NewClient() error = %v, want mqtts-only failure", err)
	}
}

func TestNewClientRejectsUnsafeNamespaceAndIdentity(t *testing.T) {
	t.Parallel()

	cases := map[string]Config{
		"empty endpoint":      {ClientID: "gateway-cn-staging", TopicPrefix: "cn-staging"},
		"unsafe client id":    {Endpoint: "mqtts://127.0.0.1:8883", ClientID: "gateway/../../aws", TopicPrefix: "cn-staging"},
		"unsafe topic prefix": {Endpoint: "mqtts://127.0.0.1:8883", ClientID: "gateway-cn-staging", TopicPrefix: "cn staging"},
	}
	for name, config := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewClient(config); err == nil {
				t.Fatal("NewClient() error = nil, want validation failure")
			}
		})
	}
}

func TestTopicNamespaceIsFailClosed(t *testing.T) {
	t.Parallel()

	for _, topic := range []string{
		"cmd/cn-staging/device/req",
		"dt/cn-staging/device/status",
		"cmd/cn-staging/+/res",
		"dt/cn-staging/#",
	} {
		if !topicInNamespace(topic, "cn-staging") {
			t.Fatalf("topicInNamespace(%q) = false", topic)
		}
	}
	for _, topic := range []string{
		"cmd/cn-staging-prod/device/req",
		"cmd/cn-staging/device/../other",
		"cmd/aws-iot-gateway-staging/device/req",
	} {
		if topicInNamespace(topic, "cn-staging") {
			t.Fatalf("topicInNamespace(%q) = true", topic)
		}
	}
}
