package runtime

import (
	"fmt"
	"io"
	"os"
	"strings"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/provider"
)

// maxProviderCompositionBytes bounds the operator-controlled composition
// document before YAML parsing. A composition contains only metadata and
// provider references; it must never become a vehicle for secrets or a large
// unbounded input.
const maxProviderCompositionBytes = 1 << 20

// LoadProviderComposition loads the composition selected by the deployment
// environment and checks that it belongs to that environment. The path is
// deliberately explicit: a domestic deployment cannot silently use the AWS
// default startup path, and a missing file fails before any network client is
// created.
func LoadProviderComposition(getenv func(string) string) (provider.Composition, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	expectedEnvironment := valueOr(getenv("GATEWAY_ENVIRONMENT"), "development")
	if !validEnvironment(expectedEnvironment) {
		return provider.Composition{}, fmt.Errorf("GATEWAY_ENVIRONMENT must be development, staging, or production")
	}
	path := strings.TrimSpace(getenv("GATEWAY_PROVIDER_COMPOSITION_PATH"))
	if path == "" {
		return provider.Composition{}, fmt.Errorf("GATEWAY_PROVIDER_COMPOSITION_PATH is required")
	}
	composition, err := LoadProviderCompositionFile(path)
	if err != nil {
		return provider.Composition{}, err
	}
	if composition.Metadata.Environment != expectedEnvironment {
		return provider.Composition{}, fmt.Errorf(
			"provider composition environment %q does not match GATEWAY_ENVIRONMENT %q",
			composition.Metadata.Environment, expectedEnvironment,
		)
	}
	return composition, nil
}

// LoadProviderCompositionFile reads one bounded, regular file and delegates
// strict YAML decoding and graph validation to the provider package. Symlinks
// are rejected so a deployment cannot unexpectedly cross its configured file
// boundary at runtime.
func LoadProviderCompositionFile(path string) (provider.Composition, error) {
	path = strings.TrimSpace(path)
	if path == "" || strings.ContainsAny(path, "\r\n") {
		return provider.Composition{}, fmt.Errorf("provider composition path is invalid")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return provider.Composition{}, fmt.Errorf("stat provider composition: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return provider.Composition{}, fmt.Errorf("provider composition must be a regular non-symlink file")
	}
	if info.Size() > maxProviderCompositionBytes {
		return provider.Composition{}, fmt.Errorf("provider composition exceeds %d bytes", maxProviderCompositionBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return provider.Composition{}, fmt.Errorf("open provider composition: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxProviderCompositionBytes+1))
	if err != nil {
		return provider.Composition{}, fmt.Errorf("read provider composition: %w", err)
	}
	if len(data) > maxProviderCompositionBytes {
		return provider.Composition{}, fmt.Errorf("provider composition exceeds %d bytes", maxProviderCompositionBytes)
	}
	composition, err := provider.LoadComposition(strings.NewReader(string(data)))
	if err != nil {
		return provider.Composition{}, fmt.Errorf("load provider composition: %w", err)
	}
	return composition, nil
}
