package runtime

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/credentialcipher"
)

// PlatformConfig 是 cmd/explorex-platform-api 的配置。
// 它与设备侧 Config 分离：平台进程不需要 AWS IoT/KVS/S3/KMS 参数。
type PlatformConfig struct {
	ListenAddress      string
	Environment        string
	DatabaseURL        string
	RedisURL           string
	CORSAllowedOrigins []string
	SessionTTL         time.Duration
	SetupTokenTTL      time.Duration
	ShutdownTimeout    time.Duration
	LoginRateLimit     int
	LoginRateWindow    time.Duration
	TrustedProxyHeader string
	// CredentialKEK 是项目凭证 secret 的 AES-256-GCM 主密钥（base64）。
	// development 可留空（项目/凭证路由返回 503）；其它环境必填。
	CredentialKEK   string
	CredentialKEKID string
	// GatewayInternalBaseURL 是设备 I/O 与 OTA 代理的目标（gateway 内部 API）。
	// 为空时这些路由返回 503；只读运维视图不依赖它。
	GatewayInternalBaseURL string
}

const (
	platformMinSessionTTL = 15 * time.Minute
	platformMaxSessionTTL = 30 * 24 * time.Hour
	platformMinSetupTTL   = 15 * time.Minute
	platformMaxSetupTTL   = 7 * 24 * time.Hour
	platformMaxLoginRate  = 1000
)

// LoadPlatformConfig 读取并校验平台 API 的环境配置。
// 非 development 环境必须显式提供 CORS 白名单，且不允许 "*"。
func LoadPlatformConfig(getenv func(string) string) (PlatformConfig, error) {
	databaseURL, err := LoadDatabaseURL(getenv)
	if err != nil {
		return PlatformConfig{}, err
	}
	sessionTTL, err := platformDuration("PLATFORM_SESSION_TTL", getenv("PLATFORM_SESSION_TTL"), 12*time.Hour,
		platformMinSessionTTL, platformMaxSessionTTL)
	if err != nil {
		return PlatformConfig{}, err
	}
	setupTTL, err := platformDuration("PLATFORM_SETUP_TOKEN_TTL", getenv("PLATFORM_SETUP_TOKEN_TTL"), 48*time.Hour,
		platformMinSetupTTL, platformMaxSetupTTL)
	if err != nil {
		return PlatformConfig{}, err
	}
	shutdownTimeout, err := platformDuration("PLATFORM_SHUTDOWN_TIMEOUT", getenv("PLATFORM_SHUTDOWN_TIMEOUT"), 20*time.Second,
		time.Second, 5*time.Minute)
	if err != nil {
		return PlatformConfig{}, err
	}
	rateWindow, err := platformDuration("PLATFORM_LOGIN_RATE_WINDOW", getenv("PLATFORM_LOGIN_RATE_WINDOW"), 15*time.Minute,
		time.Second, 24*time.Hour)
	if err != nil {
		return PlatformConfig{}, err
	}
	rateLimit, err := platformInt("PLATFORM_LOGIN_RATE_LIMIT", getenv("PLATFORM_LOGIN_RATE_LIMIT"), 5, 1, platformMaxLoginRate)
	if err != nil {
		return PlatformConfig{}, err
	}
	config := PlatformConfig{
		ListenAddress:      valueOr(getenv("PLATFORM_LISTEN_ADDRESS"), ":14081"),
		Environment:        valueOr(getenv("GATEWAY_ENVIRONMENT"), "development"),
		DatabaseURL:        databaseURL,
		RedisURL:           strings.TrimSpace(getenv("REDIS_URL")),
		CORSAllowedOrigins: platformCSV(getenv("PLATFORM_CORS_ALLOWED_ORIGINS")),
		SessionTTL:         sessionTTL,
		SetupTokenTTL:      setupTTL,
		ShutdownTimeout:    shutdownTimeout,
		LoginRateLimit:     rateLimit,
		LoginRateWindow:    rateWindow,
		TrustedProxyHeader: strings.TrimSpace(getenv("PLATFORM_TRUSTED_PROXY_HEADER")),
		CredentialKEK:      strings.TrimSpace(getenv("PLATFORM_CREDENTIAL_KEK")),
		CredentialKEKID:    valueOr(getenv("PLATFORM_CREDENTIAL_KEK_ID"), "kek-v1"),
		GatewayInternalBaseURL: strings.TrimSuffix(
			strings.TrimSpace(getenv("PLATFORM_GATEWAY_INTERNAL_BASE_URL")), "/"),
	}
	if !validEnvironment(config.Environment) {
		return PlatformConfig{}, fmt.Errorf("GATEWAY_ENVIRONMENT must be development, staging, or production")
	}
	if _, port, err := net.SplitHostPort(config.ListenAddress); err != nil || port == "" {
		return PlatformConfig{}, fmt.Errorf("PLATFORM_LISTEN_ADDRESS must be host:port")
	} else if _, err := strconv.Atoi(port); err != nil {
		return PlatformConfig{}, fmt.Errorf("PLATFORM_LISTEN_ADDRESS port must be numeric")
	}
	if err := validateServiceURL("DATABASE_URL", config.DatabaseURL, "postgres", "postgresql"); err != nil {
		return PlatformConfig{}, err
	}
	if err := validateServiceURL("REDIS_URL", config.RedisURL, "redis", "rediss"); err != nil {
		return PlatformConfig{}, err
	}
	for _, origin := range config.CORSAllowedOrigins {
		if origin == "*" || (!strings.HasPrefix(origin, "http://") && !strings.HasPrefix(origin, "https://")) {
			return PlatformConfig{}, fmt.Errorf("PLATFORM_CORS_ALLOWED_ORIGINS entries must be explicit http(s) origins")
		}
	}
	if config.Environment != "development" && len(config.CORSAllowedOrigins) == 0 {
		return PlatformConfig{}, fmt.Errorf("PLATFORM_CORS_ALLOWED_ORIGINS is required outside development")
	}
	if strings.ContainsAny(config.TrustedProxyHeader, ":\r\n ") {
		return PlatformConfig{}, fmt.Errorf("PLATFORM_TRUSTED_PROXY_HEADER must be a single header name")
	}
	if config.CredentialKEK != "" {
		if _, err := credentialcipher.NewFromBase64(config.CredentialKEK, config.CredentialKEKID); err != nil {
			return PlatformConfig{}, fmt.Errorf("PLATFORM_CREDENTIAL_KEK is invalid: %w", err)
		}
	} else if config.Environment != "development" {
		return PlatformConfig{}, fmt.Errorf("PLATFORM_CREDENTIAL_KEK is required outside development")
	}
	return config, nil
}

// LoadPlatformConfigFromEnvironment 是进程入口使用的便捷封装。
func LoadPlatformConfigFromEnvironment() (PlatformConfig, error) {
	return LoadPlatformConfig(os.Getenv)
}

func platformDuration(name, value string, fallback, minimum, maximum time.Duration) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration such as 12h", name)
	}
	if parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("%s must be between %s and %s", name, minimum, maximum)
	}
	return parsed, nil
}

func platformInt(name, value string, fallback, minimum, maximum int) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	if parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("%s must be between %d and %d", name, minimum, maximum)
	}
	return parsed, nil
}

func platformCSV(value string) []string {
	values := make([]string, 0, 4)
	for _, item := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			values = append(values, trimmed)
		}
	}
	return values
}
