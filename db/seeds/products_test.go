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
		`'["webrtc.v1"]'::jsonb`,
		"ON CONFLICT (pid) DO UPDATE",
		"capabilities = EXCLUDED.capabilities",
	} {
		if !strings.Contains(seed, fragment) {
			t.Errorf("product seed missing %q", fragment)
		}
	}
}

func TestProductDPSeedDefinesMinionCameraContract(t *testing.T) {
	t.Parallel()

	content, err := Files.ReadFile("002_product_dps.sql")
	if err != nil {
		t.Fatalf("read product DP seed: %v", err)
	}
	seed := string(content)
	for _, fragment := range []string{
		"INSERT INTO product_dps (id, product_id, code, type, direction, spec, status)",
		"'minion-cam-x1'",
		"'video_res'",
		"'privacy_mode'",
		"ON CONFLICT (product_id, code) DO UPDATE",
	} {
		if !strings.Contains(seed, fragment) {
			t.Errorf("product DP seed missing %q", fragment)
		}
	}
}

func TestProductDPSeedDefinesMachineCheckableDomains(t *testing.T) {
	t.Parallel()

	content, err := Files.ReadFile("002_product_dps.sql")
	if err != nil {
		t.Fatalf("read product DP seed: %v", err)
	}
	seed := string(content)
	for _, fragment := range []string{
		`"minimum":0,"maximum":100`,
		`"enum":["runtime","all"]`,
		`"enum":["start","stop"]`,
		`"ack":{"files":"finalized recording metadata objects"`,
		`"report_interval_semantics":"accepted for cross-repo compatibility but not applied by current firmware action"`,
	} {
		if !strings.Contains(seed, fragment) {
			t.Errorf("product DP seed missing machine-checkable domain %q", fragment)
		}
	}
}

func TestProductDPSeedDefinesConditionalAndNestedSchemas(t *testing.T) {
	t.Parallel()

	content, err := Files.ReadFile("002_product_dps.sql")
	if err != nil {
		t.Fatalf("read product DP seed: %v", err)
	}
	seed := string(content)
	for _, fragment := range []string{
		`"variants":{"field":"op","cases":{"move"`,
		`"variants":{"field":"mode","cases":{"0"`,
		`"variants":{"field":"start","cases":{"1"`,
		`"items":{"type":"string","enum":["explorex.control.v1"]}`,
		`"constraints":{"positive":["width","height"],"max_sums":{"x+width":10000,"y+height":10000}}`,
		`"op":{"type":"string","enum":["list"]}`,
		`"date":{"type":"string"}`,
	} {
		if !strings.Contains(seed, fragment) {
			t.Errorf("product DP seed missing conditional/nested schema %q", fragment)
		}
	}
}
