package httpapi

import (
	"bytes"
	"context"
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

func TestListDevicesRequiresTheOwningInternalUser(t *testing.T) {
	handler := New(
		activation.NewService(),
		StaticTokenAuthenticator{"owner-token": "user-42"},
	)
	request := httptest.NewRequest(http.MethodGet, "/v1/devices", nil)
	request.Header.Set("Authorization", "Bearer owner-token")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /v1/devices status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	var payload []map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode device list: %v", err)
	}
	if len(payload) != 0 {
		t.Fatalf("GET /v1/devices = %#v, want no devices before activation", payload)
	}

	unauthenticated := httptest.NewRequest(http.MethodGet, "/v1/devices", nil)
	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, unauthenticated)
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("GET /v1/devices without token status = %d, want %d", denied.Code, http.StatusUnauthorized)
	}
}

func TestDevicePrepareReturnsTheCertificateIDNeededForDeviceInstallation(t *testing.T) {
	service := activation.NewServiceWith(testProvisioner{})
	created, err := service.Create(context.Background(), activation.CreateInput{
		UserSubject: "user-42",
		Method:      activation.MethodBLE,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	handler := New(service, StaticTokenAuthenticator{})
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/device-activations/"+created.ID+"/prepare",
		bytes.NewBufferString(`{"activation_code":"`+created.Code+`","csr_pem":"csr-pem"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("POST /v1/device-activations/{id}/prepare status = %d, want %d: %s",
			response.Code, http.StatusOK, response.Body.String())
	}
	var body struct {
		DeviceID      string `json:"device_id"`
		CertificateID string `json:"certificate_id"`
		Certificate   string `json:"certificate_pem"`
		Endpoint      string `json:"aws_iot_endpoint"`
		Nonce         string `json:"installation_nonce"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode prepare response: %v", err)
	}
	if body.DeviceID != "aws-iot-demo-device-1" || body.CertificateID != "certificate-1" ||
		body.Certificate == "" || body.Endpoint == "" || body.Nonce == "" {
		t.Fatalf("prepare response = %#v, want complete device installation fields", body)
	}
}

type testProvisioner struct{}

func (testProvisioner) Provision(context.Context, string, string) (activation.PreparedIdentity, error) {
	return activation.PreparedIdentity{
		DeviceID:       "aws-iot-demo-device-1",
		CertificateID:  "certificate-1",
		CertificatePEM: "certificate-pem",
		AWSIoTEndpoint: "example-ats.iot.ap-southeast-1.amazonaws.com",
	}, nil
}
