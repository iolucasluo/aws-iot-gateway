package binding

import (
	"context"
	"errors"
	"testing"
	"time"
)

type repositoryStub struct {
	binding  Binding
	stored   DeviceToken
	released bool
}

func (repository *repositoryStub) FindActiveBinding(_ context.Context, internalAppID, deviceSN string) (Binding, error) {
	if repository.binding.InternalAppID != internalAppID || repository.binding.DeviceSN != deviceSN || !repository.binding.Active {
		return Binding{}, ErrBindingNotFound
	}
	return repository.binding, nil
}

func (repository *repositoryStub) StoreToken(_ context.Context, token DeviceToken) error {
	repository.stored = token
	return nil
}

func (repository *repositoryStub) ValidateToken(_ context.Context, internalAppID string, tokenHash [32]byte, deviceSN string) (Binding, error) {
	if repository.stored.TokenHash != tokenHash || repository.stored.BindingID != repository.binding.ID ||
		repository.binding.InternalAppID != internalAppID || repository.binding.DeviceSN != deviceSN || repository.released {
		return Binding{}, ErrTokenNotFound
	}
	return repository.binding, nil
}

func (repository *repositoryStub) FindBindingByToken(_ context.Context, tokenHash [32]byte) (Binding, error) {
	if repository.stored.TokenHash != tokenHash || repository.released {
		return Binding{}, ErrTokenNotFound
	}
	return repository.binding, nil
}

func (repository *repositoryStub) ReleaseBinding(_ context.Context, bindingID, _ string) (Binding, error) {
	if repository.binding.ID != bindingID || repository.released {
		return Binding{}, ErrBindingNotFound
	}
	repository.released = true
	return repository.binding, nil
}

type recommissionerStub struct{ err error }

func (recommissioner recommissionerStub) ResetRuntime(context.Context, Binding) error {
	return recommissioner.err
}

func TestIssueTokenStoresHashAndAuthorizesOnlyOwningApp(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	repository := &repositoryStub{binding: testBinding()}
	service := NewService(repository, func() (string, error) { return "plain-device-token", nil }, func() time.Time { return now }, nil)
	issued, err := service.IssueToken(context.Background(), "livpet-staging", repository.binding.DeviceSN)
	if err != nil {
		t.Fatalf("IssueToken() error = %v", err)
	}
	if issued.DeviceToken != "plain-device-token" || issued.TokenType != "DeviceToken" || issued.ExpiresIn != 86400 {
		t.Fatalf("IssueToken() = %#v", issued)
	}
	if repository.stored.TokenHash != HashToken(issued.DeviceToken) || repository.stored.ExpiresAt != now.Add(24*time.Hour) {
		t.Fatalf("stored token = %#v", repository.stored)
	}
	if _, err := service.AuthorizeDevice(context.Background(), "livpet-staging", issued.DeviceToken, repository.binding.DeviceSN); err != nil {
		t.Fatalf("AuthorizeDevice() owner error = %v", err)
	}
	if _, err := service.AuthorizeDevice(context.Background(), "demo-staging", issued.DeviceToken, repository.binding.DeviceSN); !errors.Is(err, ErrForbidden) {
		t.Fatalf("AuthorizeDevice() cross-app error = %v", err)
	}
}

func TestUnbindImmediatelyReleasesBinding(t *testing.T) {
	t.Parallel()

	repository := &repositoryStub{binding: testBinding()}
	service := NewService(repository, func() (string, error) { return "plain-device-token", nil }, time.Now, nil)
	issued, err := service.IssueToken(context.Background(), "livpet-staging", repository.binding.DeviceSN)
	if err != nil {
		t.Fatalf("IssueToken() error = %v", err)
	}
	released, err := service.Unbind(context.Background(), "livpet-staging", issued.DeviceToken)
	if err != nil {
		t.Fatalf("Unbind() error = %v", err)
	}
	if !repository.released || released.DeviceSN != repository.binding.DeviceSN {
		t.Fatalf("released = %#v, repository.released = %t", released, repository.released)
	}
	if _, err := service.AuthorizeDevice(context.Background(), "livpet-staging", issued.DeviceToken, repository.binding.DeviceSN); !errors.Is(err, ErrForbidden) {
		t.Fatalf("old token remains usable: %v", err)
	}
}

func TestRecommissionKeepsBindingUntilRuntimeResetACK(t *testing.T) {
	t.Parallel()

	repository := &repositoryStub{binding: testBinding()}
	service := NewService(repository, func() (string, error) { return "plain-device-token", nil }, time.Now,
		recommissionerStub{err: errors.New("ack timeout")})
	issued, err := service.IssueToken(context.Background(), "livpet-staging", repository.binding.DeviceSN)
	if err != nil {
		t.Fatalf("IssueToken() error = %v", err)
	}
	if _, err := service.Recommission(context.Background(), "livpet-staging", issued.DeviceToken, "runtime"); !errors.Is(err, ErrRecommissionUnavailable) {
		t.Fatalf("Recommission() error = %v", err)
	}
	if repository.released {
		t.Fatal("binding was released without device ACK")
	}

	service = NewService(repository, func() (string, error) { return "unused", nil }, time.Now, recommissionerStub{})
	if _, err := service.Recommission(context.Background(), "livpet-staging", issued.DeviceToken, "runtime"); err != nil {
		t.Fatalf("Recommission() after ACK error = %v", err)
	}
	if !repository.released {
		t.Fatal("binding was not released after device ACK")
	}
}

func testBinding() Binding {
	return Binding{
		ID: "018f0000-0000-7000-8000-000000000002", DeviceID: "018f0000-0000-7000-8000-000000000001",
		DeviceSN: "fdr-00000000000000000000000000000001", InternalAppID: "livpet-staging",
		BindingExternalUserID: "user-42", CertificateID: "certificate-1", Active: true,
	}
}
