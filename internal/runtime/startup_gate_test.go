package runtime

import (
	"strings"
	"testing"
)

func TestRequireAWSDataPlaneRejectsDomesticMode(t *testing.T) {
	t.Parallel()

	err := RequireAWSDataPlane(Config{ProviderMode: "domestic-dev"})
	if err == nil || !strings.Contains(err.Error(), "domestic-dev") {
		t.Fatalf("RequireAWSDataPlane() error = %v, want domestic-dev rejection", err)
	}
}

func TestRequireAWSDataPlaneAcceptsAWSMode(t *testing.T) {
	t.Parallel()

	if err := RequireAWSDataPlane(Config{ProviderMode: "aws"}); err != nil {
		t.Fatalf("RequireAWSDataPlane() error = %v", err)
	}
}
