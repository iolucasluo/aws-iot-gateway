package seeds

import (
	"strings"
	"testing"
)

func TestProductSeedDefinesGatewayActivationCatalog(t *testing.T) {
	t.Parallel()

	content, err := Files.ReadFile("001_products.sql")
	if err != nil {
		t.Fatalf("read product seed: %v", err)
	}
	seed := string(content)
	for _, fragment := range []string{
		"INSERT INTO products (id, pid, name, status, capabilities)",
		"'feeder-x1'",
		"'lamp-x1'",
		"'minion-cam-x1'",
		`'["webrtc.v1", "local-media.v1", "ptz.v1", "ota.app.v1"]'::jsonb`,
		`'["webrtc.v1", "ota.app.v1"]'::jsonb`,
		"ON CONFLICT (pid) DO UPDATE",
		"capabilities = EXCLUDED.capabilities",
	} {
		if !strings.Contains(seed, fragment) {
			t.Errorf("product seed missing %q", fragment)
		}
	}
}

func TestProductSeedEnablesMinionCameraAppOTA(t *testing.T) {
	t.Parallel()

	content, err := Files.ReadFile("001_products.sql")
	if err != nil {
		t.Fatalf("read product seed: %v", err)
	}
	seed := string(content)
	const expected = "'minion-cam-x1', 'Minion Cam X1', 'active',\n        '[\"webrtc.v1\", \"ota.app.v1\"]'::jsonb)"
	if !strings.Contains(seed, expected) {
		t.Fatalf("minion product seed missing ota.app.v1 capability")
	}
}
