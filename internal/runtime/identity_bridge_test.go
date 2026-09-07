package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/provider"
)

func TestNewIdentityRuntimeRequiresAProviderInstanceAndClient(t *testing.T) {
	t.Parallel()

	if _, err := NewIdentityRuntime("", &identityProviderStub{}); err == nil {
		t.Fatal("NewIdentityRuntime() accepted an empty provider instance")
	}
	if _, err := NewIdentityRuntime("identity.cn_staging", nil); err == nil {
		t.Fatal("NewIdentityRuntime() accepted a nil identity provider")
	}
}

func TestIdentityRuntimePrepareBindsAndValidatesProviderReference(t *testing.T) {
	t.Parallel()

	providerStub := &identityProviderStub{ref: validIdentityRef("identity.cn_staging")}
	runtime, err := NewIdentityRuntime("identity.cn_staging", providerStub)
	if err != nil {
		t.Fatalf("NewIdentityRuntime() error = %v", err)
	}
	request := provider.IdentityRequest{DeviceID: "fdr-001", PID: "feeder-x1", CSRPEM: "csr-pem"}
	ref, err := runtime.Prepare(context.Background(), request)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if providerStub.request != request || ref.ProviderInstance != "identity.cn_staging" {
		t.Fatalf("Prepare() request/ref = %#v / %#v", providerStub.request, ref)
	}

	providerStub.ref.ProviderInstance = "identity.other"
	if _, err := runtime.Prepare(context.Background(), request); !errors.Is(err, ErrIdentityProviderMismatch) {
		t.Fatalf("Prepare() error = %v, want provider mismatch", err)
	}
}

func TestIdentityRuntimeRejectsInvalidRequestAndReferenceBeforeDelegation(t *testing.T) {
	t.Parallel()

	providerStub := &identityProviderStub{ref: validIdentityRef("identity.cn_staging")}
	runtime, err := NewIdentityRuntime("identity.cn_staging", providerStub)
	if err != nil {
		t.Fatalf("NewIdentityRuntime() error = %v", err)
	}
	for _, request := range []provider.IdentityRequest{
		{DeviceID: "", PID: "feeder-x1", CSRPEM: "csr"},
		{DeviceID: "fdr-001", PID: "bad pid", CSRPEM: "csr"},
		{DeviceID: "fdr-001", PID: "feeder-x1", CSRPEM: ""},
	} {
		if _, err := runtime.Prepare(context.Background(), request); !errors.Is(err, ErrInvalidIdentityRequest) {
			t.Fatalf("Prepare(%#v) error = %v, want invalid request", request, err)
		}
	}
	if providerStub.prepareCalls != 0 {
		t.Fatalf("invalid requests delegated %d times", providerStub.prepareCalls)
	}

	providerStub.ref = validIdentityRef("identity.cn_staging")
	providerStub.ref.CertificatePEM = "not-a-certificate"
	if _, err := runtime.Prepare(context.Background(), provider.IdentityRequest{DeviceID: "fdr-001", PID: "feeder-x1", CSRPEM: "csr"}); !errors.Is(err, ErrInvalidIdentityRef) {
		t.Fatalf("Prepare() error = %v, want invalid ref", err)
	}
}

func TestIdentityRuntimeDelegatesLifecycleOnlyAfterReferenceValidation(t *testing.T) {
	t.Parallel()

	providerStub := &identityProviderStub{ref: validIdentityRef("identity.cn_staging")}
	runtime, err := NewIdentityRuntime("identity.cn_staging", providerStub)
	if err != nil {
		t.Fatalf("NewIdentityRuntime() error = %v", err)
	}
	ref := validIdentityRef("identity.cn_staging")
	if err := runtime.Activate(context.Background(), ref); err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	rotated, err := runtime.Rotate(context.Background(), ref)
	if err != nil {
		t.Fatalf("Rotate() error = %v", err)
	}
	if rotated.CertificateID != "cert-rotated" {
		t.Fatalf("Rotate() ref = %#v", rotated)
	}
	if err := runtime.Revoke(context.Background(), ref, "test"); err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}
	if providerStub.activateCalls != 1 || providerStub.rotateCalls != 1 || providerStub.revokeCalls != 1 {
		t.Fatalf("lifecycle calls = activate:%d rotate:%d revoke:%d", providerStub.activateCalls, providerStub.rotateCalls, providerStub.revokeCalls)
	}

	ref.ProviderInstance = "identity.other"
	if err := runtime.Activate(context.Background(), ref); !errors.Is(err, ErrIdentityProviderMismatch) {
		t.Fatalf("Activate() error = %v, want provider mismatch", err)
	}
}

func validIdentityRef(instance string) provider.IdentityRef {
	return provider.IdentityRef{
		ProviderInstance: instance,
		ExternalID:       "device-external-1",
		CertificateID:    "cert-1",
		CertificatePEM:   "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----",
		Endpoint:         "mqtts://broker.example:8883",
	}
}

type identityProviderStub struct {
	ref           provider.IdentityRef
	request       provider.IdentityRequest
	prepareErr    error
	prepareCalls  int
	activateCalls int
	rotateCalls   int
	revokeCalls   int
}

func (stub *identityProviderStub) Prepare(_ context.Context, request provider.IdentityRequest) (provider.IdentityRef, error) {
	stub.prepareCalls++
	stub.request = request
	if stub.prepareErr != nil {
		return provider.IdentityRef{}, stub.prepareErr
	}
	return stub.ref, nil
}

func (stub *identityProviderStub) Activate(_ context.Context, _ provider.IdentityRef) error {
	stub.activateCalls++
	return nil
}

func (stub *identityProviderStub) Rotate(_ context.Context, _ provider.IdentityRef) (provider.IdentityRef, error) {
	stub.rotateCalls++
	ref := stub.ref
	ref.CertificateID = "cert-rotated"
	return ref, nil
}

func (stub *identityProviderStub) Revoke(_ context.Context, _ provider.IdentityRef, reason string) error {
	stub.revokeCalls++
	if strings.TrimSpace(reason) == "" {
		return errors.New("reason is required")
	}
	return nil
}

func (stub *identityProviderStub) Health(context.Context) error { return nil }
