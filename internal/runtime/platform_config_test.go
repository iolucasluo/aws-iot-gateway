package runtime

import (
	"strings"
	"testing"
	"time"
)

func platformEnv(overrides map[string]string) func(string) string {
	base := map[string]string{
		"DATABASE_URL": "postgres://gateway:secret@localhost:5432/gateway",
		"REDIS_URL":    "redis://localhost:6379/0",
	}
	return func(key string) string {
		if value, found := overrides[key]; found {
			return value
		}
		return base[key]
	}
}

func TestLoadPlatformConfigDefaultsAndOverrides(t *testing.T) {
	t.Parallel()
	config, err := LoadPlatformConfig(platformEnv(nil))
	if err != nil {
		t.Fatalf("LoadPlatformConfig() error = %v", err)
	}
	if config.ListenAddress != ":14081" || config.Environment != "development" {
		t.Fatalf("defaults = %+v, want :14081 in development", config)
	}
	if config.SessionTTL != 12*time.Hour || config.SetupTokenTTL != 48*time.Hour {
		t.Fatalf("session/setup TTL = %s/%s", config.SessionTTL, config.SetupTokenTTL)
	}
	if config.LoginRateLimit != 5 || config.LoginRateWindow != 15*time.Minute {
		t.Fatalf("rate limit = %d/%s, want 5 per 15m", config.LoginRateLimit, config.LoginRateWindow)
	}
	if len(config.CORSAllowedOrigins) != 0 {
		t.Fatalf("development CORS origins = %v, want empty", config.CORSAllowedOrigins)
	}

	overridden, err := LoadPlatformConfig(platformEnv(map[string]string{
		"PLATFORM_LISTEN_ADDRESS":       "127.0.0.1:9000",
		"PLATFORM_SESSION_TTL":          "1h",
		"PLATFORM_SETUP_TOKEN_TTL":      "30m",
		"PLATFORM_LOGIN_RATE_LIMIT":     "10",
		"PLATFORM_LOGIN_RATE_WINDOW":    "5m",
		"PLATFORM_CORS_ALLOWED_ORIGINS": " https://admin.example.com ,https://dev.example.com ",
		"PLATFORM_TRUSTED_PROXY_HEADER": "X-Forwarded-For",
		"PLATFORM_SHUTDOWN_TIMEOUT":     "5s",
	}))
	if err != nil {
		t.Fatalf("LoadPlatformConfig(overrides) error = %v", err)
	}
	if overridden.ListenAddress != "127.0.0.1:9000" || overridden.SessionTTL != time.Hour {
		t.Fatalf("overrides = %+v", overridden)
	}
	if len(overridden.CORSAllowedOrigins) != 2 || overridden.CORSAllowedOrigins[0] != "https://admin.example.com" {
		t.Fatalf("CORS origins = %v, want two trimmed origins", overridden.CORSAllowedOrigins)
	}
	if overridden.TrustedProxyHeader != "X-Forwarded-For" {
		t.Fatalf("trusted proxy header = %q", overridden.TrustedProxyHeader)
	}
}

func TestLoadPlatformConfigRejectsInvalidValues(t *testing.T) {
	t.Parallel()
	for name, overrides := range map[string]map[string]string{
		"listen-address":        {"PLATFORM_LISTEN_ADDRESS": "nope"},
		"listen-port":           {"PLATFORM_LISTEN_ADDRESS": "127.0.0.1:port"},
		"session-ttl-syntax":    {"PLATFORM_SESSION_TTL": "12"},
		"session-ttl-too-short": {"PLATFORM_SESSION_TTL": "1m"},
		"session-ttl-too-long":  {"PLATFORM_SESSION_TTL": "721h"},
		"setup-ttl-too-long":    {"PLATFORM_SETUP_TOKEN_TTL": "240h"},
		"rate-limit-syntax":     {"PLATFORM_LOGIN_RATE_LIMIT": "many"},
		"rate-limit-zero":       {"PLATFORM_LOGIN_RATE_LIMIT": "0"},
		"rate-limit-too-high":   {"PLATFORM_LOGIN_RATE_LIMIT": "5000"},
		"rate-window":           {"PLATFORM_LOGIN_RATE_WINDOW": "0s"},
		"cors-wildcard":         {"PLATFORM_CORS_ALLOWED_ORIGINS": "*"},
		"cors-scheme":           {"PLATFORM_CORS_ALLOWED_ORIGINS": "admin.example.com"},
		"trusted-header":        {"PLATFORM_TRUSTED_PROXY_HEADER": "X Forwarded For"},
	} {
		if _, err := LoadPlatformConfig(platformEnv(overrides)); err == nil {
			t.Errorf("LoadPlatformConfig(%s) error = nil, want a failure", name)
		}
	}
}

func TestLoadPlatformConfigRequiresCORSAndDatabaseOutsideDevelopment(t *testing.T) {
	t.Parallel()
	if _, err := LoadPlatformConfig(platformEnv(map[string]string{"GATEWAY_ENVIRONMENT": "staging"})); err == nil {
		t.Fatal("staging without PLATFORM_CORS_ALLOWED_ORIGINS was accepted")
	}
	if _, err := LoadPlatformConfig(platformEnv(map[string]string{
		"GATEWAY_ENVIRONMENT":           "staging",
		"PLATFORM_CORS_ALLOWED_ORIGINS": "https://admin.example.com",
	})); err != nil {
		t.Fatalf("staging with an explicit origin list was rejected: %v", err)
	}
	if _, err := LoadPlatformConfig(func(string) string { return "" }); err == nil {
		t.Fatal("missing DATABASE_URL and PGHOST were accepted")
	} else if !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("error = %v, want it to name DATABASE_URL", err)
	}
	if _, err := LoadPlatformConfig(func(key string) string {
		switch key {
		case "PGHOST":
			return "localhost"
		case "PGPORT":
			return "5432"
		case "PGDATABASE":
			return "gateway"
		case "PGUSER":
			return "gateway"
		case "PGPASSWORD":
			return "secret"
		case "REDIS_URL":
			return "redis://localhost:6379/0"
		}
		return ""
	}); err != nil {
		t.Fatalf("PG* fallback was rejected: %v", err)
	}
}
