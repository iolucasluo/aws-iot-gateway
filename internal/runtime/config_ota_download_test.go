package runtime

import (
	"strings"
	"testing"
)

func TestStagingRequiresHTTPSOTADeviceDownloadBaseURL(t *testing.T) {
	env := validEnvironmentMap()
	env["OTA_DEVICE_DOWNLOAD_BASE_URL"] = "https://gateway.example"
	config, err := LoadConfig(func(key string) string { return env[key] })
	if err != nil || config.OTADeviceDownloadBaseURL != env["OTA_DEVICE_DOWNLOAD_BASE_URL"] {
		t.Fatalf("LoadConfig() = %+v, %v", config, err)
	}
	for _, invalid := range []string{"", "http://gateway.example", "https://gateway.example/path", "https://user@gateway.example"} {
		bad := validEnvironmentMap()
		bad["OTA_DEVICE_DOWNLOAD_BASE_URL"] = invalid
		if _, err := LoadConfig(func(key string) string { return bad[key] }); err == nil || !strings.Contains(err.Error(), "OTA_DEVICE_DOWNLOAD_BASE_URL") {
			t.Fatalf("LoadConfig() accepted %q: %v", invalid, err)
		}
	}
}
