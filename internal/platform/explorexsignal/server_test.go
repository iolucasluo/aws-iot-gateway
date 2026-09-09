package explorexsignal

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
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

func TestServerClosesSessionWhenEitherPeerDisconnects(t *testing.T) {
	for _, disconnectedRole := range []role{viewerRole, deviceRole} {
		t.Run(string(disconnectedRole), func(t *testing.T) {
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
			var session sessionResponse
			if response.StatusCode != http.StatusCreated || json.NewDecoder(response.Body).Decode(&session) != nil {
				t.Fatalf("create status = %d", response.StatusCode)
			}

			viewerURL := strings.Replace(session.ClientConfig["wss_url"].(string), "ws://signal.test", "ws"+strings.TrimPrefix(ts.URL, "http"), 1)
			deviceURL := strings.Replace(session.ClientConfig["device_wss_url"].(string), "ws://signal.test", "ws"+strings.TrimPrefix(ts.URL, "http"), 1)
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

			disconnected, counterpart := viewer, device
			if disconnectedRole == deviceRole {
				disconnected, counterpart = device, viewer
			}
			if err := disconnected.Close(); err != nil {
				t.Fatal(err)
			}

			deadline := time.Now().Add(time.Second)
			for {
				server.mu.Lock()
				_, exists := server.sessions[session.SessionID]
				server.mu.Unlock()
				if !exists {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("session remained active after %s disconnected", disconnectedRole)
				}
				time.Sleep(10 * time.Millisecond)
			}
			_ = counterpart.SetReadDeadline(time.Now().Add(time.Second))
			if _, _, err := counterpart.ReadMessage(); err == nil {
				t.Fatal("counterpart remained connected after session closed")
			}
		})
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

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func TestServerLogsRejectedMessageAndTeardown(t *testing.T) {
	var buf lockedBuffer
	server, err := NewServer(ServerConfig{
		AuthorizationToken: "gateway-secret",
		PublicBaseURL:      "http://signal.test",
		Logger:             log.New(&buf, "", 0),
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
	reqBody := `{"device_id":"device-1","binding_id":"binding-1","request_id":"request-1","requested_protocol":"explorex-signal-v1","capabilities":{"video_receive":true},"expires_at":"` + expiresAt + `"}`
	request, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/sessions", strings.NewReader(reqBody))
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
	var created sessionResponse
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	viewerURL := strings.Replace(created.ClientConfig["wss_url"].(string), "ws://signal.test", "ws"+strings.TrimPrefix(ts.URL, "http"), 1)
	viewer, _, err := websocket.DefaultDialer.Dial(viewerURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer viewer.Close()

	if err := viewer.WriteMessage(websocket.TextMessage, []byte(`{"type":"banana"}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := viewer.ReadMessage(); err == nil {
		t.Fatal("server kept the connection after an invalid message")
	}
	logged := buf.String()
	if !strings.Contains(logged, "rejected message") || !strings.Contains(logged, "torn down") {
		t.Fatalf("missing teardown logs: %q", logged)
	}
}
