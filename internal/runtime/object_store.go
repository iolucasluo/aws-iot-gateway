package runtime

import (
	"fmt"
	"os"
	"strings"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/aliyunoss"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/provider"
)

const (
	objectCredentialRef = "env://GATEWAY_OBJECT_CREDENTIALS"
	objectBucketRef     = "env://GATEWAY_OBJECT_BUCKET"
)

func BuildObjectStore(composition provider.Composition, getenv func(string) string) (provider.ObjectStore, error) {
	instanceID := strings.TrimSpace(composition.Bindings["media_object"])
	if instanceID == "" {
		return nil, fmt.Errorf("media_object binding is required")
	}
	instance, ok := composition.Instances[instanceID]
	if !ok || instance.Capability != provider.CapabilityObject {
		return nil, fmt.Errorf("media_object binding references an invalid object instance")
	}
	if instance.Driver != "aliyun_oss" {
		return nil, fmt.Errorf("unsupported media_object driver %q", instance.Driver)
	}
	if instance.CredentialRef != objectCredentialRef {
		return nil, fmt.Errorf("aliyun_oss credential_ref must be %s", objectCredentialRef)
	}
	if instance.BucketRef != objectBucketRef {
		return nil, fmt.Errorf("aliyun_oss bucket_ref must be %s", objectBucketRef)
	}
	if getenv == nil {
		getenv = os.Getenv
	}
	publicEndpoint := strings.TrimSpace(getenv("GATEWAY_OBJECT_PUBLIC_ENDPOINT"))
	if publicEndpoint == "" {
		publicEndpoint = instance.Endpoint
	}
	store, err := aliyunoss.New(aliyunoss.Config{
		Region:          instance.Region,
		Endpoint:        instance.Endpoint,
		PublicEndpoint:  publicEndpoint,
		Bucket:          strings.TrimSpace(getenv("GATEWAY_OBJECT_BUCKET")),
		AccessKeyID:     strings.TrimSpace(getenv("GATEWAY_OBJECT_ACCESS_KEY_ID")),
		AccessKeySecret: strings.TrimSpace(getenv("GATEWAY_OBJECT_ACCESS_KEY_SECRET")),
	})
	if err != nil {
		return nil, fmt.Errorf("build aliyun_oss media object adapter: %w", err)
	}
	return store, nil
}
