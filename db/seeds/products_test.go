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
		"'minion-cam-x1'",
		`'["webrtc.v1", "local-media.v1", "ptz.v1"]'::jsonb`,
		`'["webrtc.v1"]'::jsonb`,
		"ON CONFLICT (pid) DO UPDATE",
		"capabilities = EXCLUDED.capabilities",
	} {
		if !strings.Contains(seed, fragment) {
			t.Errorf("product seed missing %q", fragment)
		}
	}
}
