package internalauth

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

type credentialStoreStub struct {
	credentials map[string]Credential
	revoked     map[string]struct{}
	err         error
	calls       int
}

func (store *credentialStoreStub) FindCredentialByAPIKey(_ context.Context, apiKey string) (Credential, error) {
	store.calls++
	if store.err != nil {
		return Credential{}, store.err
	}
	if _, found := store.revoked[apiKey]; found {
		return Credential{}, ErrCredentialRevoked
	}
	credential, found := store.credentials[apiKey]
	if !found {
		return Credential{}, ErrUnknownAPIKey
	}
	return credential, nil
}

func signedRequest(t *testing.T, secret, apiKey string, now time.Time) SignatureRequest {
	t.Helper()
	request := SignatureRequest{
		APIKey:    apiKey,
		Timestamp: fmt.Sprintf("0000001%013d", now.UnixMilli()),
		Nonce:     "550e8400-e29b-41d4-a716-446655440010",
		Method:    "POST",
		Path:      "/api/v1/openapi/devices/list",
	}
	signature, err := Sign(secret, request)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	request.Signature = signature
	return request
}

func TestVerifierResolvesDatabaseCredentialsBeforeStaticOnes(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(1_718_000_000_000)
	store := &credentialStoreStub{credentials: map[string]Credential{
		"ak_project": {
			// DB 凭证的主体 id 是 project id，而不是凭证行 id。
			InternalAppID: "50000000-0000-0000-0000-000000000001",
			APIKey:        "ak_project",
			APISecret:     "project-api-secret",
			Permissions:   []string{"devices:read", "devices:operate"},
		},
	}}
	verifier, err := NewVerifier(
		[]Credential{{InternalAppID: "livpet-staging", APIKey: "ak_legacy", APISecret: "legacy-secret"}},
		&nonceStoreStub{consumed: map[string]struct{}{}},
		func() time.Time { return now },
		store,
	)
	if err != nil {
		t.Fatalf("NewVerifier() error = %v", err)
	}

	principal, err := verifier.Authenticate(context.Background(), signedRequest(t, "project-api-secret", "ak_project", now))
	if err != nil {
		t.Fatalf("Authenticate(project credential) error = %v", err)
	}
	if principal.InternalAppID != "50000000-0000-0000-0000-000000000001" {
		t.Fatalf("principal internal app id = %q, want the project id", principal.InternalAppID)
	}
	if !principal.HasPermission("devices:operate") || principal.HasPermission("ota:deploy") {
		t.Fatalf("principal permissions = %#v", principal.Permissions)
	}

	principal, err = verifier.Authenticate(context.Background(), signedRequest(t, "legacy-secret", "ak_legacy", now))
	if err != nil {
		t.Fatalf("Authenticate(static credential) error = %v", err)
	}
	if principal.InternalAppID != "livpet-staging" {
		t.Fatalf("principal internal app id = %q, want the static app id", principal.InternalAppID)
	}
	if store.calls != 2 {
		t.Fatalf("store calls = %d, want 2", store.calls)
	}
}

func TestVerifierFailsClosedWhenCredentialStoreErrors(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(1_718_000_000_000)
	store := &credentialStoreStub{err: errors.New("postgres unavailable")}
	verifier, err := NewVerifier(
		[]Credential{{InternalAppID: "livpet-staging", APIKey: "ak_legacy", APISecret: "legacy-secret"}},
		&nonceStoreStub{consumed: map[string]struct{}{}},
		func() time.Time { return now },
		store,
	)
	if err != nil {
		t.Fatalf("NewVerifier() error = %v", err)
	}
	_, err = verifier.Authenticate(context.Background(), signedRequest(t, "legacy-secret", "ak_legacy", now))
	if err == nil || errors.Is(err, ErrUnknownAPIKey) || errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("Authenticate() error = %v, want a store failure (no fallback to static credentials)", err)
	}
}

func TestVerifierRejectsUnusableCredentialsFromStores(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(1_718_000_000_000)
	for name, credential := range map[string]Credential{
		"missing-subject": {APIKey: "ak_broken", APISecret: "secret"},
		"missing-secret":  {InternalAppID: "project-1", APIKey: "ak_broken"},
	} {
		store := &credentialStoreStub{credentials: map[string]Credential{"ak_broken": credential}}
		verifier, err := NewVerifier(nil, &nonceStoreStub{consumed: map[string]struct{}{}}, func() time.Time { return now }, store)
		if err != nil {
			t.Fatalf("NewVerifier() error = %v", err)
		}
		if _, err := verifier.Authenticate(context.Background(), signedRequest(t, "secret", "ak_broken", now)); !errors.Is(err, ErrUnknownAPIKey) {
			t.Errorf("Authenticate(%s) error = %v, want ErrUnknownAPIKey", name, err)
		}
	}
}

func TestVerifierRequiresAtLeastOneCredentialSource(t *testing.T) {
	t.Parallel()
	if _, err := NewVerifier(nil, &nonceStoreStub{consumed: map[string]struct{}{}}, nil); err == nil {
		t.Fatal("NewVerifier() without any credential source was accepted")
	}
	if _, err := NewVerifier(nil, &nonceStoreStub{consumed: map[string]struct{}{}}, nil,
		&credentialStoreStub{credentials: map[string]Credential{}}); err != nil {
		t.Fatalf("NewVerifier() with only a credential store was rejected: %v", err)
	}
	if _, err := NewVerifier(nil, nil, nil, &credentialStoreStub{}); err == nil {
		t.Fatal("NewVerifier() without a nonce store was accepted")
	}
	if _, err := NewVerifier(nil, &nonceStoreStub{}, nil, nil); err == nil {
		t.Fatal("NewVerifier() with a nil credential store was accepted")
	}
}

func TestValidPermissionUsesTheSharedAllowList(t *testing.T) {
	t.Parallel()
	for _, permission := range Permissions {
		if !ValidPermission(permission) {
			t.Errorf("ValidPermission(%q) = false", permission)
		}
	}
	for _, permission := range []string{"", "devices:write", "ota:admin", "devices:read "} {
		if ValidPermission(permission) {
			t.Errorf("ValidPermission(%q) = true, want false", permission)
		}
	}
}
func TestVerifierDoesNotReviveRevokedDatabaseCredentialsFromStaticConfig(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(1_718_000_000_000)
	// 同一个 api_key 在 DB 里已禁用、在 INTERNAL_APPS_JSON 里仍然存在：
	// 必须以未知凭证拒绝，不能被静态配置复活。
	store := &credentialStoreStub{revoked: map[string]struct{}{"ak_shared": {}}}
	verifier, err := NewVerifier(
		[]Credential{{InternalAppID: "livpet-staging", APIKey: "ak_shared", APISecret: "shared-secret"}},
		&nonceStoreStub{consumed: map[string]struct{}{}},
		func() time.Time { return now },
		store,
	)
	if err != nil {
		t.Fatalf("NewVerifier() error = %v", err)
	}
	if _, err := verifier.Authenticate(context.Background(), signedRequest(t, "shared-secret", "ak_shared", now)); !errors.Is(err, ErrUnknownAPIKey) {
		t.Fatalf("Authenticate(revoked DB credential) error = %v, want ErrUnknownAPIKey", err)
	}
}
