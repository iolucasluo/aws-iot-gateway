// Package explorexsignal implements the provider adapter for the
// provider-neutral ExploreX realtime signaling contract.
package explorexsignal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/provider"
)

const (
	Protocol          = "explorex-signal-v1"
	maxResponseBytes  = 32 << 10
	maxSessionTTL     = 15 * time.Minute
	defaultClientTTL  = 5 * time.Second
	healthcheckPath   = "/healthz"
	sessionPathPrefix = "/v1/sessions"
)

var (
	ErrInvalidConfig   = errors.New("invalid ExploreX signaling configuration")
	ErrInvalidRequest  = errors.New("invalid ExploreX signaling request")
	ErrInvalidResponse = errors.New("invalid ExploreX signaling response")
	ErrUnavailable     = errors.New("ExploreX signaling unavailable")
)

var identifierPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

type Config struct {
	Endpoint           string
	HTTPClient         *http.Client
	AuthorizationToken string
	Now                func() time.Time
	Capabilities       provider.RealtimeCapabilities
}

type Client struct {
	endpoint           string
	httpClient         *http.Client
	authorizationToken string
	now                func() time.Time
	capabilities       provider.RealtimeCapabilities
}

var _ provider.RealtimeProvider = (*Client)(nil)

func NewClient(config Config) (*Client, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(config.Endpoint), "/")
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("%w: endpoint must be an HTTP(S) origin without credentials or query", ErrInvalidConfig)
	}
	if endpoint == "" || strings.ContainsAny(endpoint, "\r\n") {
		return nil, fmt.Errorf("%w: endpoint is empty or contains control characters", ErrInvalidConfig)
	}
	authorizationToken := strings.TrimSpace(config.AuthorizationToken)
	if strings.ContainsAny(authorizationToken, "\r\n") {
		return nil, fmt.Errorf("%w: authorization token contains control characters", ErrInvalidConfig)
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultClientTTL}
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	capabilities := config.Capabilities
	if len(capabilities.Protocols) == 0 {
		capabilities = provider.RealtimeCapabilities{
			Protocols:    []string{Protocol},
			DataChannel:  true,
			AudioSend:    true,
			AudioReceive: true,
		}
	}
	return &Client{
		endpoint: endpoint, httpClient: httpClient, authorizationToken: authorizationToken,
		now: now, capabilities: capabilities,
	}, nil
}

type prepareRequest struct {
	DeviceID          string         `json:"device_id"`
	BindingID         string         `json:"binding_id"`
	RequestID         string         `json:"request_id"`
	RequestedProtocol string         `json:"requested_protocol"`
	Capabilities      map[string]any `json:"capabilities"`
	ExpiresAt         string         `json:"expires_at"`
}

type sessionResponse struct {
	SessionID        string         `json:"session_id"`
	ProviderInstance string         `json:"provider_instance"`
	Protocol         string         `json:"protocol"`
	ClientConfig     map[string]any `json:"client_config"`
	Capabilities     map[string]any `json:"capabilities"`
	ExpiresAt        string         `json:"expires_at"`
}

func (client *Client) PrepareSession(ctx context.Context, request provider.RealtimeSessionRequest) (provider.RealtimeSessionDescriptor, error) {
	if client == nil || ctx == nil || !safeIdentifier(request.DeviceID) || !safeIdentifier(request.BindingID) ||
		!safeIdentifier(request.RequestID) || request.RequestedProtocol != Protocol || request.Capabilities == nil {
		return provider.RealtimeSessionDescriptor{}, ErrInvalidRequest
	}
	now := client.now().UTC()
	if request.ExpiresAt.IsZero() || !request.ExpiresAt.After(now) || request.ExpiresAt.Sub(now) > maxSessionTTL {
		return provider.RealtimeSessionDescriptor{}, ErrInvalidRequest
	}
	body, err := json.Marshal(prepareRequest{
		DeviceID: request.DeviceID, BindingID: request.BindingID, RequestID: request.RequestID,
		RequestedProtocol: request.RequestedProtocol, Capabilities: request.Capabilities,
		ExpiresAt: request.ExpiresAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return provider.RealtimeSessionDescriptor{}, fmt.Errorf("%w: encode request", ErrInvalidRequest)
	}
	response, err := client.doJSON(ctx, http.MethodPost, sessionPathPrefix, request.RequestID, body)
	if err != nil {
		return provider.RealtimeSessionDescriptor{}, err
	}
	var decoded sessionResponse
	if err := decodeStrict(response, &decoded); err != nil {
		return provider.RealtimeSessionDescriptor{}, fmt.Errorf("%w: %v", ErrInvalidResponse, err)
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, decoded.ExpiresAt)
	if err != nil || !expiresAt.After(now) || expiresAt.After(request.ExpiresAt) ||
		!safeIdentifier(decoded.SessionID) || !safeIdentifier(decoded.ProviderInstance) ||
		decoded.Protocol != Protocol || decoded.ClientConfig == nil || decoded.Capabilities == nil {
		return provider.RealtimeSessionDescriptor{}, ErrInvalidResponse
	}
	return provider.RealtimeSessionDescriptor{
		SessionID: decoded.SessionID, ProviderInstance: decoded.ProviderInstance, Protocol: decoded.Protocol,
		ClientConfig: decoded.ClientConfig, Capabilities: decoded.Capabilities, ExpiresAt: expiresAt.UTC(),
	}, nil
}

func (client *Client) CloseSession(ctx context.Context, session provider.RealtimeSessionRef) error {
	if client == nil || ctx == nil || !safeIdentifier(session.ID) || !safeIdentifier(session.ProviderInstance) {
		return ErrInvalidRequest
	}
	_, err := client.doJSON(ctx, http.MethodDelete, sessionPathPrefix+"/"+url.PathEscape(session.ID), "", nil)
	return err
}

func (client *Client) Capabilities() provider.RealtimeCapabilities {
	if client == nil {
		return provider.RealtimeCapabilities{}
	}
	return client.capabilities
}

func (client *Client) Health(ctx context.Context) error {
	if client == nil || ctx == nil {
		return ErrInvalidRequest
	}
	_, err := client.doJSON(ctx, http.MethodGet, healthcheckPath, "", nil)
	return err
}

func safeIdentifier(value string) bool {
	return identifierPattern.MatchString(value)
}

func (client *Client) doJSON(ctx context.Context, method, path, idempotencyKey string, body []byte) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, method, client.endpoint+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: create HTTP request", ErrUnavailable)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if client.authorizationToken != "" {
		request.Header.Set("Authorization", "Bearer "+client.authorizationToken)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, maxResponseBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("%w: read response", ErrUnavailable)
	}
	if len(data) > maxResponseBytes {
		return nil, fmt.Errorf("%w: response too large", ErrInvalidResponse)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("%w: HTTP %d", ErrUnavailable, response.StatusCode)
	}
	return data, nil
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("response contains trailing JSON")
		}
		return err
	}
	return nil
}
