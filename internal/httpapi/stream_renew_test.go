package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/activation"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/binding"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/media"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/provider"
)

func TestStreamRenewRequiresDeviceToken(t *testing.T) {
	now := time.UnixMilli(1718000000000)
	handler := newSignedTestHandler(t, activation.NewService(), now, Dependencies{Binding: binding.NewService(&httpBindingRepository{}, nil, nil, nil)})
	req := signedInternalRequest(t, now, "nonce-renew-token-1", http.MethodPost, "/api/v1/openapi/devices/fdr-1/streams/session-1/renew", []byte(`{}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("renew without token=%d, want 401", response.Code)
	}
}

type httpRenewProvider struct{ calls int }

func (*httpRenewProvider) PrepareSession(context.Context, provider.RealtimeSessionRequest) (provider.RealtimeSessionDescriptor, error) {
	panic("renew must not prepare")
}
func (*httpRenewProvider) CloseSession(context.Context, provider.RealtimeSessionRef) error {
	return nil
}
func (*httpRenewProvider) Capabilities() provider.RealtimeCapabilities {
	return provider.RealtimeCapabilities{}
}
func (*httpRenewProvider) Health(context.Context) error { return nil }
func (p *httpRenewProvider) RenewSession(_ context.Context, _ provider.RealtimeSessionRef, deadline time.Time) (time.Time, error) {
	p.calls++
	return deadline, nil
}

type httpRenewRepository struct{ httpStreamRepository }

func (*httpRenewRepository) Find(_ context.Context, _, _, id string) (bool, error) {
	return id == "session-1", nil
}
func (*httpRenewRepository) Renew(_ context.Context, _, _, _ string, deadline time.Time) (time.Time, error) {
	return deadline, nil
}

func TestStreamRenewSuccessAndRejections(t *testing.T) {
	now := time.Now().UTC()
	repository := &httpBindingRepository{binding: binding.Binding{ID: "binding-1", DeviceID: "device-1", DeviceSN: "fdr-1", InternalAppID: "livpet-staging", Active: true}}
	authorization := binding.NewService(repository, func() (string, error) { return "test-device-token", nil }, func() time.Time { return now }, nil)
	if _, err := authorization.IssueToken(context.Background(), "livpet-staging", "fdr-1"); err != nil {
		t.Fatal(err)
	}
	realtime := &httpRenewProvider{}
	stream := media.NewRealtimeStreamService(realtime, "realtime.test", httpStreamCommander{}, &httpRenewRepository{}, nil, nil)
	handler := newSignedTestHandler(t, activation.NewService(), now, Dependencies{Binding: authorization, Media: stream})
	for i, tc := range []struct {
		body, token, id string
		status          int
	}{
		{`{}`, "test-device-token", "session-1", 200},
		{`null`, "test-device-token", "session-1", 400},
		{`{"expires_at":"2099-01-01T00:00:00Z"}`, "test-device-token", "session-1", 400},
		{`{}`, "wrong-token", "session-1", 403},
		{`{}`, "test-device-token", "other-session", 404},
	} {
		req := signedInternalRequest(t, now, fmt.Sprintf("renew-case-nonce-%d", i), http.MethodPost, "/api/v1/openapi/devices/fdr-1/streams/"+tc.id+"/renew", []byte(tc.body))
		req.Header.Set("X-Device-Token", tc.token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != tc.status {
			t.Fatalf("case %d status=%d body=%s", i, rec.Code, rec.Body.String())
		}
		if rec.Code == 200 {
			var value struct {
				StreamID  string    `json:"stream_id"`
				ExpiresAt time.Time `json:"expires_at"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &value); err != nil {
				t.Fatal(err)
			}
			if value.StreamID != tc.id || !value.ExpiresAt.After(now) || value.ExpiresAt.Sub(now) > 5*time.Minute+time.Second || rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("invalid lease response")
			}
		}
	}
	if realtime.calls != 1 {
		t.Fatalf("rejected requests reached provider: %d", realtime.calls)
	}
}
