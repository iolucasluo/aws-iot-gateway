package runtime

import (
	"strings"
	"testing"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/provider"
)

func TestBuildObjectStoreBindsAliyunOSSFromComposition(t *testing.T) {
	t.Parallel()

	composition := provider.Composition{
		Bindings: map[string]string{"media_object": "object.cn_media"},
		Instances: map[string]provider.Instance{
			"object.cn_media": {
				Capability:    provider.CapabilityObject,
				Driver:        "aliyun_oss",
				Region:        "cn-shenzhen",
				Endpoint:      "https://oss-cn-shenzhen.aliyuncs.com",
				CredentialRef: "env://GATEWAY_OBJECT_CREDENTIALS",
				BucketRef:     "env://GATEWAY_OBJECT_BUCKET",
			},
		},
	}
	env := map[string]string{
		"GATEWAY_OBJECT_ACCESS_KEY_ID":     "test-access-key",
		"GATEWAY_OBJECT_ACCESS_KEY_SECRET": "test-access-secret",
		"GATEWAY_OBJECT_BUCKET":            "test-media-bucket",
	}

	store, err := BuildObjectStore(composition, func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("BuildObjectStore() error = %v", err)
	}
	if store == nil {
		t.Fatal("BuildObjectStore() returned nil store")
	}
}

func TestBuildObjectStoreRequiresExplicitMediaBinding(t *testing.T) {
	t.Parallel()

	_, err := BuildObjectStore(provider.Composition{}, func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "media_object binding") {
		t.Fatalf("BuildObjectStore() error = %v, want missing binding failure", err)
	}
}

func TestBuildObjectStoreRejectsUnknownCredentialReference(t *testing.T) {
	t.Parallel()

	composition := provider.Composition{
		Bindings: map[string]string{"media_object": "object.cn_media"},
		Instances: map[string]provider.Instance{
			"object.cn_media": {
				Capability:    provider.CapabilityObject,
				Driver:        "aliyun_oss",
				Region:        "cn-shenzhen",
				Endpoint:      "https://oss-cn-shenzhen.aliyuncs.com",
				CredentialRef: "secret://implicit-global-key",
				BucketRef:     "env://GATEWAY_OBJECT_BUCKET",
			},
		},
	}

	_, err := BuildObjectStore(composition, func(string) string { return "configured" })
	if err == nil || !strings.Contains(err.Error(), "credential_ref") {
		t.Fatalf("BuildObjectStore() error = %v, want credential_ref failure", err)
	}
}
