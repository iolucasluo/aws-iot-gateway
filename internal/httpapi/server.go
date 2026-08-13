package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/activation"
)

var ErrUnauthenticated = errors.New("unauthenticated")

type Authenticator interface {
	Subject(*http.Request) (string, error)
}

type StaticTokenAuthenticator map[string]string

func (a StaticTokenAuthenticator) Subject(request *http.Request) (string, error) {
	const bearerPrefix = "Bearer "
	value := request.Header.Get("Authorization")
	if len(value) <= len(bearerPrefix) || value[:len(bearerPrefix)] != bearerPrefix {
		return "", ErrUnauthenticated
	}
	subject, ok := a[value[len(bearerPrefix):]]
	if !ok || subject == "" {
		return "", ErrUnauthenticated
	}
	return subject, nil
}

type server struct {
	activation    *activation.Service
	authenticator Authenticator
}

func New(service *activation.Service, authenticator Authenticator) http.Handler {
	return server{activation: service, authenticator: authenticator}
}

func (s server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodGet && request.URL.Path == "/healthz" {
		writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if request.Method == http.MethodPost && request.URL.Path == "/v1/activations" {
		s.createActivation(writer, request)
		return
	}
	if request.Method == http.MethodGet && request.URL.Path == "/v1/devices" {
		s.listDevices(writer, request)
		return
	}
	if activationID, ok := strings.CutPrefix(request.URL.Path, "/v1/activations/"); ok &&
		activationID != "" && !strings.Contains(activationID, "/") && request.Method == http.MethodGet {
		s.getActivation(writer, request, activationID)
		return
	}
	if suffix, ok := strings.CutPrefix(request.URL.Path, "/v1/device-activations/"); ok {
		parts := strings.Split(suffix, "/")
		if len(parts) == 2 && parts[0] != "" && request.Method == http.MethodPost {
			switch parts[1] {
			case "prepare":
				s.prepareDevice(writer, request, parts[0])
				return
			case "confirm":
				s.confirmDevice(writer, request, parts[0])
				return
			}
		}
	}
	writeError(writer, http.StatusNotFound, "not_found", "endpoint not found")
}

func (s server) createActivation(writer http.ResponseWriter, request *http.Request) {
	subject, ok := s.requireSubject(writer, request)
	if !ok {
		return
	}
	var body struct {
		Method string `json:"method"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	created, err := s.activation.Create(request.Context(), activation.CreateInput{
		UserSubject: subject,
		Method:      activation.Method(body.Method),
	})
	if err != nil {
		writeActivationError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, activationResponse{Activation: created, IncludeCode: true})
}

func (s server) getActivation(writer http.ResponseWriter, request *http.Request, activationID string) {
	subject, ok := s.requireSubject(writer, request)
	if !ok {
		return
	}
	value, err := s.activation.GetForUser(request.Context(), subject, activationID)
	if err != nil {
		writeActivationError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, activationResponse{Activation: value})
}

func (s server) listDevices(writer http.ResponseWriter, request *http.Request) {
	subject, ok := s.requireSubject(writer, request)
	if !ok {
		return
	}
	devices, err := s.activation.ListDevicesForUser(request.Context(), subject)
	if err != nil {
		writeActivationError(writer, err)
		return
	}
	type deviceResponse struct {
		ID           string `json:"id"`
		ActivationID string `json:"activation_id"`
		Endpoint     string `json:"aws_iot_endpoint"`
	}
	response := make([]deviceResponse, 0, len(devices))
	for _, device := range devices {
		response = append(response, deviceResponse{
			ID:           device.ID,
			ActivationID: device.ActivationID,
			Endpoint:     device.Endpoint,
		})
	}
	writeJSON(writer, http.StatusOK, response)
}

func (s server) prepareDevice(writer http.ResponseWriter, request *http.Request, activationID string) {
	var body struct {
		Code   string `json:"activation_code"`
		CSRPEM string `json:"csr_pem"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	installation, err := s.activation.Prepare(request.Context(), activation.PrepareInput{
		ActivationID: activationID,
		Code:         body.Code,
		CSRPEM:       body.CSRPEM,
	})
	if err != nil {
		writeActivationError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{
		"activation_id":      installation.ActivationID,
		"device_id":          installation.DeviceID,
		"certificate_id":     installation.CertificateID,
		"certificate_pem":    installation.Certificate,
		"aws_iot_endpoint":   installation.Endpoint,
		"installation_nonce": installation.ProofNonce,
	})
}

func (s server) confirmDevice(writer http.ResponseWriter, request *http.Request, activationID string) {
	var body struct {
		Code  string `json:"activation_code"`
		Proof string `json:"installation_proof"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	confirmed, err := s.activation.Confirm(request.Context(), activation.ConfirmInput{
		ActivationID: activationID,
		Code:         body.Code,
		Proof:        body.Proof,
	})
	if err != nil {
		writeActivationError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, activationResponse{Activation: confirmed})
}

func (s server) requireSubject(writer http.ResponseWriter, request *http.Request) (string, bool) {
	if s.authenticator == nil {
		writeError(writer, http.StatusInternalServerError, "auth_not_configured", "authenticator is not configured")
		return "", false
	}
	subject, err := s.authenticator.Subject(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, "unauthenticated", "internal user token is required")
		return "", false
	}
	return subject, true
}

type activationResponse struct {
	Activation  activation.Activation
	IncludeCode bool
}

func (response activationResponse) MarshalJSON() ([]byte, error) {
	type payload struct {
		ActivationID string    `json:"activation_id"`
		Method       string    `json:"method"`
		Status       string    `json:"status"`
		ExpiresAt    time.Time `json:"expires_at"`
		Code         string    `json:"activation_code,omitempty"`
	}
	value := payload{
		ActivationID: response.Activation.ID,
		Method:       string(response.Activation.Method),
		Status:       string(response.Activation.Status),
		ExpiresAt:    response.Activation.ExpiresAt,
	}
	if response.IncludeCode {
		value.Code = response.Activation.Code
	}
	return json.Marshal(value)
}

func decodeJSON(writer http.ResponseWriter, request *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return false
	}
	return true
}

func writeActivationError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, activation.ErrInvalidInput):
		writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid")
	case errors.Is(err, activation.ErrActivationNotFound):
		writeError(writer, http.StatusNotFound, "activation_not_found", "activation was not found")
	case errors.Is(err, activation.ErrActivationUnauthorized), errors.Is(err, activation.ErrInstallationProofInvalid):
		writeError(writer, http.StatusForbidden, "activation_forbidden", "activation authorization was rejected")
	case errors.Is(err, activation.ErrActivationExpired):
		writeError(writer, http.StatusGone, "activation_expired", "activation has expired")
	default:
		writeError(writer, http.StatusInternalServerError, "activation_failed", "activation operation failed")
	}
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}
