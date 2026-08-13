package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/activation"
)

func TestCreateActivationRequiresInternalUserAndReturnsTheCodeOnlyOnce(t *testing.T) {
	t.Parallel()

	handler := New(
		activation.NewService(),
		StaticTokenAuthenticator{"internal-token": "user-42"},
	)
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/activations",
		bytes.NewBufferString(`{"method":"ble"}`),
	)
	request.Header.Set("Authorization", "Bearer internal-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("POST /v1/activations status = %d, want %d: %s", response.Code, http.StatusCreated, response.Body.String())
	}
	var created struct {
		ActivationID string `json:"activation_id"`
		Method       string `json:"method"`
		Status       string `json:"status"`
		Code         string `json:"activation_code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if created.ActivationID == "" || created.Method != "ble" ||
		created.Status != "created" || created.Code == "" {
		t.Fatalf("create response = %#v, want issued BLE activation", created)
	}

	getRequest := httptest.NewRequest(
		http.MethodGet,
		"/v1/activations/"+created.ActivationID,
		nil,
	)
	getRequest.Header.Set("Authorization", "Bearer internal-token")
	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, getRequest)
	if getResponse.Code != http.StatusOK {
		t.Fatalf("GET /v1/activations/{id} status = %d, want %d", getResponse.Code, http.StatusOK)
	}
	var observed map[string]any
	if err := json.Unmarshal(getResponse.Body.Bytes(), &observed); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if _, found := observed["activation_code"]; found {
		t.Fatalf("GET response must not repeat activation_code: %s", getResponse.Body.String())
	}
}
