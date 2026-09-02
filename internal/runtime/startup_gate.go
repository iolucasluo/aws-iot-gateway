package runtime

import "fmt"

// RequireAWSDataPlane is the explicit guard for the legacy AWS application
// builder. Domestic compositions must use a different provider-aware builder;
// silently falling through here would initialize AWS SDK clients and violate
// the no-AWS data-plane boundary.
func RequireAWSDataPlane(config Config) error {
	if config.ProviderMode != "aws" {
		return fmt.Errorf("AWS data-plane startup is forbidden for provider mode %q", config.ProviderMode)
	}
	return nil
}
