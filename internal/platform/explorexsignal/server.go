package explorexsignal

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/provider"
)

const (
	maxSessionRequestBytes = 32 << 10
	maxWebSocketBytes      = 64 << 10
	maxPendingMessages     = 16
	defaultSessionTTL      = 5 * time.Minute
	maxServerSessionTTL    = 15 * time.Minute
)

type ServerConfig struct {
	AuthorizationToken string
	PublicBaseURL      string
	Logger             *log.Logger
	ICEServers         []provider.ICEServer
	Now                func() time.Time
	NewID              func() (string, error)
	NewTicket          func() (string, error)
	MaxSessions        int
}

type Server struct {
	authorizationToken string
	publicBaseURL      string
	iceServers         []provider.ICEServer
	now                func() time.Time
	newID              func() (string, error)
	newTicket          func() (string, error)
	maxSessions        int
	logger             *log.Logger

	mu       sync.Mutex
	sessions map[string]*signalSession
	idem     map[string]string
}

type signalSession struct {
	descriptor  provider.RealtimeSessionDescriptor
	fingerprint [32]byte
	expiresAt   time.Time
	viewer      ticketState
	device      ticketState
	viewerConn  *signalPeer
	deviceConn  *signalPeer
	pending     map[role][][]byte
	closed      bool
}

type ticketState struct {
	hash     [32]byte
	consumed bool
}

type role string

const (
	viewerRole role = "viewer"
	deviceRole role = "device"
)

type signalPeer struct {
	role role
	conn *websocket.Conn
	mu   sync.Mutex
}

type sessionRequest struct {
	DeviceID          string         `json:"device_id"`
	BindingID         string         `json:"binding_id"`
	RequestID         string         `json:"request_id"`
	RequestedProtocol string         `json:"requested_protocol"`
	Capabilities      map[string]any `json:"capabilities"`
	ExpiresAt         string         `json:"expires_at"`
}

func NewServer(config ServerConfig) (*Server, error) {
	token := strings.TrimSpace(config.AuthorizationToken)
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return nil, fmt.Errorf("%w: authorization token is required", ErrInvalidConfig)
	}
	baseURL := strings.TrimRight(strings.TrimSpace(config.PublicBaseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Path != "" && parsed.Path != "/") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https" && parsed.Scheme != "ws" && parsed.Scheme != "wss") {
		return nil, fmt.Errorf("%w: public base URL must be an HTTP(S)/WS(S) origin", ErrInvalidConfig)
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	newID := config.NewID
	if newID == nil {
		newID = randomSessionID
	}
	newTicket := config.NewTicket
	if newTicket == nil {
		newTicket = randomOpaque
	}
	maxSessions := config.MaxSessions
	if maxSessions <= 0 {
		maxSessions = 256
	}
	return &Server{
		authorizationToken: token,
		publicBaseURL:      baseURL,
		iceServers:         append([]provider.ICEServer(nil), config.ICEServers...),
		now:                now,
		newID:              newID,
		newTicket:          newTicket,
		maxSessions:        maxSessions,
		logger:             config.Logger,
		sessions:           make(map[string]*signalSession),
		idem:               make(map[string]string),
	}, nil
}

func (server *Server) logf(format string, args ...any) {
	if server.logger != nil {
		server.logger.Printf(format, args...)
	}
}

func (server *Server) Handler() http.Handler {
	return http.HandlerFunc(server.ServeHTTP)
}

func (server *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodGet && request.URL.Path == "/healthz" {
		writeJSONResponse(writer, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/v1/sessions/") {
		parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
		if len(parts) == 4 && parts[0] == "v1" && parts[1] == "sessions" && parts[3] == "ws" {
			// WebSocket authorization is the short-lived, single-use ticket in
			// the URL. Do not require the Gateway bearer on native clients.
			server.serveWebSocket(writer, request, parts[2])
			return
		}
	}
	if !server.authorized(request) {
		writeJSONResponse(writer, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if request.Method == http.MethodPost && request.URL.Path == "/v1/sessions" {
		server.createSession(writer, request)
		return
	}
	if strings.HasPrefix(request.URL.Path, "/v1/sessions/") {
		parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
		if len(parts) == 4 && parts[3] == "renew" && safeIdentifier(parts[2]) && request.Method == http.MethodPost {
			server.renewSession(writer, request, parts[2])
			return
		}
		if len(parts) == 3 && parts[0] == "v1" && parts[1] == "sessions" && parts[2] != "" && request.Method == http.MethodDelete {
			server.closeSession(writer, parts[2])
			return
		}
	}
	writeJSONResponse(writer, http.StatusNotFound, map[string]string{"error": "not_found"})
}

func (server *Server) authorized(request *http.Request) bool {
	return request.Header.Get("Authorization") == "Bearer "+server.authorizationToken
}

func (server *Server) createSession(writer http.ResponseWriter, request *http.Request) {
	var input sessionRequest
	if err := decodeStrictJSON(request, maxSessionRequestBytes, &input); err != nil {
		writeJSONResponse(writer, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	if request.Header.Get("Idempotency-Key") != input.RequestID || !safeIdentifier(input.DeviceID) ||
		!safeIdentifier(input.BindingID) || !safeIdentifier(input.RequestID) || input.RequestedProtocol != Protocol ||
		input.Capabilities == nil {
		writeJSONResponse(writer, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, input.ExpiresAt)
	if err != nil {
		writeJSONResponse(writer, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	now := server.now().UTC()
	if !expiresAt.After(now) || expiresAt.Sub(now) > maxServerSessionTTL {
		writeJSONResponse(writer, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	fingerprint, _ := json.Marshal(input)
	digest := sha256.Sum256(fingerprint)

	server.mu.Lock()
	server.expireLocked(now)
	if existingID, ok := server.idem[input.RequestID]; ok {
		existing := server.sessions[existingID]
		if existing == nil || existing.fingerprint != digest {
			server.mu.Unlock()
			writeJSONResponse(writer, http.StatusConflict, map[string]string{"error": "idempotency_conflict"})
			return
		}
		descriptor := existing.descriptor
		server.mu.Unlock()
		writeJSONResponse(writer, http.StatusOK, descriptor)
		return
	}
	if len(server.sessions) >= server.maxSessions {
		server.mu.Unlock()
		writeJSONResponse(writer, http.StatusServiceUnavailable, map[string]string{"error": "capacity_exceeded"})
		return
	}
	sessionID, err := server.newID()
	if err != nil || !validOpaque(sessionID, 128) {
		server.mu.Unlock()
		writeJSONResponse(writer, http.StatusServiceUnavailable, map[string]string{"error": "unavailable"})
		return
	}
	viewerTicket, err := server.newTicket()
	if err != nil || !validOpaque(viewerTicket, 256) {
		server.mu.Unlock()
		writeJSONResponse(writer, http.StatusServiceUnavailable, map[string]string{"error": "unavailable"})
		return
	}
	deviceTicket, err := server.newTicket()
	if err != nil || !validOpaque(deviceTicket, 256) {
		server.mu.Unlock()
		writeJSONResponse(writer, http.StatusServiceUnavailable, map[string]string{"error": "unavailable"})
		return
	}
	viewerURL, viewerOK := server.publicWebSocketURL(sessionID, string(viewerRole), viewerTicket)
	deviceURL, deviceOK := server.publicWebSocketURL(sessionID, string(deviceRole), deviceTicket)
	if !viewerOK || !deviceOK {
		server.mu.Unlock()
		writeJSONResponse(writer, http.StatusServiceUnavailable, map[string]string{"error": "unavailable"})
		return
	}
	descriptor := provider.RealtimeSessionDescriptor{
		SessionID:        sessionID,
		ProviderInstance: "realtime.cn_staging",
		Protocol:         Protocol,
		ClientConfig: map[string]any{
			"wss_url":           viewerURL,
			"device_wss_url":    deviceURL,
			"device_config":     map[string]any{"wss_url": deviceURL},
			"ice_servers":       server.iceServers,
			"ticket_expires_at": expiresAt.Format(time.RFC3339Nano),
		},
		Capabilities: map[string]any{"protocols": []string{Protocol}},
		ExpiresAt:    expiresAt,
	}
	server.sessions[sessionID] = &signalSession{
		descriptor: descriptor, fingerprint: digest, expiresAt: expiresAt,
		viewer:  ticketState{hash: sha256.Sum256([]byte(viewerTicket))},
		device:  ticketState{hash: sha256.Sum256([]byte(deviceTicket))},
		pending: map[role][][]byte{viewerRole: nil, deviceRole: nil},
	}
	server.idem[input.RequestID] = sessionID
	server.mu.Unlock()
	writeJSONResponse(writer, http.StatusCreated, descriptor)
}

func (server *Server) closeSession(writer http.ResponseWriter, sessionID string) {
	server.mu.Lock()
	session := server.sessions[sessionID]
	if session == nil {
		server.mu.Unlock()
		writeJSONResponse(writer, http.StatusOK, map[string]any{"session_id": sessionID, "closed": true})
		return
	}
	session.closed = true
	viewer, device := session.viewerConn, session.deviceConn
	delete(server.sessions, sessionID)
	server.mu.Unlock()
	closePeer(viewer)
	closePeer(device)
	writeJSONResponse(writer, http.StatusOK, map[string]any{"session_id": sessionID, "closed": true})
}

func (server *Server) serveWebSocket(writer http.ResponseWriter, request *http.Request, sessionID string) {
	requestedRole := role(request.URL.Query().Get("role"))
	if requestedRole != viewerRole && requestedRole != deviceRole {
		writeJSONResponse(writer, http.StatusBadRequest, map[string]string{"error": "invalid_role"})
		return
	}
	ticket := request.URL.Query().Get("ticket")
	if ticket == "" || strings.ContainsAny(ticket, "\r\n") {
		writeJSONResponse(writer, http.StatusUnauthorized, map[string]string{"error": "invalid_ticket"})
		return
	}

	server.mu.Lock()
	now := server.now().UTC()
	session := server.sessions[sessionID]
	if session == nil || session.closed || !session.expiresAt.After(now) || !session.descriptor.ExpiresAt.After(now) {
		server.mu.Unlock()
		writeJSONResponse(writer, http.StatusUnauthorized, map[string]string{"error": "session_expired"})
		return
	}
	state := &session.viewer
	if requestedRole == deviceRole {
		state = &session.device
	}
	if state.consumed || state.hash != sha256.Sum256([]byte(ticket)) {
		server.mu.Unlock()
		writeJSONResponse(writer, http.StatusUnauthorized, map[string]string{"error": "invalid_ticket"})
		return
	}
	if (requestedRole == viewerRole && session.viewerConn != nil) || (requestedRole == deviceRole && session.deviceConn != nil) {
		server.mu.Unlock()
		writeJSONResponse(writer, http.StatusConflict, map[string]string{"error": "role_already_connected"})
		return
	}
	state.consumed = true
	server.mu.Unlock()

	upgrader := websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin:     func(*http.Request) bool { return true }, // native App/device clients have no stable browser origin
	}
	conn, err := upgrader.Upgrade(writer, request, nil)
	if err != nil {
		return
	}
	peer := &signalPeer{role: requestedRole, conn: conn}
	server.mu.Lock()
	if session.closed || !session.expiresAt.After(server.now().UTC()) || !session.descriptor.ExpiresAt.After(server.now().UTC()) {
		server.mu.Unlock()
		closePeer(peer)
		return
	}
	if requestedRole == viewerRole {
		session.viewerConn = peer
	} else {
		session.deviceConn = peer
	}
	pending := session.pending[requestedRole]
	session.pending[requestedRole] = nil
	server.mu.Unlock()
	for _, payload := range pending {
		if err := peer.write(payload); err != nil {
			closePeer(peer)
			return
		}
	}
	server.readPeer(sessionID, session, peer)
}

func (server *Server) readPeer(sessionID string, session *signalSession, peer *signalPeer) {
	defer func() {
		_ = peer.conn.Close()
		var counterpart *signalPeer
		server.mu.Lock()
		if current := server.sessions[sessionID]; current == session {
			session.closed = true
			if peer.role == viewerRole && session.viewerConn == peer {
				session.viewerConn = nil
				counterpart = session.deviceConn
				session.deviceConn = nil
			}
			if peer.role == deviceRole && session.deviceConn == peer {
				session.deviceConn = nil
				counterpart = session.viewerConn
				session.viewerConn = nil
			}
			delete(server.sessions, sessionID)
		}
		server.mu.Unlock()
		server.logf("explorexsignal: session=%s role=%s disconnected; session torn down", sessionID, peer.role)
		closePeer(counterpart)
	}()
	peer.conn.SetReadLimit(maxWebSocketBytes)
	for {
		messageType, payload, err := peer.conn.ReadMessage()
		if err != nil {
			server.logf("explorexsignal: session=%s role=%s read error: %v", sessionID, peer.role, err)
			return
		}
		if messageType != websocket.TextMessage || !validWireMessage(peer.role, payload) {
			server.logf("explorexsignal: session=%s role=%s rejected message type=%v len=%d", sessionID, peer.role, messageType, len(payload))
			return
		}
		server.relay(sessionID, session, peer.role, payload)
	}
}

func (server *Server) relay(sessionID string, session *signalSession, from role, payload []byte) {
	to := deviceRole
	if from == deviceRole {
		to = viewerRole
	}
	server.mu.Lock()
	if server.sessions[sessionID] != session || session.closed {
		server.mu.Unlock()
		return
	}
	target := session.deviceConn
	if to == viewerRole {
		target = session.viewerConn
	}
	if target == nil {
		queue := session.pending[to]
		if len(queue) >= maxPendingMessages {
			server.mu.Unlock()
			return
		}
		session.pending[to] = append(queue, append([]byte(nil), payload...))
		server.mu.Unlock()
		return
	}
	server.mu.Unlock()
	_ = target.write(payload)
}

func (server *Server) Run(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			server.mu.Lock()
			server.expireLocked(now.UTC())
			server.mu.Unlock()
		}
	}
}

func (server *Server) expireLocked(now time.Time) {
	for id, session := range server.sessions {
		if session.expiresAt.After(now) {
			continue
		}
		session.closed = true
		closePeer(session.viewerConn)
		closePeer(session.deviceConn)
		delete(server.sessions, id)
	}
}

func (server *Server) publicWebSocketURL(sessionID, requestedRole, ticket string) (string, bool) {
	base, err := url.Parse(server.publicBaseURL)
	if err != nil || base.Host == "" {
		return "", false
	}
	switch base.Scheme {
	case "http":
		base.Scheme = "ws"
	case "https":
		base.Scheme = "wss"
	case "ws", "wss":
	default:
		// A scheme-less base such as "rtc.example.test:80" parses the host as the
		// scheme, and any other scheme cannot carry a websocket upgrade at all.
		return "", false
	}
	// A viewer can never open a socket on port 0. A misconfigured base URL has to
	// fail session creation instead of silently handing the app an unusable address.
	if base.Port() == "0" {
		return "", false
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/v1/sessions/" + url.PathEscape(sessionID) + "/ws"
	query := base.Query()
	query.Set("role", requestedRole)
	query.Set("ticket", ticket)
	base.RawQuery = query.Encode()
	return base.String(), true
}

func (peer *signalPeer) write(payload []byte) error {
	peer.mu.Lock()
	defer peer.mu.Unlock()
	_ = peer.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return peer.conn.WriteMessage(websocket.TextMessage, payload)
}

func closePeer(peer *signalPeer) {
	if peer == nil {
		return
	}
	_ = peer.conn.Close()
}

func validWireMessage(peerRole role, payload []byte) bool {
	var fields map[string]json.RawMessage
	if len(payload) == 0 || len(payload) > maxWebSocketBytes || json.Unmarshal(payload, &fields) != nil {
		return false
	}
	typeValue, ok := fields["type"]
	if !ok {
		return false
	}
	var messageType string
	if json.Unmarshal(typeValue, &messageType) != nil {
		return false
	}
	switch messageType {
	case "offer", "answer":
		if (peerRole == viewerRole && messageType != "offer") || (peerRole == deviceRole && messageType != "answer") {
			return false
		}
		return validSDPMessage(fields, messageType)
	case "ice-candidate":
		if peerRole != viewerRole && peerRole != deviceRole {
			return false
		}
		return validNullableString(fields, "candidate") && validNullableString(fields, "sdp_mid") && validNullableInt(fields, "sdp_mline_index")
	case "close":
		return len(fields) == 1
	default:
		return false
	}
}

func validSDPMessage(fields map[string]json.RawMessage, messageType string) bool {
	if len(fields) != 3 {
		return false
	}
	var sdpType, sdp string
	if json.Unmarshal(fields["sdp_type"], &sdpType) != nil || sdpType != messageType || json.Unmarshal(fields["sdp"], &sdp) != nil {
		return false
	}
	return sdp != "" && len(sdp) <= maxWebSocketBytes
}

func validNullableString(fields map[string]json.RawMessage, name string) bool {
	raw, ok := fields[name]
	if !ok || string(raw) == "null" {
		return ok
	}
	var value string
	return json.Unmarshal(raw, &value) == nil && len(value) <= 4096
}

func validNullableInt(fields map[string]json.RawMessage, name string) bool {
	raw, ok := fields[name]
	if !ok || string(raw) == "null" {
		return ok
	}
	var value int
	return json.Unmarshal(raw, &value) == nil && value >= 0 && value <= 64
}

func decodeStrictJSON(request *http.Request, limit int64, target any) error {
	decoder := json.NewDecoder(io.LimitReader(request.Body, limit+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}

func writeJSONResponse(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func randomOpaque() (string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func randomSessionID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", value), nil
}

func validOpaque(value string, maxLen int) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= maxLen && !strings.ContainsAny(value, "\r\n")
}
