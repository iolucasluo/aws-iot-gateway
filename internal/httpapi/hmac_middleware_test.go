package httpapi

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/internalauth"
)

type hmacNonceStore struct{ used map[string]struct{} }

func (store *hmacNonceStore) Consume(_ context.Context, key string, _ time.Duration) (bool, error) {
	if _, found := store.used[key]; found {
		return false, nil
	}
	store.used[key] = struct{}{}
	return true, nil
}

func TestHMACMiddlewareAuthenticatesExactBodyAndInjectsInternalApp(t *testing.T) {
	t.Parallel()

	now := time.UnixMilli(1_718_000_000_000)
	verifier, err := internalauth.NewVerifier(
		[]internalauth.Credential{{InternalAppID: "livpet-staging", APIKey: "ak_livpet", APISecret: "livpet-secret"}},
		&hmacNonceStore{used: map[string]struct{}{}},
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatalf("NewVerifier() error = %v", err)
	}
	body := []byte(`{"device_sn":"fdr-1"}`)
	next := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		principal, ok := InternalAppPrincipal(request.Context())
		if !ok || principal.InternalAppID != "livpet-staging" {
			t.Fatalf("principal = %#v, ok = %t", principal, ok)
		}
		observed, readErr := io.ReadAll(request.Body)
		if readErr != nil || !bytes.Equal(observed, body) {
			t.Fatalf("restored body = %q, error = %v", observed, readErr)
		}
		writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
	})
	handler := HMACMiddleware(verifier, next)
	request := signedRequest(t, now, http.MethodPost, "/api/v1/openapi/devices/token", body)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
}

func TestHMACMiddlewareRejectsReplayAndUnknownKey(t *testing.T) {
	t.Parallel()

	now := time.UnixMilli(1_718_000_000_000)
	verifier, _ := internalauth.NewVerifier(
		[]internalauth.Credential{{InternalAppID: "livpet-staging", APIKey: "ak_livpet", APISecret: "livpet-secret"}},
		&hmacNonceStore{used: map[string]struct{}{}},
		func() time.Time { return now },
	)
	handler := HMACMiddleware(verifier, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	first := signedRequest(t, now, http.MethodGet, "/api/v1/openapi/devices/fdr-1", nil)
	firstResponse := httptest.NewRecorder()
	handler.ServeHTTP(firstResponse, first)
	if firstResponse.Code != http.StatusNoContent {
		t.Fatalf("first status = %d", firstResponse.Code)
	}
	replay := signedRequest(t, now, http.MethodGet, "/api/v1/openapi/devices/fdr-1", nil)
	replayResponse := httptest.NewRecorder()
	handler.ServeHTTP(replayResponse, replay)
	if replayResponse.Code != http.StatusConflict {
		t.Fatalf("replay status = %d, want 409: %s", replayResponse.Code, replayResponse.Body.String())
	}

	unknown := signedRequest(t, now, http.MethodGet, "/api/v1/openapi/devices/fdr-1", nil)
	unknown.Header.Set("X-Api-Key", "ak_unknown")
	unknownResponse := httptest.NewRecorder()
	handler.ServeHTTP(unknownResponse, unknown)
	if unknownResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unknown key status = %d, want 401", unknownResponse.Code)
	}
}

func signedRequest(t *testing.T, now time.Time, method, path string, body []byte) *http.Request {
	t.Helper()
	timestamp := fmt.Sprintf("0000001%013d", now.UnixMilli())
	nonce := "550e8400-e29b-41d4-a716-446655440000"
	signature, err := internalauth.Sign("livpet-secret", internalauth.SignatureRequest{
		APIKey: "ak_livpet", Timestamp: timestamp, Nonce: nonce,
		Method: method, Path: path, Body: body,
	})
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("X-Api-Key", "ak_livpet")
	request.Header.Set("X-Timestamp", timestamp)
	request.Header.Set("X-Nonce", nonce)
	request.Header.Set("X-Signature", signature)
	return request
}
