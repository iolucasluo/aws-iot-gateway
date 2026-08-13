package runtime

import "testing"

func TestLoadConfigRejectsMissingSecretsAndAcceptsInjectedTokenMap(t *testing.T) {
	t.Parallel()

	env := map[string]string{
		"AWS_REGION":                   "ap-southeast-1",
		"AWS_IOT_THING_PREFIX":         "aws-iot-demo-",
		"AWS_IOT_DEVICE_POLICY_NAME":   "aws-iot-demo-device",
		"GATEWAY_INTERNAL_TOKENS_JSON": `{"injected-demo-token":"internal-user-42"}`,
	}
	config, err := LoadConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if config.ListenAddress != ":14080" || config.ActivationState != "./var/activations.json" ||
		config.InternalTokens["injected-demo-token"] != "internal-user-42" {
		t.Fatalf("LoadConfig() = %#v", config)
	}

	delete(env, "GATEWAY_INTERNAL_TOKENS_JSON")
	if _, err := LoadConfig(func(key string) string { return env[key] }); err == nil {
		t.Fatal("LoadConfig() should reject a missing injected token map")
	}
}
