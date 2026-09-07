package provider

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestLoadCompositionAcceptsMixedDomesticProvidersWithoutAWSDataPlane(t *testing.T) {
	composition, err := LoadComposition(strings.NewReader(validCompositionYAML))
	if err != nil {
		t.Fatalf("LoadComposition() error = %v", err)
	}
	if composition.Metadata.ID != "cn-staging" || composition.Bindings["media_object"] != "object.media_cn" {
		t.Fatalf("composition = %#v", composition)
	}
}

func TestLoadCompositionRejectsUnknownFields(t *testing.T) {
	unknown := strings.Replace(validCompositionYAML, "policy:\n", "unexpected: true\npolicy:\n", 1)
	if _, err := LoadComposition(strings.NewReader(unknown)); !errors.Is(err, ErrInvalidComposition) {
		t.Fatalf("LoadComposition() error = %v, want ErrInvalidComposition", err)
	}
}

func TestLoadCompositionRejectsDomesticAWSDataPlane(t *testing.T) {
	awsDataPlane := strings.Replace(validCompositionYAML, "driver: mqtt_broker", "driver: aws_iot_mqtt", 1)
	if _, err := LoadComposition(strings.NewReader(awsDataPlane)); !errors.Is(err, ErrInvalidComposition) {
		t.Fatalf("LoadComposition() error = %v, want domestic AWS rejection", err)
	}
}

func TestLoadCompositionRejectsAWSKVSMissingIdentityDependency(t *testing.T) {
	awsKVS := strings.Replace(validCompositionYAML, "driver: explorex_signal", "driver: aws_kvs", 1)
	if _, err := LoadComposition(strings.NewReader(awsKVS)); !errors.Is(err, ErrInvalidComposition) {
		t.Fatalf("LoadComposition() error = %v, want missing identity dependency", err)
	}
}

func TestLoadCompositionRejectsBindingCapabilityMismatch(t *testing.T) {
	mismatch := strings.Replace(validCompositionYAML, "media_object: object.media_cn", "media_object: object.ota_cn", 1)
	if _, err := LoadComposition(strings.NewReader(mismatch)); !errors.Is(err, ErrInvalidComposition) {
		t.Fatalf("LoadComposition() error = %v, want binding mismatch", err)
	}
}

func TestLoadCompositionRejectsMixedProvidersWhenPolicyDisallowsIt(t *testing.T) {
	singleProviderOnly := strings.Replace(validCompositionYAML, "allow_mixed_provider: true", "allow_mixed_provider: false", 1)
	if _, err := LoadComposition(strings.NewReader(singleProviderOnly)); !errors.Is(err, ErrInvalidComposition) {
		t.Fatalf("LoadComposition() error = %v, want mixed-provider rejection", err)
	}
}

func TestDomesticCompositionExampleIsLoadable(t *testing.T) {
	content, err := os.ReadFile("../../deploy/domestic-staging/composition.cn-staging.example.yaml")
	if err != nil {
		t.Fatalf("read domestic composition example: %v", err)
	}
	if _, err := LoadComposition(strings.NewReader(string(content))); err != nil {
		t.Fatalf("LoadComposition(domestic example) error = %v", err)
	}
}

func TestCompositionAllowsOptionalRealtimeAndStorageBindingsToBeOmitted(t *testing.T) {
	composition, err := LoadComposition(strings.NewReader(validCompositionYAML))
	if err != nil {
		t.Fatalf("LoadComposition() error = %v", err)
	}
	delete(composition.Bindings, "realtime")
	delete(composition.Bindings, "media_object")
	delete(composition.Bindings, "ota_artifact")
	if err := composition.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want optional bindings to be allowed", err)
	}
}

const validCompositionYAML = `api_version: explorex.io/v1
kind: ProviderComposition
metadata:
  id: cn-staging
  environment: staging
  region: cn
instances:
  identity.cn_staging:
    capability: identity
    driver: ca_mqtt_identity
    credential_ref: secret://cn-staging/identity
  messaging.cn_staging:
    capability: messaging
    driver: mqtt_broker
    endpoint: mqtts://mqtt.example.invalid:8883
    trust_anchor_ref: config://cn-staging/mqtt-ca
  realtime.cn_staging:
    capability: realtime
    driver: explorex_signal
    endpoint: https://rtc.example.invalid
  object.media_cn:
    capability: object
    driver: aliyun_oss
    bucket_ref: secret://cn-staging/media-bucket
  object.ota_cn:
    capability: artifact
    driver: tencent_cos
    bucket_ref: secret://cn-staging/ota-bucket
  signer.cn_staging:
    capability: signer
    driver: domestic_kms
    key_ref: secret://cn-staging/time-key
bindings:
  identity: identity.cn_staging
  messaging: messaging.cn_staging
  realtime: realtime.cn_staging
  media_object: object.media_cn
  ota_artifact: object.ota_cn
  signer: signer.cn_staging
policy:
  forbid_regions: [aws]
  allow_mixed_provider: true
`
