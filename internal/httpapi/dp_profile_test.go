package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/activation"
)

func TestActivationSelectsAnExplicitIndependentDPProfile(t *testing.T) {
	for _, tc := range []struct {
		name, profile string
		status        int
		errorCode     string
	}{
		{"cross-product", "feeder-v1", http.StatusCreated, ""},
		{"missing", "", http.StatusBadRequest, "invalid_request"},
		{"unknown", "unknown-v1", http.StatusBadRequest, "dp_profile_unavailable"},
		{"malformed", "feeder", http.StatusBadRequest, "invalid_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.UnixMilli(1_718_000_000_000)
			handler := newSignedTestHandler(t, activation.NewService(), now)
			body, _ := json.Marshal(map[string]string{"method": "qr", "pid": "lamp-x1", "dp_profile": tc.profile, "binding_external_user_id": "user", "idempotency_key": "profile-test"})
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, signedInternalRequest(t, now, "nonce-profile-001", http.MethodPost, "/api/v1/openapi/device-activations", body))
			if response.Code != tc.status {
				t.Fatalf("status=%d want=%d: %s", response.Code, tc.status, response.Body.String())
			}
			var payload map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if tc.status == http.StatusCreated {
				if payload["dp_profile"] != tc.profile || payload["pid"] != "lamp-x1" {
					t.Fatalf("selection lost: %v", payload)
				}
			} else if payload["error_code"] != tc.errorCode {
				t.Fatalf("error code=%v want=%s", payload["error_code"], tc.errorCode)
			}
		})
	}
}

func TestDPProfileCatalogIsAnAuthenticatedIndependentDirectory(t *testing.T) {
	now := time.UnixMilli(1_718_000_000_000)
	handler := newSignedTestHandler(t, activation.NewService(), now)
	path := "/api/v1/openapi/dp-profiles"
	unsigned := httptest.NewRecorder()
	handler.ServeHTTP(unsigned, httptest.NewRequest(http.MethodGet, path, nil))
	if unsigned.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned status=%d", unsigned.Code)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, signedInternalRequest(t, now, "nonce-profiles-01", http.MethodGet, path, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("catalog status=%d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Profiles []struct {
			Code string `json:"code"`
			Name string `json:"name"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Profiles) != 3 {
		t.Fatalf("profiles=%v", payload.Profiles)
	}
}
