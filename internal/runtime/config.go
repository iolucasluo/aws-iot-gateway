package runtime

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var internalAppIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{2,63}$`)

type InternalApp struct {
	ID                string   `json:"id"`
	APIKey            string   `json:"api_key"`
	APISecret         string   `json:"api_secret"`
	WebhookURL        string   `json:"webhook_url,omitempty"`
	WebhookSecret     string   `json:"webhook_secret,omitempty"`
	WebhookEventTypes []string `json:"webhook_event_types,omitempty"`
	Permissions       []string `json:"permissions,omitempty"`
}

func (app InternalApp) HasPermission(permission string) bool {
	for _, candidate := range app.Permissions {
		if candidate == permission {
			return true
		}
	}
	return false
}

type Config struct {
	ListenAddress                      string
	Environment                        string
	ProviderMode                       string
	ProviderCompositionPath            string
	DatabaseURL                        string
	RedisURL                           string
	AWSRegion                          string
	DevicePolicyName                   string
	IoTTopicPrefix                     string
	IoTCredentialEndpoint              string
	IoTRoleAlias                       string
	IoTShadowName                      string
	IngestQueueURL                     string
	KVSChannelPrefix                   string
	S3MediaBucket                      string
	S3OTABucket                        string
	OTADevArtifactAllowedOrigins       []string
	TimeBootstrapKeyID                 string
	TimeBootstrapSigner                string
	TimeBootstrapKMSKeyID              string
	TimeBootstrapLocalPrivateKeyPath   string
	DomesticMQTTEndpoint               string
	DomesticMQTTTopicPrefix            string
	DomesticMQTTClientID               string
	DomesticMQTTClientCertPath         string
	DomesticMQTTClientKeyPath          string
	DomesticCACertPath                 string
	DomesticCAKeyPath                  string
	DomesticSignerPrivateKeyPath       string
	DomesticSignerKeyID                string
	DomesticRealtimeEndpoint           string
	DomesticRealtimeAuthorizationToken string
	InternalApps                       []InternalApp
	DirectACKSubscriberEnabled         bool
	OTADeviceDownloadBaseURL           string
}

func LoadConfig(getenv func(string) string) (Config, error) {
	databaseURL, err := LoadDatabaseURL(getenv)
	if err != nil {
		return Config{}, err
	}
	directACKSubscriberEnabled, err := parseOptionalBool(
		"AWS_IOT_DIRECT_ACK_SUBSCRIBER_ENABLED", getenv("AWS_IOT_DIRECT_ACK_SUBSCRIBER_ENABLED"),
	)
	if err != nil {
		return Config{}, err
	}
	config := Config{
		ListenAddress:                      valueOr(getenv("GATEWAY_LISTEN_ADDRESS"), ":14080"),
		Environment:                        valueOr(getenv("GATEWAY_ENVIRONMENT"), "development"),
		ProviderMode:                       valueOr(getenv("GATEWAY_PROVIDER_MODE"), "aws"),
		ProviderCompositionPath:            strings.TrimSpace(getenv("GATEWAY_PROVIDER_COMPOSITION_PATH")),
		DatabaseURL:                        databaseURL,
		RedisURL:                           strings.TrimSpace(getenv("REDIS_URL")),
		AWSRegion:                          strings.TrimSpace(getenv("AWS_REGION")),
		DevicePolicyName:                   strings.TrimSpace(getenv("AWS_IOT_DEVICE_POLICY_NAME")),
		IoTTopicPrefix:                     strings.TrimSpace(getenv("AWS_IOT_TOPIC_PREFIX")),
		IoTCredentialEndpoint:              strings.TrimSpace(getenv("AWS_IOT_CREDENTIAL_PROVIDER_ENDPOINT")),
		IoTRoleAlias:                       strings.TrimSpace(getenv("AWS_IOT_ROLE_ALIAS")),
		IoTShadowName:                      strings.TrimSpace(getenv("AWS_IOT_SHADOW_NAME")),
		IngestQueueURL:                     strings.TrimSpace(getenv("AWS_IOT_INGEST_QUEUE_URL")),
		KVSChannelPrefix:                   strings.TrimSpace(getenv("AWS_KVS_CHANNEL_PREFIX")),
		S3MediaBucket:                      strings.TrimSpace(getenv("AWS_S3_MEDIA_BUCKET")),
		S3OTABucket:                        strings.TrimSpace(getenv("AWS_S3_OTA_BUCKET")),
		TimeBootstrapKeyID:                 strings.TrimSpace(getenv("TIME_BOOTSTRAP_KEY_ID")),
		TimeBootstrapSigner:                valueOr(getenv("TIME_BOOTSTRAP_SIGNER"), "aws-kms"),
		TimeBootstrapKMSKeyID:              strings.TrimSpace(getenv("TIME_BOOTSTRAP_KMS_KEY_ID")),
		TimeBootstrapLocalPrivateKeyPath:   strings.TrimSpace(getenv("TIME_BOOTSTRAP_LOCAL_PRIVATE_KEY_PATH")),
		DomesticMQTTEndpoint:               strings.TrimSpace(getenv("DOMESTIC_MQTT_ENDPOINT")),
		DomesticMQTTTopicPrefix:            strings.TrimSpace(getenv("DOMESTIC_MQTT_TOPIC_PREFIX")),
		DomesticMQTTClientID:               strings.TrimSpace(getenv("DOMESTIC_MQTT_CLIENT_ID")),
		DomesticMQTTClientCertPath:         strings.TrimSpace(getenv("DOMESTIC_MQTT_CLIENT_CERT_PATH")),
		DomesticMQTTClientKeyPath:          strings.TrimSpace(getenv("DOMESTIC_MQTT_CLIENT_KEY_PATH")),
		DomesticCACertPath:                 strings.TrimSpace(getenv("DOMESTIC_CA_CERT_PATH")),
		DomesticCAKeyPath:                  strings.TrimSpace(getenv("DOMESTIC_CA_KEY_PATH")),
		DomesticSignerPrivateKeyPath:       strings.TrimSpace(getenv("DOMESTIC_SIGNER_PRIVATE_KEY_PATH")),
		DomesticSignerKeyID:                strings.TrimSpace(getenv("DOMESTIC_SIGNER_KEY_ID")),
		DomesticRealtimeEndpoint:           strings.TrimSpace(getenv("DOMESTIC_REALTIME_ENDPOINT")),
		DomesticRealtimeAuthorizationToken: strings.TrimSpace(getenv("DOMESTIC_REALTIME_AUTH_TOKEN")),
		DirectACKSubscriberEnabled:         directACKSubscriberEnabled,
		OTADeviceDownloadBaseURL:           strings.TrimSpace(getenv("OTA_DEVICE_DOWNLOAD_BASE_URL")),
	}
	if !validEnvironment(config.Environment) {
		return Config{}, fmt.Errorf("GATEWAY_ENVIRONMENT must be development, staging, or production")
	}
	if config.ProviderMode != "aws" && config.ProviderMode != "domestic-dev" {
		return Config{}, fmt.Errorf("GATEWAY_PROVIDER_MODE must be aws or domestic-dev")
	}
	if config.ProviderMode == "domestic-dev" {
		if config.Environment != "development" {
			return Config{}, fmt.Errorf("GATEWAY_PROVIDER_MODE=domestic-dev is allowed only when GATEWAY_ENVIRONMENT=development")
		}
		if config.ProviderCompositionPath == "" {
			return Config{}, fmt.Errorf("GATEWAY_PROVIDER_COMPOSITION_PATH is required for domestic-dev")
		}
		if config.DomesticMQTTEndpoint == "" || config.DomesticMQTTTopicPrefix == "" ||
			config.DomesticMQTTClientID == "" || config.DomesticMQTTClientCertPath == "" ||
			config.DomesticMQTTClientKeyPath == "" ||
			config.DomesticCACertPath == "" || config.DomesticCAKeyPath == "" ||
			config.DomesticSignerPrivateKeyPath == "" || config.DomesticSignerKeyID == "" {
			return Config{}, fmt.Errorf("DOMESTIC_MQTT_ENDPOINT, DOMESTIC_MQTT_TOPIC_PREFIX, DOMESTIC_MQTT_CLIENT_ID, DOMESTIC_MQTT_CLIENT_CERT_PATH, DOMESTIC_MQTT_CLIENT_KEY_PATH, DOMESTIC_CA_CERT_PATH, DOMESTIC_CA_KEY_PATH, DOMESTIC_SIGNER_PRIVATE_KEY_PATH, and DOMESTIC_SIGNER_KEY_ID are required for domestic-dev")
		}
		if config.DomesticRealtimeEndpoint == "" || config.DomesticRealtimeAuthorizationToken == "" {
			return Config{}, fmt.Errorf("DOMESTIC_REALTIME_ENDPOINT and DOMESTIC_REALTIME_AUTH_TOKEN are required for domestic-dev")
		}
		if err := validateHTTPURL("DOMESTIC_REALTIME_ENDPOINT", config.DomesticRealtimeEndpoint); err != nil || strings.Contains(config.DomesticRealtimeEndpoint, "amazonaws.com") {
			return Config{}, fmt.Errorf("DOMESTIC_REALTIME_ENDPOINT must be a non-AWS HTTP(S) URL")
		}
		if strings.ContainsAny(config.DomesticRealtimeAuthorizationToken, "\r\n") {
			return Config{}, fmt.Errorf("DOMESTIC_REALTIME_AUTH_TOKEN is invalid")
		}
		if strings.Contains(config.DomesticMQTTEndpoint, "amazonaws.com") ||
			strings.Contains(config.DomesticCACertPath, "aws") || strings.Contains(config.DomesticCAKeyPath, "aws") {
			return Config{}, fmt.Errorf("domestic-dev configuration must not reference AWS")
		}
		if directACKSubscriberEnabled {
			return Config{}, fmt.Errorf("AWS_IOT_DIRECT_ACK_SUBSCRIBER_ENABLED is not allowed for domestic-dev")
		}
	}
	if config.DirectACKSubscriberEnabled && config.Environment != "development" {
		return Config{}, fmt.Errorf("AWS_IOT_DIRECT_ACK_SUBSCRIBER_ENABLED is allowed only when GATEWAY_ENVIRONMENT=development")
	}
	if config.ProviderMode == "aws" && (config.AWSRegion == "" || config.DevicePolicyName == "" || config.IoTTopicPrefix == "" ||
		config.IoTCredentialEndpoint == "" || config.IoTRoleAlias == "" || config.IoTShadowName == "") {
		return Config{}, fmt.Errorf("AWS_REGION, AWS_IOT_DEVICE_POLICY_NAME, AWS_IOT_TOPIC_PREFIX, AWS_IOT_CREDENTIAL_PROVIDER_ENDPOINT, AWS_IOT_ROLE_ALIAS, and AWS_IOT_SHADOW_NAME are required")
	}
	if config.ProviderMode == "aws" && !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`).MatchString(config.IoTTopicPrefix) {
		return Config{}, fmt.Errorf("AWS_IOT_TOPIC_PREFIX is invalid")
	}
	if config.ProviderMode == "aws" && (!regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{1,253}$`).MatchString(config.IoTCredentialEndpoint) ||
		!strings.Contains(config.IoTCredentialEndpoint, ".credentials.iot.") ||
		!strings.HasSuffix(config.IoTCredentialEndpoint, ".amazonaws.com")) {
		return Config{}, fmt.Errorf("AWS_IOT_CREDENTIAL_PROVIDER_ENDPOINT is invalid")
	}
	if config.ProviderMode == "aws" && !regexp.MustCompile(`^[A-Za-z0-9_+=,.@-]{1,128}$`).MatchString(config.IoTRoleAlias) {
		return Config{}, fmt.Errorf("AWS_IOT_ROLE_ALIAS is invalid")
	}
	if config.ProviderMode == "aws" && !regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`).MatchString(config.IoTShadowName) {
		return Config{}, fmt.Errorf("AWS_IOT_SHADOW_NAME is invalid")
	}
	if config.ProviderMode == "aws" {
		if err := validateHTTPURL("AWS_IOT_INGEST_QUEUE_URL", config.IngestQueueURL); err != nil {
			return Config{}, err
		}
		if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`).MatchString(config.KVSChannelPrefix) {
			return Config{}, fmt.Errorf("AWS_KVS_CHANNEL_PREFIX is invalid")
		}
		if !regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`).MatchString(config.S3MediaBucket) {
			return Config{}, fmt.Errorf("AWS_S3_MEDIA_BUCKET is invalid")
		}
		otaBucketPattern := regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
		if config.S3OTABucket != "" && !otaBucketPattern.MatchString(config.S3OTABucket) {
			return Config{}, fmt.Errorf("AWS_S3_OTA_BUCKET is invalid")
		}
	}
	rawOTAOrigins := strings.TrimSpace(getenv("OTA_DEV_ARTIFACT_ALLOWED_ORIGINS"))
	if rawOTAOrigins != "" {
		if config.Environment != "development" {
			return Config{}, fmt.Errorf("OTA_DEV_ARTIFACT_ALLOWED_ORIGINS is allowed only for development")
		}
		seenOrigins := map[string]struct{}{}
		for _, rawOrigin := range strings.Split(rawOTAOrigins, ",") {
			origin := strings.TrimSpace(rawOrigin)
			parsed, err := url.Parse(origin)
			if err != nil || parsed.User != nil || parsed.Host == "" ||
				(parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Path != "" ||
				parsed.RawQuery != "" || parsed.Fragment != "" {
				return Config{}, fmt.Errorf("OTA_DEV_ARTIFACT_ALLOWED_ORIGINS contains an invalid origin")
			}
			if _, duplicate := seenOrigins[origin]; duplicate {
				return Config{}, fmt.Errorf("OTA_DEV_ARTIFACT_ALLOWED_ORIGINS contains a duplicate origin")
			}
			seenOrigins[origin] = struct{}{}
			config.OTADevArtifactAllowedOrigins = append(config.OTADevArtifactAllowedOrigins, origin)
		}
	}
	if config.ProviderMode == "aws" && config.Environment != "development" && config.S3OTABucket == "" {
		return Config{}, fmt.Errorf("AWS_S3_OTA_BUCKET is required outside development")
	}
	if config.Environment != "development" {
		parsed, err := url.Parse(config.OTADeviceDownloadBaseURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
			(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
			return Config{}, fmt.Errorf("OTA_DEVICE_DOWNLOAD_BASE_URL must be an HTTPS origin outside development")
		}
	}
	if config.ProviderMode == "aws" && config.Environment == "development" && config.S3OTABucket == "" && len(config.OTADevArtifactAllowedOrigins) == 0 {
		return Config{}, fmt.Errorf("AWS_S3_OTA_BUCKET or OTA_DEV_ARTIFACT_ALLOWED_ORIGINS is required for development OTA")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`).MatchString(config.TimeBootstrapKeyID) {
		return Config{}, fmt.Errorf("TIME_BOOTSTRAP_KEY_ID is required and must be a safe COSE kid")
	}
	switch config.TimeBootstrapSigner {
	case "local":
		if config.Environment != "development" {
			return Config{}, fmt.Errorf("TIME_BOOTSTRAP_SIGNER=local is allowed only for development")
		}
		if config.TimeBootstrapLocalPrivateKeyPath == "" ||
			strings.ContainsAny(config.TimeBootstrapLocalPrivateKeyPath, "\r\n") {
			return Config{}, fmt.Errorf("TIME_BOOTSTRAP_LOCAL_PRIVATE_KEY_PATH is required and invalid for local signer")
		}
		if config.TimeBootstrapKMSKeyID != "" {
			return Config{}, fmt.Errorf("TIME_BOOTSTRAP_KMS_KEY_ID must be empty for local signer")
		}
	case "aws-kms":
		if config.Environment == "development" {
			return Config{}, fmt.Errorf("TIME_BOOTSTRAP_SIGNER=aws-kms is not allowed for development")
		}
		if config.TimeBootstrapKMSKeyID == "" || len(config.TimeBootstrapKMSKeyID) > 2048 || strings.ContainsAny(config.TimeBootstrapKMSKeyID, "\r\n") {
			return Config{}, fmt.Errorf("TIME_BOOTSTRAP_KMS_KEY_ID is required and invalid for aws-kms signer")
		}
		if config.TimeBootstrapLocalPrivateKeyPath != "" {
			return Config{}, fmt.Errorf("TIME_BOOTSTRAP_LOCAL_PRIVATE_KEY_PATH must be empty for aws-kms signer")
		}
	default:
		return Config{}, fmt.Errorf("TIME_BOOTSTRAP_SIGNER must be local or aws-kms")
	}
	if err := validateServiceURL("DATABASE_URL", config.DatabaseURL, "postgres", "postgresql"); err != nil {
		return Config{}, err
	}
	if err := validateServiceURL("REDIS_URL", config.RedisURL, "redis", "rediss"); err != nil {
		return Config{}, err
	}
	appsJSON := strings.TrimSpace(getenv("INTERNAL_APPS_JSON"))
	if appsJSON == "" {
		return Config{}, fmt.Errorf("INTERNAL_APPS_JSON is required")
	}
	decoder := json.NewDecoder(strings.NewReader(appsJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config.InternalApps); err != nil {
		return Config{}, fmt.Errorf("decode INTERNAL_APPS_JSON: %w", err)
	}
	if len(config.InternalApps) == 0 {
		return Config{}, fmt.Errorf("INTERNAL_APPS_JSON requires at least one app")
	}
	if err := validateInternalApps(config.InternalApps); err != nil {
		return Config{}, err
	}
	return config, nil
}

func parseOptionalBool(name, value string) (bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return false, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", name)
	}
	return parsed, nil
}

func LoadDatabaseURL(getenv func(string) string) (string, error) {
	if databaseURL := strings.TrimSpace(getenv("DATABASE_URL")); databaseURL != "" {
		return databaseURL, nil
	}
	host := strings.TrimSpace(getenv("PGHOST"))
	port := strings.TrimSpace(getenv("PGPORT"))
	database := strings.TrimSpace(getenv("PGDATABASE"))
	user := strings.TrimSpace(getenv("PGUSER"))
	password := getenv("PGPASSWORD")
	if host == "" || port == "" || database == "" || user == "" || password == "" ||
		strings.ContainsAny(password, "\r\n") {
		return "", fmt.Errorf("DATABASE_URL or PGHOST, PGPORT, PGDATABASE, PGUSER, and PGPASSWORD are required")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 || strings.ContainsAny(host+database+user, "\r\n") {
		return "", fmt.Errorf("PGHOST, PGPORT, PGDATABASE, PGUSER, and PGPASSWORD are invalid")
	}
	databaseURL := &url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, password),
		Host:     host + ":" + port,
		Path:     "/" + database,
		RawQuery: "sslmode=require",
	}
	return databaseURL.String(), nil
}

func validateHTTPURL(name, value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil {
		return fmt.Errorf("%s must be a valid HTTP(S) URL without credentials", name)
	}
	return nil
}

func validateInternalApps(apps []InternalApp) error {
	seenIDs := make(map[string]struct{}, len(apps))
	seenKeys := make(map[string]struct{}, len(apps))
	for index := range apps {
		app := &apps[index]
		app.ID = strings.TrimSpace(app.ID)
		app.APIKey = strings.TrimSpace(app.APIKey)
		if !internalAppIDPattern.MatchString(app.ID) {
			return fmt.Errorf("INTERNAL_APPS_JSON contains an invalid app id")
		}
		if _, duplicate := seenIDs[app.ID]; duplicate {
			return fmt.Errorf("INTERNAL_APPS_JSON contains a duplicate app id")
		}
		seenIDs[app.ID] = struct{}{}
		if app.APIKey == "" || app.APIKey != strings.TrimSpace(app.APIKey) || strings.ContainsAny(app.APIKey, "\r\n") {
			return fmt.Errorf("INTERNAL_APPS_JSON contains an invalid api key")
		}
		if _, duplicate := seenKeys[app.APIKey]; duplicate {
			return fmt.Errorf("INTERNAL_APPS_JSON contains a duplicate api key")
		}
		seenKeys[app.APIKey] = struct{}{}
		if len(app.APISecret) < 8 || app.APISecret != strings.TrimSpace(app.APISecret) || strings.ContainsAny(app.APISecret, "\r\n") {
			return fmt.Errorf("INTERNAL_APPS_JSON contains an invalid api secret")
		}
		if err := validateWebhook(app); err != nil {
			return err
		}
		seenEventTypes := make(map[string]struct{}, len(app.WebhookEventTypes))
		for eventIndex, eventType := range app.WebhookEventTypes {
			eventType = strings.TrimSpace(eventType)
			if eventType == "" || strings.ContainsAny(eventType, "\r\n") {
				return fmt.Errorf("INTERNAL_APPS_JSON contains an invalid webhook event type")
			}
			if _, duplicate := seenEventTypes[eventType]; duplicate {
				return fmt.Errorf("INTERNAL_APPS_JSON contains a duplicate webhook event type")
			}
			seenEventTypes[eventType] = struct{}{}
			app.WebhookEventTypes[eventIndex] = eventType
		}
		seenPermissions := make(map[string]struct{}, len(app.Permissions))
		for permissionIndex, permission := range app.Permissions {
			permission = strings.TrimSpace(permission)
			if permission != "ota:publish" && permission != "ota:deploy" {
				return fmt.Errorf("INTERNAL_APPS_JSON contains an invalid permission")
			}
			if _, duplicate := seenPermissions[permission]; duplicate {
				return fmt.Errorf("INTERNAL_APPS_JSON contains a duplicate permission")
			}
			seenPermissions[permission] = struct{}{}
			app.Permissions[permissionIndex] = permission
		}
	}
	return nil
}

func validateWebhook(app *InternalApp) error {
	app.WebhookURL = strings.TrimSpace(app.WebhookURL)
	if app.WebhookURL == "" && app.WebhookSecret == "" {
		return nil
	}
	if app.WebhookURL == "" || len(app.WebhookSecret) < 8 ||
		app.WebhookSecret != strings.TrimSpace(app.WebhookSecret) || strings.ContainsAny(app.WebhookSecret, "\r\n") {
		return fmt.Errorf("INTERNAL_APPS_JSON requires webhook URL and secret together")
	}
	parsed, err := url.Parse(app.WebhookURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("INTERNAL_APPS_JSON webhook URL must be HTTPS without credentials, query, or fragment")
	}
	return nil
}

func validateServiceURL(name, value string, schemes ...string) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("%s must be a valid service URL", name)
	}
	for _, scheme := range schemes {
		if parsed.Scheme == scheme {
			return nil
		}
	}
	return fmt.Errorf("%s uses an unsupported scheme", name)
}

func validEnvironment(value string) bool {
	switch value {
	case "development", "staging", "production":
		return true
	default:
		return false
	}
}

func valueOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func LoadConfigFromEnvironment() (Config, error) {
	return LoadConfig(os.Getenv)
}
