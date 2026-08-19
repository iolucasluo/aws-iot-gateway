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
		"INSERT INTO products (id, pid, name, status)",
		"'feeder-x1'",
		"'minion-cam-x1'",
		"ON CONFLICT (pid) DO NOTHING",
	} {
		if !strings.Contains(seed, fragment) {
			t.Errorf("product seed missing %q", fragment)
		}
	}
}
