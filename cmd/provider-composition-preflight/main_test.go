package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunPrintsOnlyNonSecretCompositionFacts(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, "composition.yaml")
	contents := `api_version: explorex.io/v1
kind: ProviderComposition
metadata:
  id: cn-staging
  environment: staging
  region: cn
instances:
  identity.cn_staging:
    capability: identity
    driver: ca_mqtt_identity
    credential_ref: secret://cn-staging/private-secret
  messaging.cn_staging:
    capability: messaging
    driver: mqtt_broker
    endpoint: mqtt://127.0.0.1:51883
  signer.cn_staging:
    capability: signer
    driver: local
bindings:
  identity: identity.cn_staging
  messaging: messaging.cn_staging
  signer: signer.cn_staging
policy:
  allow_mixed_provider: true
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"GATEWAY_ENVIRONMENT":               "staging",
		"GATEWAY_PROVIDER_COMPOSITION_PATH": path,
	}
	var stdout, stderr bytes.Buffer
	if code := run(func(key string) string { return env[key] }, &stdout, &stderr); code != 0 {
		t.Fatalf("run() code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "id=cn-staging environment=staging region=cn") ||
		!strings.Contains(stdout.String(), "instance=messaging.cn_staging capability=messaging driver=mqtt_broker") {
		t.Fatalf("stdout=%q", stdout.String())
	}
	if strings.Contains(stdout.String(), "private-secret") || strings.Contains(stdout.String(), "secret://") {
		t.Fatalf("stdout leaked credential reference: %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestRunFailsClosedWhenCompositionIsMissing(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	if code := run(func(string) string { return "" }, &stdout, &stderr); code != 1 {
		t.Fatalf("run() code=%d, want 1", code)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "GATEWAY_PROVIDER_COMPOSITION_PATH") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}
