package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunReportsDomesticRuntimeBlockersWithoutSecrets(t *testing.T) {
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
    credential_ref: secret://private-identity
  messaging.cn_staging:
    capability: messaging
    driver: mqtt_broker
    endpoint: mqtts://127.0.0.1:51884
  signer.cn_staging:
    capability: signer
    driver: domestic_kms
    key_ref: secret://private-key
  realtime.cn_staging:
    capability: realtime
    driver: explorex_signal
    endpoint: http://127.0.0.1:18080
bindings:
  identity: identity.cn_staging
  messaging: messaging.cn_staging
  signer: signer.cn_staging
  realtime: realtime.cn_staging
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
	if code := run(func(key string) string { return env[key] }, &stdout, &stderr); code != 2 {
		t.Fatalf("run() code=%d, want 2 for a valid but blocked domestic plan; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	output := stdout.String()
	for _, want := range []string{
		"provider composition runtime audit: BLOCKED",
		"binding=identity instance=identity.cn_staging capability=identity driver=ca_mqtt_identity status=BLOCKED",
		"binding=messaging instance=messaging.cn_staging capability=messaging driver=mqtt_broker status=SUPPORTED",
		"binding=signer instance=signer.cn_staging capability=signer driver=domestic_kms status=BLOCKED",
		"binding=realtime instance=realtime.cn_staging capability=realtime driver=explorex_signal status=BLOCKED reason=provider adapter exists; application runtime wiring is pending",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("stdout=%q, missing %q", output, want)
		}
	}
	if strings.Contains(output, "private-identity") || strings.Contains(output, "private-key") || strings.Contains(output, "secret://") {
		t.Fatalf("stdout leaked a credential reference: %q", output)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestRunFailsClosedWhenCompositionCannotBeLoaded(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	if code := run(func(string) string { return "" }, &stdout, &stderr); code != 1 {
		t.Fatalf("run() code=%d, want 1", code)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "GATEWAY_PROVIDER_COMPOSITION_PATH") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}
