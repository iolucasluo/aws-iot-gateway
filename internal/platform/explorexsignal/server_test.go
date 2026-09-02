package explorexsignal

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestServerCreatesIdempotentSessionAndRelaysSingleUseTickets(t *testing.T) {
	server, err := NewServer(ServerConfig{
		AuthorizationToken: "gateway-secret",
		PublicBaseURL:      "http://signal.test",
		Now:                func() time.Time { return time.Unix(1_800_000_000, 0).UTC() },
		NewID:              func() (string, error) { return "session-1", nil },
		NewTicket:          func() (string, error) { return "ticket-1", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	expiresAt := time.Unix(1_800_000_300, 0).UTC().Format(time.RFC3339)
	body := `{"device_id":"device-1","binding_id":"binding-1","request_id":"request-1","requested_protocol":"explorex-signal-v1","capabilities":{"video_receive":true},"expires_at":"` + expiresAt + `"}`
	request, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/sessions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer gateway-secret")
	request.Header.Set("Idempotency-Key", "request-1")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", response.StatusCode)
	}
	var first sessionResponse
	if err := json.NewDecoder(response.Body).Decode(&first); err != nil {
		t.Fatal(err)
	}
	if first.SessionID != "session-1" || first.Protocol != Protocol || first.ClientConfig["wss_url"] == nil {
		t.Fatalf("response = %#v", first)
	}

	request, err = http.NewRequest(http.MethodPost, ts.URL+"/v1/sessions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer gateway-secret")
	request.Header.Set("Idempotency-Key", "request-1")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("idempotent status = %d", response.StatusCode)
	}
	var second sessionResponse
	if err := json.NewDecoder(response.Body).Decode(&second); err != nil {
		t.Fatal(err)
	}
	if second.SessionID != first.SessionID || second.ClientConfig["wss_url"] != first.ClientConfig["wss_url"] {
		t.Fatalf("idempotent response changed: first=%#v second=%#v", first, second)
	}

	viewerURL := strings.Replace(first.ClientConfig["wss_url"].(string), "ws://signal.test", "ws"+strings.TrimPrefix(ts.URL, "http"), 1)
	deviceURL := strings.Replace(first.ClientConfig["device_wss_url"].(string), "ws://signal.test", "ws"+strings.TrimPrefix(ts.URL, "http"), 1)
	viewer, _, err := websocket.DefaultDialer.Dial(viewerURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer viewer.Close()
	device, _, err := websocket.DefaultDialer.Dial(deviceURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer device.Close()

	offer := `{"type":"offer","sdp_type":"offer","sdp":"v=0"}`
	if err := viewer.WriteMessage(websocket.TextMessage, []byte(offer)); err != nil {
		t.Fatal(err)
	}
	if _, relayed, err := device.ReadMessage(); err != nil || string(relayed) != offer {
		t.Fatalf("relayed offer = %q, err=%v", relayed, err)
	}

	if _, _, err := websocket.DefaultDialer.Dial(viewerURL, nil); err == nil {
		t.Fatal("replayed viewer ticket unexpectedly connected")
	}
}

func TestServerRejectsInvalidAuthAndIdempotency(t *testing.T) {
	server, err := NewServer(ServerConfig{AuthorizationToken: "gateway-secret", PublicBaseURL: "http://signal.test"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	request, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/sessions", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Idempotency-Key", "request-1")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing auth status = %d", response.StatusCode)
	}

	request, err = http.NewRequest(http.MethodPost, ts.URL+"/v1/sessions", strings.NewReader(`{"device_id":"device-1","binding_id":"binding-1","request_id":"request-1","requested_protocol":"explorex-signal-v1","capabilities":{},"expires_at":"2030-01-01T00:00:00Z","unknown":true}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer gateway-secret")
	request.Header.Set("Idempotency-Key", "request-2")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid request status = %d", response.StatusCode)
	}
}

func TestWebSocketURLHasOnlyOpaqueTicketQuery(t *testing.T) {
	server, err := NewServer(ServerConfig{AuthorizationToken: "gateway-secret", PublicBaseURL: "https://rtc.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	config, ok := server.publicWebSocketURL("session-1", "viewer", "ticket-value")
	if !ok {
		t.Fatal("public websocket URL rejected")
	}
	parsed, err := url.Parse(config)
	if err != nil || parsed.Scheme != "wss" || parsed.Query().Get("role") != "viewer" || parsed.Query().Get("ticket") != "ticket-value" {
		t.Fatalf("url = %q, err=%v", config, err)
	}
}
