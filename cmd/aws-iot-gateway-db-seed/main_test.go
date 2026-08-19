package main

import "testing"

func TestValidateEnvironmentAllowsOnlyNonProduction(t *testing.T) {
	t.Parallel()

	for _, environment := range []string{"development", "staging"} {
		if err := validateEnvironment(environment); err != nil {
			t.Errorf("validateEnvironment(%q) error = %v", environment, err)
		}
	}
	for _, environment := range []string{"", "production", "test"} {
		if err := validateEnvironment(environment); err == nil {
			t.Errorf("validateEnvironment(%q) error = nil, want rejection", environment)
		}
	}
}
