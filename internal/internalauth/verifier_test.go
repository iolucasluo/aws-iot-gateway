package internalauth

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

type nonceStoreStub struct {
	consumed map[string]struct{}
	err      error
}

func (store *nonceStoreStub) Consume(_ context.Context, key string, _ time.Duration) (bool, error) {
	if store.err != nil {
		return false, store.err
	}
	if _, found := store.consumed[key]; found {
		return false, nil
	}
	store.consumed[key] = struct{}{}
	return true, nil
}

func TestVerifierAuthenticatesInternalAppAndConsumesNonce(t *testing.T) {
	t.Parallel()

	now := time.UnixMilli(1_718_000_000_000)
	store := &nonceStoreStub{consumed: map[string]struct{}{}}
	verifier, err := NewVerifier([]Credential{{
		InternalAppID: "livpet-staging",
		APIKey:        "ak_livpet_staging",
		APISecret:     "livpet-api-secret",
	}}, store, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewVerifier() error = %v", err)
	}
	request := SignatureRequest{
		APIKey:    "ak_livpet_staging",
		Timestamp: fmt.Sprintf("0000001%013d", now.UnixMilli()),
		Nonce:     "550e8400-e29b-41d4-a716-446655440000",
		Method:    "POST",
		Path:      "/api/v1/openapi/devices/token",
		Body:      []byte(`{"device_sn":"device-1"}`),
	}
	request.Signature, err = Sign("livpet-api-secret", request)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}

	principal, err := verifier.Authenticate(context.Background(), request)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if principal.InternalAppID != "livpet-staging" || principal.APIKey != "ak_livpet_staging" {
		t.Fatalf("principal = %#v", principal)
	}
	if _, err := verifier.Authenticate(context.Background(), request); !errors.Is(err, ErrNonceReplayed) {
		t.Fatalf("replayed Authenticate() error = %v, want ErrNonceReplayed", err)
	}
}

func TestVerifierFailsClosedForTimeSignatureAndNonceStore(t *testing.T) {
	t.Parallel()

	now := time.UnixMilli(1_718_000_000_000)
	credential := Credential{InternalAppID: "livpet-staging", APIKey: "ak_livpet_staging", APISecret: "livpet-api-secret"}
	base := SignatureRequest{
		APIKey:    credential.APIKey,
		Timestamp: fmt.Sprintf("0000001%013d", now.UnixMilli()),
		Nonce:     "550e8400-e29b-41d4-a716-446655440000",
		Method:    "GET",
		Path:      "/api/v1/openapi/devices/list",
	}
	base.Signature, _ = Sign(credential.APISecret, base)

	t.Run("expired timestamp", func(t *testing.T) {
		verifier, _ := NewVerifier([]Credential{credential}, &nonceStoreStub{consumed: map[string]struct{}{}}, func() time.Time {
			return now.Add(6 * time.Minute)
		})
		if _, err := verifier.Authenticate(context.Background(), base); !errors.Is(err, ErrTimestampOutsideWindow) {
			t.Fatalf("Authenticate() error = %v", err)
		}
	})

	t.Run("wrong signature", func(t *testing.T) {
		verifier, _ := NewVerifier([]Credential{credential}, &nonceStoreStub{consumed: map[string]struct{}{}}, func() time.Time { return now })
		request := base
		request.Signature = "0000000000000000000000000000000000000000000000000000000000000000"
		if _, err := verifier.Authenticate(context.Background(), request); !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("Authenticate() error = %v", err)
		}
	})

	t.Run("nonce store unavailable", func(t *testing.T) {
		verifier, _ := NewVerifier([]Credential{credential}, &nonceStoreStub{err: errors.New("redis unavailable")}, func() time.Time { return now })
		if _, err := verifier.Authenticate(context.Background(), base); !errors.Is(err, ErrReplayProtectionUnavailable) {
			t.Fatalf("Authenticate() error = %v", err)
		}
	})
}
