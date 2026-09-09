package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gatewayruntime "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/runtime"
)

type bridgeSmokeCloser struct {
	calls int
}

func (closer *bridgeSmokeCloser) Close() error {
	closer.calls++
	return nil
}

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

func TestBridgeWaitModeIsExplicitOptIn(t *testing.T) {
	t.Parallel()

	for value, want := range map[string]bool{"": false, "true": false, "0": false, "1": true, " 1 ": true} {
		if got := bridgeWaitEnabled(func(string) string { return value }); got != want {
			t.Fatalf("bridgeWaitEnabled(%q) = %t, want %t", value, got, want)
		}
	}
}

func TestWaitForBridgeMessageReturnsMetadataAndClosesSubscription(t *testing.T) {
	t.Parallel()

	closer := &bridgeSmokeCloser{}
	message, err := waitForBridgeMessage(context.Background(), func(handler gatewayruntime.MessageHandler) (bridgeSubscription, error) {
		if err := handler(context.Background(), "cmd/cn-staging/fdr-0001/res", []byte(`{"code":0}`)); err != nil {
			t.Fatalf("handler() error = %v", err)
		}
		return closer, nil
	})
	if err != nil {
		t.Fatalf("waitForBridgeMessage() error = %v", err)
	}
	if message.topic != "cmd/cn-staging/fdr-0001/res" || message.payloadBytes != len(`{"code":0}`) {
		t.Fatalf("message = %#v", message)
	}
	if closer.calls != 1 {
		t.Fatalf("subscription close calls = %d, want 1", closer.calls)
	}
}

func TestWaitForBridgeMessageReturnsSubscribeError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("subscribe failed")
	_, err := waitForBridgeMessage(context.Background(), func(gatewayruntime.MessageHandler) (bridgeSubscription, error) {
		return nil, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("waitForBridgeMessage() error = %v, want %v", err, wantErr)
	}
}

func TestWaitForBridgeMessageTimesOutAndClosesSubscription(t *testing.T) {
	t.Parallel()

	closer := &bridgeSmokeCloser{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := waitForBridgeMessage(ctx, func(gatewayruntime.MessageHandler) (bridgeSubscription, error) {
		return closer, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waitForBridgeMessage() error = %v, want deadline exceeded", err)
	}
	if closer.calls != 1 {
		t.Fatalf("subscription close calls = %d, want 1", closer.calls)
	}
}
