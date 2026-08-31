package provider

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	APIVersion = "explorex.io/v1"
	Kind       = "ProviderComposition"
)

var ErrInvalidComposition = errors.New("invalid provider composition")

var identifierPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

type Capability string

const (
	CapabilityIdentity  Capability = "identity"
	CapabilityMessaging Capability = "messaging"
	CapabilityTwin      Capability = "twin"
	CapabilityRealtime  Capability = "realtime"
	CapabilityObject    Capability = "object"
	CapabilityArtifact  Capability = "artifact"
	CapabilityQueue     Capability = "queue"
	CapabilitySigner    Capability = "signer"
)

type Metadata struct {
	ID          string `yaml:"id" json:"id"`
	Environment string `yaml:"environment" json:"environment"`
	Region      string `yaml:"region" json:"region"`
}

type Instance struct {
	Capability     Capability `yaml:"capability" json:"capability"`
	Driver         string     `yaml:"driver" json:"driver"`
	Region         string     `yaml:"region,omitempty" json:"region,omitempty"`
	Endpoint       string     `yaml:"endpoint,omitempty" json:"endpoint,omitempty"`
	CredentialRef  string     `yaml:"credential_ref,omitempty" json:"credential_ref,omitempty"`
	TrustAnchorRef string     `yaml:"trust_anchor_ref,omitempty" json:"trust_anchor_ref,omitempty"`
	BucketRef      string     `yaml:"bucket_ref,omitempty" json:"bucket_ref,omitempty"`
	KeyRef         string     `yaml:"key_ref,omitempty" json:"key_ref,omitempty"`
	Dependencies   []string   `yaml:"dependencies,omitempty" json:"dependencies,omitempty"`
}

type Policy struct {
	ForbidRegions      []string `yaml:"forbid_regions,omitempty" json:"forbid_regions,omitempty"`
	AllowMixedProvider bool     `yaml:"allow_mixed_provider" json:"allow_mixed_provider"`
}

type Composition struct {
	APIVersion string              `yaml:"api_version" json:"api_version"`
	Kind       string              `yaml:"kind" json:"kind"`
	Metadata   Metadata            `yaml:"metadata" json:"metadata"`
	Instances  map[string]Instance `yaml:"instances" json:"instances"`
	Bindings   map[string]string   `yaml:"bindings" json:"bindings"`
	Policy     Policy              `yaml:"policy" json:"policy"`
}

// LoadComposition decodes one strict YAML document and validates it before
// returning. Runtime values such as credentials and bucket names stay outside
// this public, reviewable composition file.
func LoadComposition(reader io.Reader) (Composition, error) {
	if reader == nil {
		return Composition{}, invalid("composition input is nil")
	}
	decoder := yaml.NewDecoder(reader)
	decoder.KnownFields(true)
	var composition Composition
	if err := decoder.Decode(&composition); err != nil {
		return Composition{}, invalid("decode composition: %v", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Composition{}, invalid("composition contains multiple documents")
		}
		return Composition{}, invalid("decode trailing composition document: %v", err)
	}
	if err := composition.Validate(); err != nil {
		return Composition{}, err
	}
	return composition, nil
}

func (composition Composition) Validate() error {
	if composition.APIVersion != APIVersion {
		return invalid("api_version must be %s", APIVersion)
	}
	if composition.Kind != Kind {
		return invalid("kind must be %s", Kind)
	}
	if !identifierPattern.MatchString(composition.Metadata.ID) ||
		!identifierPattern.MatchString(composition.Metadata.Environment) ||
		!identifierPattern.MatchString(composition.Metadata.Region) {
		return invalid("metadata id, environment, and region must be safe identifiers")
	}
	if len(composition.Instances) == 0 {
		return invalid("instances are required")
	}
	if len(composition.Bindings) == 0 {
		return invalid("bindings are required")
	}
	if err := validatePolicy(composition.Policy); err != nil {
		return err
	}

	providerFamilies := make(map[string]struct{})
	for instanceID, instance := range composition.Instances {
		if !identifierPattern.MatchString(instanceID) {
			return invalid("instance id %q is invalid", instanceID)
		}
		if !knownCapability(instance.Capability) {
			return invalid("instance %q has unknown capability %q", instanceID, instance.Capability)
		}
		if !identifierPattern.MatchString(instance.Driver) || !driverSupports(instance.Driver, instance.Capability) {
			return invalid("instance %q driver %q does not support capability %q", instanceID, instance.Driver, instance.Capability)
		}
		region := instance.Region
		if region == "" {
			region = composition.Metadata.Region
		}
		if !identifierPattern.MatchString(region) {
			return invalid("instance %q region is invalid", instanceID)
		}
		if forbiddenRegion(composition.Policy.ForbidRegions, region) ||
			(forbiddenRegion(composition.Policy.ForbidRegions, driverFamily(instance.Driver))) {
			return invalid("instance %q resolves to forbidden region/provider %q", instanceID, region)
		}
		if strings.HasPrefix(composition.Metadata.Region, "cn") && driverFamily(instance.Driver) == "aws" {
			return invalid("domestic composition cannot use AWS data-plane instance %q", instanceID)
		}
		if instance.Endpoint != "" {
			if err := validateEndpoint(instance.Endpoint); err != nil {
				return invalid("instance %q endpoint: %v", instanceID, err)
			}
		}
		for field, value := range map[string]string{
			"credential_ref": instance.CredentialRef, "trust_anchor_ref": instance.TrustAnchorRef,
			"bucket_ref": instance.BucketRef, "key_ref": instance.KeyRef,
		} {
			if value != "" && (strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n")) {
				return invalid("instance %q %s is invalid", instanceID, field)
			}
		}
		for _, dependency := range instance.Dependencies {
			if !identifierPattern.MatchString(dependency) {
				return invalid("instance %q dependency %q is invalid", instanceID, dependency)
			}
			if _, ok := composition.Instances[dependency]; !ok {
				return invalid("instance %q dependency %q is missing", instanceID, dependency)
			}
		}
		providerFamilies[driverFamily(instance.Driver)] = struct{}{}
	}
	if !composition.Policy.AllowMixedProvider && len(providerFamilies) > 1 {
		return invalid("composition mixes providers while policy disallows it")
	}

	for binding, instanceID := range composition.Bindings {
		expectedCapability, known := bindingCapabilities[binding]
		if !known {
			return invalid("binding %q is unknown", binding)
		}
		instance, exists := composition.Instances[instanceID]
		if !exists {
			return invalid("binding %q references missing instance %q", binding, instanceID)
		}
		if instance.Capability != expectedCapability {
			return invalid("binding %q requires capability %q", binding, expectedCapability)
		}
	}
	for _, binding := range requiredBindings {
		if strings.TrimSpace(composition.Bindings[binding]) == "" {
			return invalid("required binding %q is missing", binding)
		}
	}
	for instanceID, instance := range composition.Instances {
		if instance.Driver != "aws_kvs" {
			continue
		}
		identityDependency := false
		for _, dependency := range instance.Dependencies {
			if composition.Instances[dependency].Capability == CapabilityIdentity {
				identityDependency = true
				break
			}
		}
		if !identityDependency {
			return invalid("AWS KVS instance %q requires an identity dependency", instanceID)
		}
	}
	return nil
}

// Identity, messaging, and signing are the core device-plane capabilities.
// Realtime, object, artifact, twin, and queue bindings are optional and only
// become required when the corresponding product/runtime path is enabled.
var requiredBindings = []string{"identity", "messaging", "signer"}

var bindingCapabilities = map[string]Capability{
	"identity": CapabilityIdentity, "messaging": CapabilityMessaging, "twin": CapabilityTwin,
	"realtime": CapabilityRealtime, "media_object": CapabilityObject, "ota_artifact": CapabilityArtifact,
	"queue": CapabilityQueue, "signer": CapabilitySigner,
}

var driverCapabilities = map[string][]Capability{
	"aws_iot_identity": {CapabilityIdentity}, "ca_mqtt_identity": {CapabilityIdentity},
	"aws_iot_mqtt": {CapabilityMessaging}, "mqtt_broker": {CapabilityMessaging},
	"aws_shadow": {CapabilityTwin}, "broker_twin": {CapabilityTwin},
	"aws_kvs": {CapabilityRealtime}, "explorex_signal": {CapabilityRealtime},
	"aws_s3": {CapabilityObject, CapabilityArtifact}, "aliyun_oss": {CapabilityObject, CapabilityArtifact},
	"tencent_cos": {CapabilityObject, CapabilityArtifact},
	"aws_sqs":     {CapabilityQueue}, "queue_broker": {CapabilityQueue},
	"aws_kms": {CapabilitySigner}, "domestic_kms": {CapabilitySigner}, "local": {CapabilitySigner},
}

func knownCapability(capability Capability) bool {
	return capability == CapabilityIdentity || capability == CapabilityMessaging || capability == CapabilityTwin ||
		capability == CapabilityRealtime || capability == CapabilityObject || capability == CapabilityArtifact ||
		capability == CapabilityQueue || capability == CapabilitySigner
}

func driverSupports(driver string, capability Capability) bool {
	for _, supported := range driverCapabilities[driver] {
		if supported == capability {
			return true
		}
	}
	return false
}

func driverFamily(driver string) string {
	switch {
	case strings.HasPrefix(driver, "aws_"):
		return "aws"
	case strings.HasPrefix(driver, "aliyun_"):
		return "aliyun"
	case strings.HasPrefix(driver, "tencent_"):
		return "tencent"
	case strings.HasPrefix(driver, "ca_") || strings.HasPrefix(driver, "domestic_"):
		return "domestic"
	case strings.HasPrefix(driver, "mqtt_") || strings.HasPrefix(driver, "queue_"):
		return "selfhosted"
	case strings.HasPrefix(driver, "explorex_"):
		return "explorex"
	case driver == "local":
		return "local"
	default:
		return "unknown"
	}
}

func forbiddenRegion(forbidden []string, value string) bool {
	for _, item := range forbidden {
		if item == value {
			return true
		}
	}
	return false
}

func validatePolicy(policy Policy) error {
	seen := make(map[string]struct{}, len(policy.ForbidRegions))
	for _, region := range policy.ForbidRegions {
		if !identifierPattern.MatchString(region) {
			return invalid("forbid_regions contains invalid value %q", region)
		}
		if _, duplicate := seen[region]; duplicate {
			return invalid("forbid_regions contains duplicate value %q", region)
		}
		seen[region] = struct{}{}
	}
	return nil
}

func validateEndpoint(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return fmt.Errorf("must be an endpoint URL without userinfo or fragment")
	}
	return nil
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidComposition, fmt.Sprintf(format, args...))
}
