package explorexsignal

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/provider"
)

func TestClientPrepareSessionParsesProviderOwnedDescriptor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/sessions" {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Idempotency-Key") != "req-1" {
			t.Fatalf("missing idempotency key: %q", request.Header.Get("Idempotency-Key"))
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"session_id":"session-1","provider_instance":"realtime.cn_staging","protocol":"explorex-signal-v1","client_config":{"wss_url":"wss://rtc.example.test/v1/sessions/session-1?ticket=one-time","ice_servers":[{"urls":["turn:rtc.example.test:3478"],"username":"u","credential":"c"}]},"capabilities":{"protocols":["explorex-signal-v1"],"data_channel":true,"audio_send":true,"audio_receive":true},"expires_at":"2030-01-01T00:05:00Z"}`))
	}))
	defer server.Close()

	client, err := NewClient(Config{Endpoint: server.URL, HTTPClient: server.Client(), Now: func() time.Time {
		return time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	}})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := client.PrepareSession(context.Background(), provider.RealtimeSessionRequest{
		DeviceID: "device-1", BindingID: "binding-1", RequestID: "req-1",
		RequestedProtocol: Protocol, Capabilities: map[string]any{"video_receive": true},
		ExpiresAt: time.Date(2030, 1, 1, 0, 5, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.SessionID != "session-1" || descriptor.ProviderInstance != "realtime.cn_staging" || descriptor.Protocol != Protocol {
		t.Fatalf("unexpected descriptor identity: %#v", descriptor)
	}
	if descriptor.ClientConfig["wss_url"] != "wss://rtc.example.test/v1/sessions/session-1?ticket=one-time" {
		t.Fatalf("unexpected provider config: %#v", descriptor.ClientConfig)
	}
}

func TestClientRejectsUnknownResponseFieldsAndExpiredSession(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "unknown field", body: `{"session_id":"s","provider_instance":"realtime.cn_staging","protocol":"explorex-signal-v1","client_config":{},"capabilities":{},"expires_at":"2030-01-01T00:05:00Z","extra":true}`},
		{name: "expired", body: `{"session_id":"s","provider_instance":"realtime.cn_staging","protocol":"explorex-signal-v1","client_config":{},"capabilities":{},"expires_at":"2029-12-31T23:59:59Z"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(test.body))
			}))
			defer server.Close()
			client, err := NewClient(Config{Endpoint: server.URL, HTTPClient: server.Client(), Now: func() time.Time {
				return time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
			}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.PrepareSession(context.Background(), provider.RealtimeSessionRequest{
				DeviceID: "device-1", BindingID: "binding-1", RequestID: "req-1",
				RequestedProtocol: Protocol, Capabilities: map[string]any{},
				ExpiresAt: time.Date(2030, 1, 1, 0, 5, 0, 0, time.UTC),
			})
			if err == nil {
				t.Fatal("expected descriptor rejection")
			}
		})
	}
}

func TestClientCloseSessionUsesProviderIdentity(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotPath = request.URL.Path
		if request.Method != http.MethodDelete {
			t.Fatalf("unexpected method: %s", request.Method)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client, err := NewClient(Config{Endpoint: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CloseSession(context.Background(), provider.RealtimeSessionRef{ID: "session-1", ProviderInstance: "realtime.cn_staging"}); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/sessions/session-1" {
		t.Fatalf("unexpected close path: %q", gotPath)
	}
}

func TestNewClientRejectsUnsafeEndpoint(t *testing.T) {
	for _, endpoint := range []string{"", "https://user:pass@example.test", "https://example.test/path?token=secret", "ftp://example.test"} {
		if _, err := NewClient(Config{Endpoint: strings.TrimSpace(endpoint)}); err == nil {
			t.Fatalf("expected endpoint rejection for %q", endpoint)
		}
	}
}
