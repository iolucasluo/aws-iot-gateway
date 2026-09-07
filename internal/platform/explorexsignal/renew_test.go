package explorexsignal

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/provider"
)

func TestRenewKeepsPeersButDoesNotExtendAdmissionTicket(t *testing.T) {
	now := time.Now().UTC()
	var clock atomic.Int64
	clock.Store(now.UnixNano())
	server, err := NewServer(ServerConfig{AuthorizationToken: "test-secret", PublicBaseURL: "http://signal.test", Now: func() time.Time { return time.Unix(0, clock.Load()).UTC() }})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()
	client, err := NewClient(Config{Endpoint: ts.URL, AuthorizationToken: "test-secret", Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := client.PrepareSession(context.Background(), provider.RealtimeSessionRequest{DeviceID: "device-1", BindingID: "binding-1", RequestID: "request-1", RequestedProtocol: Protocol, Capabilities: map[string]any{"video_receive": true}, ExpiresAt: now.Add(5 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	dial := func(raw string) *websocket.Conn {
		t.Helper()
		endpoint := strings.Replace(raw, "ws://signal.test", "ws"+strings.TrimPrefix(ts.URL, "http"), 1)
		conn, _, err := websocket.DefaultDialer.Dial(endpoint, nil)
		if err != nil {
			t.Fatal(err)
		}
		return conn
	}
	viewer := dial(descriptor.ClientConfig["wss_url"].(string))
	defer viewer.Close()
	device := dial(descriptor.ClientConfig["device_wss_url"].(string))
	defer device.Close()
	// Both HTTP admission operations have completed before advancing the clock.
	now = now.Add(4 * time.Minute)
	clock.Store(now.UnixNano())
	body := `{"expires_at":"` + now.Add(5*time.Minute).Format(time.RFC3339Nano) + `"}`
	renew := func(secret string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/sessions/"+descriptor.SessionID+"/renew", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+secret)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		return res.StatusCode
	}
	if got := renew("wrong"); got != 401 {
		t.Fatalf("unauthorized renew=%d", got)
	}
	if got := renew("test-secret"); got != 200 {
		t.Fatalf("renew=%d, want 200", got)
	}
	now = now.Add(2 * time.Minute)
	clock.Store(now.UnixNano())
	server.mu.Lock()
	server.expireLocked(now)
	server.mu.Unlock()
	if err := viewer.WriteMessage(websocket.TextMessage, []byte(`{"type":"offer","sdp_type":"offer","sdp":"v=0"}`)); err != nil {
		t.Fatal(err)
	}
	device.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := device.ReadMessage(); err != nil {
		t.Fatalf("renewed peer closed: %v", err)
	}
	server.mu.Lock()
	ticketDeadline := server.sessions[descriptor.SessionID].descriptor.ExpiresAt
	server.mu.Unlock()
	if !ticketDeadline.Equal(descriptor.ExpiresAt) {
		t.Fatal("renew extended admission descriptor")
	}
	now = now.Add(4 * time.Minute)
	clock.Store(now.UnixNano())
	body = `{"expires_at":"` + now.Add(5*time.Minute).Format(time.RFC3339Nano) + `"}`
	server.mu.Lock()
	server.expireLocked(now)
	server.mu.Unlock()
	if got := renew("test-secret"); got != 404 {
		t.Fatalf("expired session resurrected=%d", got)
	}
}

func TestRenewRejectsInvalidDeadline(t *testing.T) {
	server, _ := NewServer(ServerConfig{AuthorizationToken: "test-secret", PublicBaseURL: "http://signal.test"})
	for _, body := range []string{`{}`, `{"expires_at":"bad"}`, `{"expires_at":"2099-01-01T00:00:00Z"}`, `{"expires_at":"2020-01-01T00:00:00Z"}`, `{"unknown":true}`} {
		req := httptest.NewRequest(http.MethodPost, "/v1/sessions/session-1/renew", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer test-secret")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != 400 {
			t.Fatalf("invalid renew=%d", rec.Code)
		}
	}
}
