package runtime

import (
	"net/url"
	"strings"
	"testing"
)

func TestLoadConfigAcceptsEnvironmentOwnedInternalApps(t *testing.T) {
	t.Parallel()

	env := validEnvironmentMap()
	config, err := LoadConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if config.ListenAddress != ":14080" || config.Environment != "staging" {
		t.Fatalf("LoadConfig() = %#v", config)
	}
	if config.DatabaseURL != env["DATABASE_URL"] || config.RedisURL != env["REDIS_URL"] {
		t.Fatalf("LoadConfig() did not preserve service URLs")
	}
	if config.IoTCredentialEndpoint != env["AWS_IOT_CREDENTIAL_PROVIDER_ENDPOINT"] ||
		config.IoTRoleAlias != env["AWS_IOT_ROLE_ALIAS"] ||
		config.IoTShadowName != env["AWS_IOT_SHADOW_NAME"] {
		t.Fatalf("device runtime config = %#v", config)
	}
	if config.TimeBootstrapKeyID != "time-staging-g1" || config.TimeBootstrapKMSKeyID != env["TIME_BOOTSTRAP_KMS_KEY_ID"] {
		t.Fatalf("time bootstrap config = %#v", config)
	}
	if config.S3OTABucket != env["AWS_S3_OTA_BUCKET"] || !config.InternalApps[1].HasPermission("ota:deploy") {
		t.Fatalf("OTA config = bucket=%q app=%#v", config.S3OTABucket, config.InternalApps[1])
	}
	if len(config.InternalApps) != 2 || config.InternalApps[0].ID != "livpet-staging" ||
		config.InternalApps[1].APIKey != "ak_demo_staging" {
		t.Fatalf("InternalApps = %#v", config.InternalApps)
	}
	if config.InternalApps[0].WebhookURL != "https://livpet.example/v1/internal/webhooks/explorex" ||
		config.InternalApps[0].WebhookSecret != "livpet-webhook-secret" {
		t.Fatalf("LivPet webhook config = %#v", config.InternalApps[0])
	}
}

func TestLoadConfigAllowsOnlyDevelopmentOTAArtifactOrigins(t *testing.T) {
	t.Parallel()
	env := validEnvironmentMap()
	env["GATEWAY_ENVIRONMENT"] = "development"
	env["TIME_BOOTSTRAP_SIGNER"] = "local"
	env["TIME_BOOTSTRAP_LOCAL_PRIVATE_KEY_PATH"] = ".local/time-bootstrap/private.pem"
	delete(env, "TIME_BOOTSTRAP_KMS_KEY_ID")
	delete(env, "AWS_S3_OTA_BUCKET")
	env["OTA_DEV_ARTIFACT_ALLOWED_ORIGINS"] = "http://192.168.1.10:18080"
	config, err := LoadConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if len(config.OTADevArtifactAllowedOrigins) != 1 || config.OTADevArtifactAllowedOrigins[0] != env["OTA_DEV_ARTIFACT_ALLOWED_ORIGINS"] {
		t.Fatalf("origins = %#v", config.OTADevArtifactAllowedOrigins)
	}

	env["GATEWAY_ENVIRONMENT"] = "staging"
	env["TIME_BOOTSTRAP_SIGNER"] = "aws-kms"
	delete(env, "TIME_BOOTSTRAP_LOCAL_PRIVATE_KEY_PATH")
	env["TIME_BOOTSTRAP_KMS_KEY_ID"] = "arn:aws:kms:ap-east-1:000000000000:key/00000000-0000-0000-0000-000000000000"
	env["AWS_S3_OTA_BUCKET"] = "aws-iot-gateway-staging-ota-example"
	if _, err := LoadConfig(func(key string) string { return env[key] }); err == nil {
		t.Fatal("LoadConfig() accepted development OTA origin in staging")
	}
}

func TestLoadConfigRejectsUnknownOTAPermission(t *testing.T) {
	t.Parallel()
	env := validEnvironmentMap()
	env["INTERNAL_APPS_JSON"] = `[{"id":"demo-staging","api_key":"ak_demo_staging","api_secret":"demo-api-secret","permissions":["ota:admin"]}]`
	if _, err := LoadConfig(func(key string) string { return env[key] }); err == nil {
		t.Fatal("LoadConfig() accepted unknown OTA permission")
	}
}

func TestLoadConfigRequiresCompleteTimeBootstrapSigner(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"TIME_BOOTSTRAP_KEY_ID", "TIME_BOOTSTRAP_KMS_KEY_ID"} {
		t.Run(key, func(t *testing.T) {
			env := validEnvironmentMap()
			delete(env, key)
			if _, err := LoadConfig(func(key string) string { return env[key] }); err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("LoadConfig() error=%v, want missing %s", err, key)
			}
		})
	}
}

func TestLoadConfigAllowsDevelopmentLocalTimeBootstrapSigner(t *testing.T) {
	t.Parallel()

	env := validEnvironmentMap()
	env["GATEWAY_ENVIRONMENT"] = "development"
	env["TIME_BOOTSTRAP_SIGNER"] = "local"
	env["TIME_BOOTSTRAP_LOCAL_PRIVATE_KEY_PATH"] = ".local/time-bootstrap/private.pem"
	delete(env, "TIME_BOOTSTRAP_KMS_KEY_ID")

	config, err := LoadConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if config.TimeBootstrapSigner != "local" ||
		config.TimeBootstrapLocalPrivateKeyPath != ".local/time-bootstrap/private.pem" ||
		config.TimeBootstrapKMSKeyID != "" {
		t.Fatalf("time bootstrap local config = %#v", config)
	}
}

func TestLoadConfigRejectsTimeBootstrapSignerOutsideItsEnvironment(t *testing.T) {
	t.Parallel()

	for name, mutate := range map[string]func(map[string]string){
		"development KMS": func(env map[string]string) {
			env["GATEWAY_ENVIRONMENT"] = "development"
			env["TIME_BOOTSTRAP_SIGNER"] = "aws-kms"
		},
		"staging local": func(env map[string]string) {
			env["TIME_BOOTSTRAP_SIGNER"] = "local"
			env["TIME_BOOTSTRAP_LOCAL_PRIVATE_KEY_PATH"] = ".local/time-bootstrap/private.pem"
			delete(env, "TIME_BOOTSTRAP_KMS_KEY_ID")
		},
		"local without private key": func(env map[string]string) {
			env["GATEWAY_ENVIRONMENT"] = "development"
			env["TIME_BOOTSTRAP_SIGNER"] = "local"
			delete(env, "TIME_BOOTSTRAP_KMS_KEY_ID")
		},
	} {
		t.Run(name, func(t *testing.T) {
			env := validEnvironmentMap()
			mutate(env)
			if _, err := LoadConfig(func(key string) string { return env[key] }); err == nil {
				t.Fatal("LoadConfig() error = nil, want signer/environment rejection")
			}
		})
	}
}

func TestLoadConfigRejectsLegacyBearerMap(t *testing.T) {
	t.Parallel()

	env := validEnvironmentMap()
	delete(env, "INTERNAL_APPS_JSON")
	env["GATEWAY_INTERNAL_TOKENS_JSON"] = `{"legacy-token":"user-42"}`
	_, err := LoadConfig(func(key string) string { return env[key] })
	if err == nil || !strings.Contains(err.Error(), "INTERNAL_APPS_JSON") {
		t.Fatalf("LoadConfig() error = %v, want missing INTERNAL_APPS_JSON", err)
	}
}

func TestLoadConfigRejectsDuplicateAppIdentityAndUnsafeWebhook(t *testing.T) {
	t.Parallel()

	for name, apps := range map[string]string{
		"duplicate id": `[
			{"id":"same-app","api_key":"ak_one","api_secret":"secret-one"},
			{"id":"same-app","api_key":"ak_two","api_secret":"secret-two"}
		]`,
		"duplicate key": `[
			{"id":"first-app","api_key":"ak_same","api_secret":"secret-one"},
			{"id":"second-app","api_key":"ak_same","api_secret":"secret-two"}
		]`,
		"http webhook": `[
			{"id":"livpet-staging","api_key":"ak_one","api_secret":"secret-one","webhook_url":"http://livpet.example/hook","webhook_secret":"hook-secret"}
		]`,
		"missing webhook secret": `[
			{"id":"livpet-staging","api_key":"ak_one","api_secret":"secret-one","webhook_url":"https://livpet.example/hook"}
		]`,
	} {
		t.Run(name, func(t *testing.T) {
			env := validEnvironmentMap()
			env["INTERNAL_APPS_JSON"] = apps
			if _, err := LoadConfig(func(key string) string { return env[key] }); err == nil {
				t.Fatal("LoadConfig() error = nil, want invalid internal app configuration")
			}
		})
	}
}

func TestLoadConfigRequiresPostgresAndRedisURLs(t *testing.T) {
	t.Parallel()

	for key, invalid := range map[string]string{
		"DATABASE_URL": "mysql://localhost/gateway",
		"REDIS_URL":    "http://localhost:6379",
	} {
		t.Run(key, func(t *testing.T) {
			env := validEnvironmentMap()
			env[key] = invalid
			if _, err := LoadConfig(func(key string) string { return env[key] }); err == nil {
				t.Fatalf("LoadConfig() accepted %s=%q", key, invalid)
			}
		})
	}
}

func TestLoadConfigBuildsPostgresURLFromDiscreteSecretFields(t *testing.T) {
	t.Parallel()

	env := validEnvironmentMap()
	delete(env, "DATABASE_URL")
	env["PGHOST"] = "staging.cluster.example"
	env["PGPORT"] = "5432"
	env["PGDATABASE"] = "gateway"
	env["PGUSER"] = "gateway_admin"
	env["PGPASSWORD"] = "secret:/?# value"

	config, err := LoadConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	parsed, err := url.Parse(config.DatabaseURL)
	if err != nil {
		t.Fatalf("url.Parse(DatabaseURL) error = %v", err)
	}
	password, hasPassword := parsed.User.Password()
	if parsed.Scheme != "postgres" || parsed.Hostname() != env["PGHOST"] ||
		parsed.Port() != env["PGPORT"] || parsed.Path != "/"+env["PGDATABASE"] ||
		parsed.User.Username() != env["PGUSER"] || !hasPassword || password != env["PGPASSWORD"] ||
		parsed.Query().Get("sslmode") != "require" {
		t.Fatalf("DatabaseURL = %q", config.DatabaseURL)
	}
}

func TestLoadConfigRejectsIncompleteDiscretePostgresFields(t *testing.T) {
	t.Parallel()

	env := validEnvironmentMap()
	delete(env, "DATABASE_URL")
	env["PGHOST"] = "staging.cluster.example"
	if _, err := LoadConfig(func(key string) string { return env[key] }); err == nil ||
		!strings.Contains(err.Error(), "PGHOST, PGPORT, PGDATABASE, PGUSER, and PGPASSWORD") {
		t.Fatalf("LoadConfig() error = %v", err)
	}
}

func validEnvironmentMap() map[string]string {
	return map[string]string{
		"GATEWAY_ENVIRONMENT":                  "staging",
		"AWS_REGION":                           "ap-east-1",
		"AWS_IOT_DEVICE_POLICY_NAME":           "aws-iot-gateway-staging-device",
		"AWS_IOT_TOPIC_PREFIX":                 "aws-iot-gateway-staging",
		"AWS_IOT_CREDENTIAL_PROVIDER_ENDPOINT": "example.credentials.iot.ap-east-1.amazonaws.com",
		"AWS_IOT_ROLE_ALIAS":                   "aws-iot-gateway-staging-kvs",
		"AWS_IOT_SHADOW_NAME":                  "internal-aws",
		"AWS_IOT_INGEST_QUEUE_URL":             "https://sqs.ap-east-1.amazonaws.com/000000000000/aws-iot-gateway-staging-ingest",
		"AWS_KVS_CHANNEL_PREFIX":               "aws-iot-gateway-staging-",
		"AWS_S3_MEDIA_BUCKET":                  "aws-iot-gateway-staging-media-example",
		"AWS_S3_OTA_BUCKET":                    "aws-iot-gateway-staging-ota-example",
		"TIME_BOOTSTRAP_KEY_ID":                "time-staging-g1",
		"TIME_BOOTSTRAP_KMS_KEY_ID":            "arn:aws:kms:ap-east-1:000000000000:key/00000000-0000-0000-0000-000000000000",
		"DATABASE_URL":                         "postgres://gateway:password@postgres.example/gateway?sslmode=require",
		"REDIS_URL":                            "rediss://redis.example:6379/0",
		"INTERNAL_APPS_JSON": `[
			{"id":"livpet-staging","api_key":"ak_livpet_staging","api_secret":"livpet-api-secret","permissions":["ota:publish"],"webhook_url":"https://livpet.example/v1/internal/webhooks/explorex","webhook_secret":"livpet-webhook-secret","webhook_event_types":["feeding_done","motion_detected"]},
			{"id":"demo-staging","api_key":"ak_demo_staging","api_secret":"demo-api-secret","permissions":["ota:deploy"]}
		]`,
	}
}

func TestLoadConfigAllowsDirectACKSubscriberOnlyInDevelopment(t *testing.T) {
	t.Parallel()

	development := validEnvironmentMap()
	development["GATEWAY_ENVIRONMENT"] = "development"
	development["TIME_BOOTSTRAP_SIGNER"] = "local"
	development["TIME_BOOTSTRAP_LOCAL_PRIVATE_KEY_PATH"] = ".local/time-bootstrap/private.pem"
	delete(development, "TIME_BOOTSTRAP_KMS_KEY_ID")
	development["AWS_IOT_DIRECT_ACK_SUBSCRIBER_ENABLED"] = "true"
	config, err := LoadConfig(func(key string) string { return development[key] })
	if err != nil {
		t.Fatalf("LoadConfig() development error = %v", err)
	}
	if !config.DirectACKSubscriberEnabled {
		t.Fatal("DirectACKSubscriberEnabled = false, want true")
	}

	staging := validEnvironmentMap()
	staging["AWS_IOT_DIRECT_ACK_SUBSCRIBER_ENABLED"] = "true"
	if _, err := LoadConfig(func(key string) string { return staging[key] }); err == nil ||
		!strings.Contains(err.Error(), "development") {
		t.Fatalf("LoadConfig() staging error = %v, want development-only rejection", err)
	}

	invalid := validEnvironmentMap()
	invalid["GATEWAY_ENVIRONMENT"] = "development"
	invalid["AWS_IOT_DIRECT_ACK_SUBSCRIBER_ENABLED"] = "yes"
	if _, err := LoadConfig(func(key string) string { return invalid[key] }); err == nil ||
		!strings.Contains(err.Error(), "AWS_IOT_DIRECT_ACK_SUBSCRIBER_ENABLED") {
		t.Fatalf("LoadConfig() invalid boolean error = %v", err)
	}
}
