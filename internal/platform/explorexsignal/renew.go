package explorexsignal

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/provider"
)

const maxRenewalTTL = 5 * time.Minute

type renewal struct {
	ExpiresAt time.Time `json:"expires_at"`
}

func (server *Server) renewSession(writer http.ResponseWriter, request *http.Request, id string) {
	data, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, 1024))
	var input renewal
	now := server.now().UTC()
	if err != nil || decodeStrict(data, &input) != nil || !input.ExpiresAt.After(now) || input.ExpiresAt.Sub(now) > maxRenewalTTL {
		writeJSONResponse(writer, http.StatusBadRequest, map[string]string{"error": "invalid_lease"})
		return
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	session := server.sessions[id]
	if session == nil || session.closed || !session.expiresAt.After(now) {
		writeJSONResponse(writer, http.StatusNotFound, map[string]string{"error": "session_expired"})
		return
	}
	// Renewal only preserves an established pair, never a pending admission.
	if session.viewerConn == nil || session.deviceConn == nil {
		writeJSONResponse(writer, http.StatusConflict, map[string]string{"error": "session_not_connected"})
		return
	}
	if input.ExpiresAt.After(session.expiresAt) {
		session.expiresAt = input.ExpiresAt.UTC()
	}
	// descriptor.ExpiresAt remains the original, immutable ticket deadline.
	writeJSONResponse(writer, http.StatusOK, renewal{ExpiresAt: session.expiresAt})
}

func (client *Client) RenewSession(ctx context.Context, session provider.RealtimeSessionRef, expiresAt time.Time) (time.Time, error) {
	if client == nil || ctx == nil || !safeIdentifier(session.ID) || !safeIdentifier(session.ProviderInstance) {
		return time.Time{}, ErrInvalidRequest
	}
	now := client.now().UTC()
	if !expiresAt.After(now) || expiresAt.Sub(now) > maxRenewalTTL {
		return time.Time{}, ErrInvalidRequest
	}
	body, err := json.Marshal(renewal{ExpiresAt: expiresAt.UTC()})
	if err != nil {
		return time.Time{}, ErrInvalidRequest
	}
	data, err := client.doJSON(ctx, http.MethodPost, sessionPathPrefix+"/"+url.PathEscape(session.ID)+"/renew", "", body)
	if err != nil {
		return time.Time{}, err
	}
	var response renewal
	if decodeStrict(data, &response) != nil || !response.ExpiresAt.After(client.now().UTC()) || response.ExpiresAt.After(now.Add(maxRenewalTTL)) {
		return time.Time{}, ErrInvalidResponse
	}
	return response.ExpiresAt.UTC(), nil
}
