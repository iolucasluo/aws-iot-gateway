package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const domesticCompositionFixture = `api_version: explorex.io/v1
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

func TestLoadProviderCompositionRequiresExplicitPath(t *testing.T) {
	t.Parallel()

	_, err := LoadProviderComposition(func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "GATEWAY_PROVIDER_COMPOSITION_PATH") {
		t.Fatalf("LoadProviderComposition() error = %v, want explicit path failure", err)
	}
}

func TestLoadProviderCompositionChecksEnvironmentAndRejectsAWSDomesticDataPlane(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, "composition.yaml")
	if err := os.WriteFile(path, []byte(domesticCompositionFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"GATEWAY_ENVIRONMENT":               "staging",
		"GATEWAY_PROVIDER_COMPOSITION_PATH": path,
	}
	composition, err := LoadProviderComposition(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("LoadProviderComposition() error = %v", err)
	}
	if composition.Metadata.ID != "cn-staging" || composition.Metadata.Environment != "staging" || composition.Metadata.Region != "cn" {
		t.Fatalf("composition metadata = %#v", composition.Metadata)
	}

	env["GATEWAY_ENVIRONMENT"] = "development"
	if _, err := LoadProviderComposition(func(key string) string { return env[key] }); err == nil {
		t.Fatal("LoadProviderComposition() accepted environment mismatch")
	}

	awsDomestic := strings.Replace(domesticCompositionFixture, "driver: mqtt_broker", "driver: aws_iot_mqtt", 1)
	if err := os.WriteFile(path, []byte(awsDomestic), 0o600); err != nil {
		t.Fatal(err)
	}
	env["GATEWAY_ENVIRONMENT"] = "staging"
	if _, err := LoadProviderComposition(func(key string) string { return env[key] }); err == nil {
		t.Fatal("LoadProviderComposition() accepted AWS domestic composition")
	}
}

func TestLoadProviderCompositionRejectsSymlinkAndOversizedFile(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	validPath := filepath.Join(directory, "composition.yaml")
	if err := os.WriteFile(validPath, []byte(domesticCompositionFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(directory, "composition-link.yaml")
	if err := os.Symlink(validPath, symlinkPath); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProviderCompositionFile(symlinkPath); err == nil {
		t.Fatal("LoadProviderCompositionFile() accepted symlink")
	}

	oversizedPath := filepath.Join(directory, "composition-large.yaml")
	if err := os.WriteFile(oversizedPath, make([]byte, maxProviderCompositionBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProviderCompositionFile(oversizedPath); err == nil {
		t.Fatal("LoadProviderCompositionFile() accepted oversized composition")
	}
}
