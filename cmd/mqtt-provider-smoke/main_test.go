package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRequiresMessagingCompositionAndTLSPaths(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	compositionPath := filepath.Join(directory, "composition.yaml")
	composition := `api_version: explorex.io/v1
kind: ProviderComposition
metadata:
  id: cn-staging
  environment: staging
  region: cn
instances:
  identity.cn_staging:
    capability: identity
    driver: ca_mqtt_identity
  messaging.cn_staging:
    capability: messaging
    driver: mqtt_broker
    endpoint: mqtts://127.0.0.1:51884
  signer.cn_staging:
    capability: signer
    driver: domestic_kms
bindings:
  identity: identity.cn_staging
  messaging: messaging.cn_staging
  signer: signer.cn_staging
policy:
  allow_mixed_provider: true
`
	if err := os.WriteFile(compositionPath, []byte(composition), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"GATEWAY_ENVIRONMENT":               "staging",
		"GATEWAY_PROVIDER_COMPOSITION_PATH": compositionPath,
		"GATEWAY_MQTT_CLIENT_ID":            "gateway-cn-staging",
		"GATEWAY_MQTT_TOPIC_PREFIX":         "cn-staging",
	}
	var stdout, stderr bytes.Buffer
	if code := run(func(key string) string { return env[key] }, &stdout, &stderr); code != 1 {
		t.Fatalf("run() code=%d, want missing TLS path failure; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "CA") && !strings.Contains(stderr.String(), "Client") {
		t.Fatalf("stderr=%q, want TLS configuration error", stderr.String())
	}
}

func TestRunRejectsNonMQTTMessagingDriver(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	compositionPath := filepath.Join(directory, "composition.yaml")
	composition := `api_version: explorex.io/v1
kind: ProviderComposition
metadata:
  id: cn-staging
  environment: staging
  region: cn
instances:
  identity.cn_staging:
    capability: identity
    driver: ca_mqtt_identity
  messaging.cn_staging:
    capability: messaging
    driver: aws_iot_mqtt
  signer.cn_staging:
    capability: signer
    driver: domestic_kms
bindings:
  identity: identity.cn_staging
  messaging: messaging.cn_staging
  signer: signer.cn_staging
policy:
  allow_mixed_provider: true
`
	if err := os.WriteFile(compositionPath, []byte(composition), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"GATEWAY_ENVIRONMENT":               "staging",
		"GATEWAY_PROVIDER_COMPOSITION_PATH": compositionPath,
	}
	var stdout, stderr bytes.Buffer
	if code := run(func(key string) string { return env[key] }, &stdout, &stderr); code != 1 {
		t.Fatalf("run() code=%d, want driver rejection; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(strings.ToLower(stderr.String()), "aws") {
		t.Fatalf("stderr=%q, want domestic AWS data-plane rejection", stderr.String())
	}
}
