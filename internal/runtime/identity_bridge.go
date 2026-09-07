package runtime

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/provider"
)

var (
	ErrInvalidIdentityRequest     = errors.New("invalid identity request")
	ErrInvalidIdentityRef         = errors.New("invalid identity reference")
	ErrIdentityProviderMismatch   = errors.New("identity provider instance mismatch")
	ErrIdentityRuntimeUnavailable = errors.New("identity provider unavailable")
)

var (
	identityInstancePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	identityValuePattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,255}$`)
	identityPIDPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,63}$`)
)

// IdentityRuntime binds one provider-neutral identity client to the instance
// selected by composition. It validates requests and returned references at
// the boundary; the underlying provider remains responsible for its network
// protocol and credential handling.
type IdentityRuntime struct {
	instanceID string
	client     provider.DeviceIdentity
}

var _ provider.DeviceIdentity = (*IdentityRuntime)(nil)

func NewIdentityRuntime(instanceID string, client provider.DeviceIdentity) (*IdentityRuntime, error) {
	instanceID = strings.TrimSpace(instanceID)
	if !identityInstancePattern.MatchString(instanceID) {
		return nil, fmt.Errorf("%w: provider instance is invalid", ErrInvalidIdentityRequest)
	}
	if client == nil {
		return nil, fmt.Errorf("%w: provider client is required", ErrIdentityRuntimeUnavailable)
	}
	return &IdentityRuntime{instanceID: instanceID, client: client}, nil
}

func (runtime *IdentityRuntime) Prepare(ctx context.Context, request provider.IdentityRequest) (provider.IdentityRef, error) {
	if runtime == nil || runtime.client == nil {
		return provider.IdentityRef{}, ErrIdentityRuntimeUnavailable
	}
	if err := validateIdentityContext(ctx); err != nil {
		return provider.IdentityRef{}, err
	}
	if err := validateIdentityRequest(request); err != nil {
		return provider.IdentityRef{}, err
	}
	ref, err := runtime.client.Prepare(ctx, request)
	if err != nil {
		return provider.IdentityRef{}, fmt.Errorf("%w: prepare provider identity", ErrIdentityRuntimeUnavailable)
	}
	if err := runtime.validateReference(ref); err != nil {
		return provider.IdentityRef{}, err
	}
	return ref, nil
}

func (runtime *IdentityRuntime) Activate(ctx context.Context, ref provider.IdentityRef) error {
	if runtime == nil || runtime.client == nil {
		return ErrIdentityRuntimeUnavailable
	}
	if err := validateIdentityContext(ctx); err != nil {
		return err
	}
	if err := runtime.validateReference(ref); err != nil {
		return err
	}
	if err := runtime.client.Activate(ctx, ref); err != nil {
		return fmt.Errorf("%w: activate provider identity", ErrIdentityRuntimeUnavailable)
	}
	return nil
}

func (runtime *IdentityRuntime) Rotate(ctx context.Context, ref provider.IdentityRef) (provider.IdentityRef, error) {
	if runtime == nil || runtime.client == nil {
		return provider.IdentityRef{}, ErrIdentityRuntimeUnavailable
	}
	if err := validateIdentityContext(ctx); err != nil {
		return provider.IdentityRef{}, err
	}
	if err := runtime.validateReference(ref); err != nil {
		return provider.IdentityRef{}, err
	}
	rotated, err := runtime.client.Rotate(ctx, ref)
	if err != nil {
		return provider.IdentityRef{}, fmt.Errorf("%w: rotate provider identity", ErrIdentityRuntimeUnavailable)
	}
	if err := runtime.validateReference(rotated); err != nil {
		return provider.IdentityRef{}, err
	}
	return rotated, nil
}

func (runtime *IdentityRuntime) Revoke(ctx context.Context, ref provider.IdentityRef, reason string) error {
	if runtime == nil || runtime.client == nil {
		return ErrIdentityRuntimeUnavailable
	}
	if err := validateIdentityContext(ctx); err != nil {
		return err
	}
	if err := runtime.validateReference(ref); err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 128 || strings.ContainsAny(reason, "\r\n") {
		return fmt.Errorf("%w: revoke reason is invalid", ErrInvalidIdentityRequest)
	}
	if err := runtime.client.Revoke(ctx, ref, reason); err != nil {
		return fmt.Errorf("%w: revoke provider identity", ErrIdentityRuntimeUnavailable)
	}
	return nil
}

func (runtime *IdentityRuntime) Health(ctx context.Context) error {
	if runtime == nil || runtime.client == nil {
		return ErrIdentityRuntimeUnavailable
	}
	if err := validateIdentityContext(ctx); err != nil {
		return err
	}
	if err := runtime.client.Health(ctx); err != nil {
		return fmt.Errorf("%w: identity health check failed", ErrIdentityRuntimeUnavailable)
	}
	return nil
}

func (runtime *IdentityRuntime) validateReference(ref provider.IdentityRef) error {
	if runtime == nil || !identityInstancePattern.MatchString(runtime.instanceID) {
		return ErrIdentityRuntimeUnavailable
	}
	if ref.ProviderInstance != runtime.instanceID {
		return fmt.Errorf("%w: got %q", ErrIdentityProviderMismatch, ref.ProviderInstance)
	}
	if !identityValuePattern.MatchString(ref.ExternalID) ||
		!identityValuePattern.MatchString(ref.CertificateID) ||
		strings.TrimSpace(ref.CertificatePEM) == "" || len(ref.CertificatePEM) > 64<<10 ||
		!strings.Contains(ref.CertificatePEM, "-----BEGIN CERTIFICATE-----") ||
		!strings.Contains(ref.CertificatePEM, "-----END CERTIFICATE-----") ||
		strings.Contains(ref.CertificatePEM, "PRIVATE KEY") ||
		!validIdentityEndpoint(ref.Endpoint) {
		return ErrInvalidIdentityRef
	}
	return nil
}

func validateIdentityContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is required", ErrInvalidIdentityRequest)
	}
	return nil
}

func validateIdentityRequest(request provider.IdentityRequest) error {
	if !identityValuePattern.MatchString(request.DeviceID) ||
		!identityPIDPattern.MatchString(request.PID) ||
		strings.TrimSpace(request.CSRPEM) == "" || len(request.CSRPEM) > 16<<10 {
		return ErrInvalidIdentityRequest
	}
	return nil
}

func validIdentityEndpoint(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && parsed.Host != "" && (parsed.Scheme == "mqtts" || parsed.Scheme == "https") &&
		parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" && !strings.ContainsAny(raw, "\r\n")
}
