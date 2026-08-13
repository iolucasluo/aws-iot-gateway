package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	ListenAddress    string
	ActivationState  string
	AWSRegion        string
	ThingPrefix      string
	DevicePolicyName string
	InternalTokens   map[string]string
}

func LoadConfig(getenv func(string) string) (Config, error) {
	config := Config{
		ListenAddress:    valueOr(getenv("GATEWAY_LISTEN_ADDRESS"), ":14080"),
		ActivationState:  valueOr(getenv("GATEWAY_ACTIVATION_STATE_PATH"), "./var/activations.json"),
		AWSRegion:        strings.TrimSpace(getenv("AWS_REGION")),
		ThingPrefix:      strings.TrimSpace(getenv("AWS_IOT_THING_PREFIX")),
		DevicePolicyName: strings.TrimSpace(getenv("AWS_IOT_DEVICE_POLICY_NAME")),
		InternalTokens:   map[string]string{},
	}
	if config.AWSRegion == "" || config.ThingPrefix == "" || config.DevicePolicyName == "" {
		return Config{}, fmt.Errorf("AWS_REGION, AWS_IOT_THING_PREFIX, and AWS_IOT_DEVICE_POLICY_NAME are required")
	}
	if strings.TrimSpace(getenv("GATEWAY_INTERNAL_TOKENS_JSON")) == "" {
		return Config{}, fmt.Errorf("GATEWAY_INTERNAL_TOKENS_JSON is required")
	}
	if err := json.Unmarshal([]byte(getenv("GATEWAY_INTERNAL_TOKENS_JSON")), &config.InternalTokens); err != nil {
		return Config{}, fmt.Errorf("decode GATEWAY_INTERNAL_TOKENS_JSON: %w", err)
	}
	for token, subject := range config.InternalTokens {
		if strings.TrimSpace(token) == "" || strings.TrimSpace(subject) == "" {
			return Config{}, fmt.Errorf("GATEWAY_INTERNAL_TOKENS_JSON requires non-empty token and subject")
		}
	}
	if filepath.IsAbs(config.ActivationState) {
		return Config{}, fmt.Errorf("GATEWAY_ACTIVATION_STATE_PATH must be a container-local relative path")
	}
	return config, nil
}

func valueOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func LoadConfigFromEnvironment() (Config, error) {
	return LoadConfig(os.Getenv)
}
