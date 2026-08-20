package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/activation"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/internalauth"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/binding"
	devicemodule "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/device"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/media"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/awskvs"
)

func TestInternalAPICreatesAndPollsHMACActivation(t *testing.T) {
	t.Parallel()

	now := time.UnixMilli(1_718_000_000_000)
	handler := newSignedTestHandler(t, activation.NewService(), now)
	createBody := []byte(`{"method":"ble","pid":"feeder-x1","binding_external_user_id":"livpet-user-42","idempotency_key":"operation-1"}`)
	create := signedInternalRequest(t, now, "nonce-create-00001", http.MethodPost, "/api/v1/openapi/device-activations", createBody)
	createResponse := httptest.NewRecorder()
	handler.ServeHTTP(createResponse, create)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201: %s", createResponse.Code, createResponse.Body.String())
	}
	var created struct {
		ActivationID   string `json:"activation_id"`
		ActivationCode string `json:"activation_code"`
		Method         string `json:"method"`
		Status         string `json:"status"`
		PID            string `json:"pid"`
		Activated      bool   `json:"activated"`
	}
	if err := json.Unmarshal(createResponse.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.ActivationID == "" || created.ActivationCode == "" || created.Method != "ble" ||
		created.Status != "pending" || created.PID != "feeder-x1" || created.Activated {
		t.Fatalf("create response = %#v", created)
	}

	pollPath := "/api/v1/openapi/device-activations/" + created.ActivationID
	poll := signedInternalRequest(t, now, "nonce-poll-0000001", http.MethodGet, pollPath, nil)
	pollResponse := httptest.NewRecorder()
	handler.ServeHTTP(pollResponse, poll)
	if pollResponse.Code != http.StatusOK {
		t.Fatalf("poll status = %d, want 200: %s", pollResponse.Code, pollResponse.Body.String())
	}
	var observed map[string]any
	if err := json.Unmarshal(pollResponse.Body.Bytes(), &observed); err != nil {
		t.Fatalf("decode poll response: %v", err)
	}
	if _, found := observed["activation_code"]; found {
		t.Fatalf("poll repeated activation code: %s", pollResponse.Body.String())
	}
}

func TestInternalAPIPreservesSelectedMinionPIDAcrossActivationLifecycle(t *testing.T) {
	t.Parallel()

	now := time.UnixMilli(1_718_000_000_000)
	handler := newSignedTestHandler(t, activation.NewService(), now)
	createBody := []byte(`{"method":"ble","pid":"minion-cam-x1","binding_external_user_id":"livpet-user-42","idempotency_key":"minion-operation-1"}`)
	create := signedInternalRequest(t, now, "nonce-minion-create", http.MethodPost, "/api/v1/openapi/device-activations", createBody)
	createResponse := httptest.NewRecorder()
	handler.ServeHTTP(createResponse, create)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201: %s", createResponse.Code, createResponse.Body.String())
	}
	var created struct {
		ActivationID string `json:"activation_id"`
		PID          string `json:"pid"`
	}
	if err := json.Unmarshal(createResponse.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.ActivationID == "" || created.PID != "minion-cam-x1" {
		t.Fatalf("create response = %#v, want minion-cam-x1", created)
	}

	pollPath := "/api/v1/openapi/device-activations/" + created.ActivationID
	poll := signedInternalRequest(t, now, "nonce-minion-poll", http.MethodGet, pollPath, nil)
	pollResponse := httptest.NewRecorder()
	handler.ServeHTTP(pollResponse, poll)
	if pollResponse.Code != http.StatusOK {
		t.Fatalf("poll status = %d, want 200: %s", pollResponse.Code, pollResponse.Body.String())
	}
	var observed struct {
		PID string `json:"pid"`
	}
	if err := json.Unmarshal(pollResponse.Body.Bytes(), &observed); err != nil {
		t.Fatalf("decode poll response: %v", err)
	}
	if observed.PID != "minion-cam-x1" {
		t.Fatalf("poll pid = %q, want minion-cam-x1", observed.PID)
	}
}

func TestInternalAPIListsActiveDeviceProducts(t *testing.T) {
	t.Parallel()

	now := time.UnixMilli(1_718_000_000_000)
	handler := newSignedTestHandler(t, activation.NewService(), now)
	request := signedInternalRequest(t, now, "nonce-product-catalog", http.MethodGet, "/api/v1/openapi/device-products", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("catalog status = %d, want 200: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Products []map[string]json.RawMessage `json:"products"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	if len(payload.Products) != 2 {
		t.Fatalf("catalog products = %#v, want two named products", payload.Products)
	}
	for _, product := range payload.Products {
		if _, found := product["pid"]; !found {
			t.Fatalf("product wire keys = %#v, want lowercase pid", product)
		}
		if _, found := product["name"]; !found {
			t.Fatalf("product wire keys = %#v, want lowercase name", product)
		}
		if _, found := product["PID"]; found {
			t.Fatalf("product wire keys = %#v, must not expose Go field names", product)
		}
	}
}

func TestInternalAPIRejectsUnknownProductPID(t *testing.T) {
	t.Parallel()

	now := time.UnixMilli(1_718_000_000_000)
	handler := newSignedTestHandler(t, activation.NewService(), now)
	body := []byte(`{"method":"ble","pid":"unknown-x1","binding_external_user_id":"livpet-user-42","idempotency_key":"unknown-product-1"}`)
	request := signedInternalRequest(t, now, "nonce-product-unknown", http.MethodPost, "/api/v1/openapi/device-activations", body)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !bytes.Contains(response.Body.Bytes(), []byte(`"error_code":"product_unavailable"`)) {
		t.Fatalf("unknown product = %d %s", response.Code, response.Body.String())
	}
}

func TestInternalAPIRequiresExplicitProductPID(t *testing.T) {
	t.Parallel()

	now := time.UnixMilli(1_718_000_000_000)
	handler := newSignedTestHandler(t, activation.NewService(), now)
	body := []byte(`{"method":"ble","binding_external_user_id":"livpet-user-42","idempotency_key":"missing-product-1"}`)
	request := signedInternalRequest(t, now, "nonce-product-missing", http.MethodPost, "/api/v1/openapi/device-activations", body)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !bytes.Contains(response.Body.Bytes(), []byte(`"error_code":"invalid_request"`)) {
		t.Fatalf("missing product = %d %s", response.Code, response.Body.String())
	}
}

func TestInternalAPIRemovesLegacyBearerRoutes(t *testing.T) {
	t.Parallel()

	now := time.UnixMilli(1_718_000_000_000)
	handler := newSignedTestHandler(t, activation.NewService(), now)
	for _, endpoint := range []string{"/v1/activations", "/v1/devices", "/v1/device-activations/id/prepare"} {
		request := httptest.NewRequest(http.MethodPost, endpoint, bytes.NewBufferString(`{}`))
		request.Header.Set("Authorization", "Bearer legacy-token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("POST %s status = %d, want 404", endpoint, response.Code)
		}
	}
}

func TestInternalAPIReturnsStableIdempotencyConflictCode(t *testing.T) {
	t.Parallel()

	now := time.UnixMilli(1_718_000_000_000)
	handler := newSignedTestHandler(t, activation.NewService(), now)
	body := []byte(`{"method":"qr","pid":"feeder-x1","binding_external_user_id":"user-1","idempotency_key":"same-operation"}`)
	first := signedInternalRequest(t, now, "nonce-idem-first1", http.MethodPost, "/api/v1/openapi/device-activations", body)
	firstResponse := httptest.NewRecorder()
	handler.ServeHTTP(firstResponse, first)
	if firstResponse.Code != http.StatusCreated {
		t.Fatalf("first create status = %d: %s", firstResponse.Code, firstResponse.Body.String())
	}
	replayed := signedInternalRequest(t, now, "nonce-idem-replay", http.MethodPost, "/api/v1/openapi/device-activations", body)
	replayedResponse := httptest.NewRecorder()
	handler.ServeHTTP(replayedResponse, replayed)
	if replayedResponse.Code != http.StatusConflict || !bytes.Contains(replayedResponse.Body.Bytes(), []byte(`"error_code":"activation_in_progress"`)) {
		t.Fatalf("replayed create = %d %s", replayedResponse.Code, replayedResponse.Body.String())
	}
}

func TestInternalAPIIssuesAndRevokesCompatibleDeviceToken(t *testing.T) {
	t.Parallel()

	now := time.UnixMilli(1_718_000_000_000)
	repository := &httpBindingRepository{binding: binding.Binding{
		ID: "018f0000-0000-7000-8000-000000000002", DeviceID: "018f0000-0000-7000-8000-000000000001",
		DeviceSN: "fdr-00000000000000000000000000000001", InternalAppID: "livpet-staging", Active: true,
	}}
	bindingService := binding.NewService(repository, func() (string, error) { return "device-token-http", nil }, func() time.Time { return now }, nil)
	handler := newSignedTestHandler(t, activation.NewService(), now, Dependencies{Binding: bindingService})

	tokenRequest := signedInternalRequest(t, now, "nonce-device-token1", http.MethodPost,
		"/api/v1/openapi/devices/token", []byte(`{"device_sn":"fdr-00000000000000000000000000000001"}`))
	tokenResponse := httptest.NewRecorder()
	handler.ServeHTTP(tokenResponse, tokenRequest)
	if tokenResponse.Code != http.StatusOK || !bytes.Contains(tokenResponse.Body.Bytes(), []byte(`"device_token":"device-token-http"`)) {
		t.Fatalf("issue token = %d %s", tokenResponse.Code, tokenResponse.Body.String())
	}
	if repository.token.TokenHash == binding.HashToken("device-token-http") && repository.plaintextStored {
		t.Fatal("repository retained the plaintext device token")
	}

	unbindPath := "/api/v1/openapi/devices/unbind/device-token-http"
	unbindRequest := signedInternalRequest(t, now, "nonce-device-unbind", http.MethodPost, unbindPath, nil)
	unbindResponse := httptest.NewRecorder()
	handler.ServeHTTP(unbindResponse, unbindRequest)
	if unbindResponse.Code != http.StatusOK || !bytes.Contains(unbindResponse.Body.Bytes(), []byte(`"unbound":true`)) || repository.binding.Active {
		t.Fatalf("unbind = %d %s, active=%t", unbindResponse.Code, unbindResponse.Body.String(), repository.binding.Active)
	}
}

func TestInternalAPIAuthorizesBoundUserNetworkReconfiguration(t *testing.T) {
	t.Parallel()

	now := time.UnixMilli(1_718_000_000_000)
	deviceSN := "fdr-00000000000000000000000000000001"
	repository := &httpBindingRepository{binding: binding.Binding{
		ID: "018f0000-0000-7000-8000-000000000002", DeviceID: "018f0000-0000-7000-8000-000000000001",
		DeviceSN: deviceSN, InternalAppID: "livpet-staging", BindingExternalUserID: "user-42", Active: true,
	}}
	repository.token = binding.DeviceToken{
		BindingID: repository.binding.ID, TokenHash: binding.HashToken("same-user-device-token"),
		ExpiresAt: now.Add(time.Hour),
	}
	bindingService := binding.NewService(repository, nil, func() time.Time { return now }, nil)
	handler := newSignedTestHandler(t, activation.NewService(), now, Dependencies{Binding: bindingService})
	path := "/api/v1/openapi/devices/" + deviceSN + "/network-reconfiguration-authorizations"
	body := []byte(`{"binding_external_user_id":"user-42","wifi_credentials_sha256":"0c8f371a2f7e8d9b4c717f22fea097fe53750c0c3c9705611ca4c58594e48fcc"}`)
	request := signedInternalRequest(t, now, "nonce-network-auth1", http.MethodPost, path, body)
	request.Header.Set("X-Device-Token", "same-user-device-token")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	var authorized map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &authorized); err != nil {
		t.Fatalf("decode network reconfiguration authorization: %v", err)
	}
	if response.Code != http.StatusOK || len(authorized) != 2 || authorized["authorized"] != true ||
		authorized["device_sn"] != deviceSN {
		t.Fatalf("network reconfiguration authorization = %d %s", response.Code, response.Body.String())
	}
	if !repository.binding.Active || repository.binding.BindingExternalUserID != "user-42" {
		t.Fatalf("authorization mutated binding = %#v", repository.binding)
	}
	replay := signedInternalRequest(t, now, "nonce-network-auth1", http.MethodPost, path, body)
	replay.Header.Set("X-Device-Token", "same-user-device-token")
	replayResponse := httptest.NewRecorder()
	handler.ServeHTTP(replayResponse, replay)
	if replayResponse.Code != http.StatusConflict ||
		!bytes.Contains(replayResponse.Body.Bytes(), []byte(`"error_code":"nonce_replayed"`)) {
		t.Fatalf("replayed authorization = %d %s", replayResponse.Code, replayResponse.Body.String())
	}
}

func TestInternalAPIRejectsNetworkReconfigurationWithoutExactBindingProof(t *testing.T) {
	t.Parallel()

	now := time.UnixMilli(1_718_000_000_000)
	deviceSN := "fdr-00000000000000000000000000000001"
	repository := &httpBindingRepository{binding: binding.Binding{
		ID: "018f0000-0000-7000-8000-000000000002", DeviceID: "018f0000-0000-7000-8000-000000000001",
		DeviceSN: deviceSN, InternalAppID: "livpet-staging", BindingExternalUserID: "user-42", Active: true,
	}}
	repository.token = binding.DeviceToken{
		BindingID: repository.binding.ID, TokenHash: binding.HashToken("same-user-device-token"),
		ExpiresAt: now.Add(time.Hour),
	}
	bindingService := binding.NewService(repository, nil, func() time.Time { return now }, nil)
	handler := newSignedTestHandler(t, activation.NewService(), now, Dependencies{Binding: bindingService})
	path := "/api/v1/openapi/devices/" + deviceSN + "/network-reconfiguration-authorizations"

	for _, testCase := range []struct {
		name, body, token, path string
		want                    int
	}{
		{name: "missing token", body: `{"binding_external_user_id":"user-42","wifi_credentials_sha256":"0c8f371a2f7e8d9b4c717f22fea097fe53750c0c3c9705611ca4c58594e48fcc"}`, want: http.StatusUnauthorized},
		{name: "wrong token", body: `{"binding_external_user_id":"user-42","wifi_credentials_sha256":"0c8f371a2f7e8d9b4c717f22fea097fe53750c0c3c9705611ca4c58594e48fcc"}`, token: "wrong-device-token", want: http.StatusForbidden},
		{name: "wrong device", body: `{"binding_external_user_id":"user-42","wifi_credentials_sha256":"0c8f371a2f7e8d9b4c717f22fea097fe53750c0c3c9705611ca4c58594e48fcc"}`, token: "same-user-device-token", path: "/api/v1/openapi/devices/fdr-wrong/network-reconfiguration-authorizations", want: http.StatusForbidden},
		{name: "wrong user", body: `{"binding_external_user_id":"user-43","wifi_credentials_sha256":"0c8f371a2f7e8d9b4c717f22fea097fe53750c0c3c9705611ca4c58594e48fcc"}`, token: "same-user-device-token", want: http.StatusForbidden},
		{name: "invalid digest", body: `{"binding_external_user_id":"user-42","wifi_credentials_sha256":"not-a-sha256"}`, token: "same-user-device-token", want: http.StatusBadRequest},
		{name: "unknown field", body: `{"binding_external_user_id":"user-42","wifi_credentials_sha256":"0c8f371a2f7e8d9b4c717f22fea097fe53750c0c3c9705611ca4c58594e48fcc","admin":true}`, token: "same-user-device-token", want: http.StatusBadRequest},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			requestPath := testCase.path
			if requestPath == "" {
				requestPath = path
			}
			request := signedInternalRequest(t, now, "nonce-network-"+strings.ReplaceAll(testCase.name, " ", "-"), http.MethodPost, requestPath, []byte(testCase.body))
			if testCase.token != "" {
				request.Header.Set("X-Device-Token", testCase.token)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != testCase.want {
				t.Fatalf("status = %d, want %d: %s", response.Code, testCase.want, response.Body.String())
			}
		})
	}
}

func TestInternalAPIRecommissionEchoesAndAppliesFullResetScope(t *testing.T) {
	t.Parallel()

	now := time.UnixMilli(1_718_000_000_000)
	repository := &httpBindingRepository{binding: binding.Binding{
		ID: "018f0000-0000-7000-8000-000000000002", DeviceID: "018f0000-0000-7000-8000-000000000001",
		DeviceSN: "fdr-00000000000000000000000000000001", InternalAppID: "livpet-staging", Active: true,
	}}
	bindingService := binding.NewService(repository, func() (string, error) { return "device-token-recommission", nil },
		func() time.Time { return now }, &httpRecommissioner{receipt: binding.ResetReceipt{
			CommandID: "session-recommission", Status: "success", Data: map[string]any{"code": float64(0)},
		}})
	if _, err := bindingService.IssueToken(context.Background(), "livpet-staging", repository.binding.DeviceSN); err != nil {
		t.Fatalf("IssueToken() error = %v", err)
	}
	handler := newSignedTestHandler(t, activation.NewService(), now, Dependencies{Binding: bindingService})
	path := "/api/v1/openapi/devices/recommission/device-token-recommission"
	request := signedInternalRequest(t, now, "nonce-recommission1", http.MethodPost, path,
		[]byte(`{"reset_scope":"all"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"reset_scope":"all"`)) || repository.binding.Active {
		t.Fatalf("recommission(all) = %d %s, active=%t", response.Code, response.Body.String(), repository.binding.Active)
	}
}

func TestInternalAPIFactoryResetAllUsesRecommissionAndPreservesLivPetResult(t *testing.T) {
	t.Parallel()

	now := time.UnixMilli(1_718_000_000_000)
	repository := &httpBindingRepository{binding: binding.Binding{
		ID: "018f0000-0000-7000-8000-000000000002", DeviceID: "018f0000-0000-7000-8000-000000000001",
		DeviceSN: "fdr-00000000000000000000000000000001", InternalAppID: "livpet-staging", Active: true,
	}}
	recommissioner := &httpRecommissioner{receipt: binding.ResetReceipt{
		CommandID: "livpet-factory-reset-session", Status: "success",
		Data: map[string]any{"code": float64(0), "reset_scope": "all", "will_reboot": true},
	}}
	bindingService := binding.NewService(repository, func() (string, error) { return "device-token-reset", nil },
		func() time.Time { return now }, recommissioner)
	if _, err := bindingService.IssueToken(context.Background(), "livpet-staging", repository.binding.DeviceSN); err != nil {
		t.Fatalf("IssueToken() error = %v", err)
	}
	handler := newSignedTestHandler(t, activation.NewService(), now, Dependencies{Binding: bindingService})
	path := "/api/v1/openapi/devices/" + repository.binding.DeviceSN + "/commands"
	body := []byte(`{"commands":[{"code":"factory_reset","value":{"scope":"all"}}],"wait_ack":true}`)
	request := signedInternalRequest(t, now, "nonce-factory-reset", http.MethodPost, path, body)
	request.Header.Set("X-Device-Token", "device-token-reset")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("factory_reset(all) status = %d: %s", response.Code, response.Body.String())
	}
	var result devicemodule.SendCommandsResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode factory_reset(all): %v", err)
	}
	if len(result.Results) != 1 || result.Results[0].Code != "factory_reset" || result.Results[0].Status != "success" ||
		result.Results[0].CommandID != "livpet-factory-reset-session" || result.Results[0].Data["scope"] != "all" ||
		result.Results[0].Data["code"] != float64(0) || result.Results[0].Data["will_reboot"] != true {
		t.Fatalf("factory_reset(all) result = %#v", result)
	}
	if repository.binding.Active || recommissioner.resetScope != "all" {
		t.Fatalf("active = %t, reset scope = %q", repository.binding.Active, recommissioner.resetScope)
	}
}

func TestInternalAPIFactoryResetAllFailsClosedWithoutACKOrAsBatch(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name string
		body string
	}{
		{name: "without ACK", body: `{"commands":[{"code":"factory_reset","value":{"scope":"all"}}],"wait_ack":false}`},
		{name: "inside batch", body: `{"commands":[{"code":"feed","value":{"portions":1}},{"code":"factory_reset","value":{"scope":"all"}}],"wait_ack":true}`},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			now := time.UnixMilli(1_718_000_000_000)
			repository := &httpBindingRepository{binding: binding.Binding{
				ID: "018f0000-0000-7000-8000-000000000002", DeviceID: "018f0000-0000-7000-8000-000000000001",
				DeviceSN: "fdr-00000000000000000000000000000001", InternalAppID: "livpet-staging", Active: true,
			}}
			recommissioner := &httpRecommissioner{receipt: binding.ResetReceipt{
				CommandID: "must-not-run", Status: "success", Data: map[string]any{"code": float64(0)},
			}}
			bindingService := binding.NewService(repository, func() (string, error) { return "device-token-reset", nil },
				func() time.Time { return now }, recommissioner)
			if _, err := bindingService.IssueToken(context.Background(), "livpet-staging", repository.binding.DeviceSN); err != nil {
				t.Fatalf("IssueToken() error = %v", err)
			}
			handler := newSignedTestHandler(t, activation.NewService(), now, Dependencies{Binding: bindingService})
			path := "/api/v1/openapi/devices/" + repository.binding.DeviceSN + "/commands"
			request := signedInternalRequest(t, now, "nonce-factory-reset-invalid-"+strings.ReplaceAll(testCase.name, " ", "-"),
				http.MethodPost, path, []byte(testCase.body))
			request.Header.Set("X-Device-Token", "device-token-reset")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != http.StatusBadRequest || !repository.binding.Active || recommissioner.calls != 0 {
				t.Fatalf("factory reset invalid = %d %s, active=%t, calls=%d",
					response.Code, response.Body.String(), repository.binding.Active, recommissioner.calls)
			}
		})
	}
}

func TestInternalAPIRejectsLegacyWiFiResetWithoutPublishing(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name string
		body string
	}{
		{name: "reset_wifi", body: `{"commands":[{"code":"reset_wifi","value":{}}],"wait_ack":true}`},
		{name: "factory_reset wifi", body: `{"commands":[{"code":"factory_reset","value":{"scope":"wifi"}}],"wait_ack":true}`},
		{name: "factory_reset missing scope", body: `{"commands":[{"code":"factory_reset","value":{}}],"wait_ack":true}`},
		{name: "batch containing reset_wifi", body: `{"commands":[{"code":"feed","value":{"portions":1}},{"code":"reset_wifi","value":{}}],"wait_ack":true}`},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			now := time.UnixMilli(1_718_000_000_000)
			repository := &httpBindingRepository{binding: binding.Binding{
				ID: "018f0000-0000-7000-8000-000000000002", DeviceID: "018f0000-0000-7000-8000-000000000001",
				DeviceSN: "fdr-00000000000000000000000000000001", InternalAppID: "livpet-staging", Active: true,
			}}
			bindingService := binding.NewService(repository, func() (string, error) { return "device-token-reset", nil },
				func() time.Time { return now }, nil)
			if _, err := bindingService.IssueToken(context.Background(), "livpet-staging", repository.binding.DeviceSN); err != nil {
				t.Fatalf("IssueToken() error = %v", err)
			}
			publisher := &httpPublisher{}
			deviceService := devicemodule.NewService(publisher, httpACKWaiter{}, &httpCommandRepository{},
				"aws-iot-gateway-staging", func() (string, error) { return "must-not-publish", nil })
			handler := newSignedTestHandler(t, activation.NewService(), now,
				Dependencies{Binding: bindingService, Device: deviceService})
			path := "/api/v1/openapi/devices/" + repository.binding.DeviceSN + "/commands"
			request := signedInternalRequest(t, now, "nonce-legacy-wifi-reset-"+strings.ReplaceAll(testCase.name, " ", "-"),
				http.MethodPost, path, []byte(testCase.body))
			request.Header.Set("X-Device-Token", "device-token-reset")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != http.StatusBadRequest ||
				!bytes.Contains(response.Body.Bytes(), []byte(`"error_code":"invalid_request"`)) ||
				!bytes.Contains(response.Body.Bytes(), []byte(`recommission(runtime)`)) ||
				publisher.calls != 0 || !repository.binding.Active {
				t.Fatalf("legacy Wi-Fi reset = %d %s, publishes=%d, active=%t",
					response.Code, response.Body.String(), publisher.calls, repository.binding.Active)
			}
		})
	}
}

func TestInternalAPIFactoryResetAllPreservesRejectedCommandResultAndBinding(t *testing.T) {
	t.Parallel()

	now := time.UnixMilli(1_718_000_000_000)
	repository := &httpBindingRepository{binding: binding.Binding{
		ID: "018f0000-0000-7000-8000-000000000002", DeviceID: "018f0000-0000-7000-8000-000000000001",
		DeviceSN: "fdr-00000000000000000000000000000001", InternalAppID: "livpet-staging", Active: true,
	}}
	recommissioner := &httpRecommissioner{receipt: binding.ResetReceipt{
		CommandID: "livpet-rejected-session", Status: "error", Reason: "device rejected command with code -1",
		Data: map[string]any{"code": float64(-1), "reset_scope": "all"},
	}}
	bindingService := binding.NewService(repository, func() (string, error) { return "device-token-reset", nil },
		func() time.Time { return now }, recommissioner)
	if _, err := bindingService.IssueToken(context.Background(), "livpet-staging", repository.binding.DeviceSN); err != nil {
		t.Fatalf("IssueToken() error = %v", err)
	}
	handler := newSignedTestHandler(t, activation.NewService(), now, Dependencies{Binding: bindingService})
	path := "/api/v1/openapi/devices/" + repository.binding.DeviceSN + "/commands"
	body := []byte(`{"commands":[{"code":"factory_reset","value":{"scope":"all"}}],"wait_ack":true}`)
	request := signedInternalRequest(t, now, "nonce-factory-reset-rejected", http.MethodPost, path, body)
	request.Header.Set("X-Device-Token", "device-token-reset")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || !repository.binding.Active {
		t.Fatalf("rejected factory reset = %d %s, active=%t", response.Code, response.Body.String(), repository.binding.Active)
	}
	var result devicemodule.SendCommandsResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode rejected factory reset: %v", err)
	}
	if len(result.Results) != 1 || result.Results[0].Code != "factory_reset" || result.Results[0].Status != "error" ||
		result.Results[0].CommandID != "livpet-rejected-session" || result.Results[0].Data["code"] != float64(-1) ||
		result.Results[0].Data["scope"] != "all" || result.Results[0].Reason == "" {
		t.Fatalf("rejected factory reset result = %#v", result)
	}
}

func TestInternalAPIFactoryResetAllPreservesDeviceBusyErrorAndBinding(t *testing.T) {
	t.Parallel()

	now := time.UnixMilli(1_718_000_000_000)
	repository := &httpBindingRepository{binding: binding.Binding{
		ID: "018f0000-0000-7000-8000-000000000002", DeviceID: "018f0000-0000-7000-8000-000000000001",
		DeviceSN: "fdr-00000000000000000000000000000001", InternalAppID: "livpet-staging", Active: true,
	}}
	bindingService := binding.NewService(repository, func() (string, error) { return "device-token-reset", nil },
		func() time.Time { return now }, &httpRecommissioner{err: devicemodule.ErrDeviceBusy})
	if _, err := bindingService.IssueToken(context.Background(), "livpet-staging", repository.binding.DeviceSN); err != nil {
		t.Fatalf("IssueToken() error = %v", err)
	}
	handler := newSignedTestHandler(t, activation.NewService(), now, Dependencies{Binding: bindingService})
	path := "/api/v1/openapi/devices/" + repository.binding.DeviceSN + "/commands"
	body := []byte(`{"commands":[{"code":"factory_reset","value":{"scope":"all"}}],"wait_ack":true}`)
	request := signedInternalRequest(t, now, "nonce-factory-reset-busy", http.MethodPost, path, body)
	request.Header.Set("X-Device-Token", "device-token-reset")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusConflict || !bytes.Contains(response.Body.Bytes(), []byte(`"error_code":"device_busy"`)) ||
		!repository.binding.Active {
		t.Fatalf("busy factory reset = %d %s, active=%t", response.Code, response.Body.String(), repository.binding.Active)
	}
}

func TestInternalAPICommandRequiresDeviceTokenAndPreservesLivPetResult(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(1_718_000_000_000)
	bindingRepository := &httpBindingRepository{binding: binding.Binding{
		ID: "018f0000-0000-7000-8000-000000000002", DeviceID: "018f0000-0000-7000-8000-000000000001",
		DeviceSN: "fdr-00000000000000000000000000000001", InternalAppID: "livpet-staging", Active: true,
	}}
	bindingService := binding.NewService(bindingRepository, func() (string, error) { return "device-token-command", nil }, func() time.Time { return now }, nil)
	if _, err := bindingService.IssueToken(context.Background(), "livpet-staging", bindingRepository.binding.DeviceSN); err != nil {
		t.Fatalf("IssueToken() error = %v", err)
	}
	deviceService := devicemodule.NewService(&httpPublisher{}, httpACKWaiter{}, &httpCommandRepository{},
		"aws-iot-gateway-staging", func() (string, error) { return "session-command", nil })
	handler := newSignedTestHandler(t, activation.NewService(), now, Dependencies{Binding: bindingService, Device: deviceService})
	path := "/api/v1/openapi/devices/" + bindingRepository.binding.DeviceSN + "/commands"
	body := []byte(`{"commands":[{"code":"feed","value":{"portions":1}}],"wait_ack":true}`)

	missingToken := signedInternalRequest(t, now, "nonce-command-miss", http.MethodPost, path, body)
	missingResponse := httptest.NewRecorder()
	handler.ServeHTTP(missingResponse, missingToken)
	if missingResponse.Code != http.StatusUnauthorized || !bytes.Contains(missingResponse.Body.Bytes(), []byte(`"error_code":"missing_device_token"`)) {
		t.Fatalf("missing token = %d %s", missingResponse.Code, missingResponse.Body.String())
	}

	request := signedInternalRequest(t, now, "nonce-command-valid", http.MethodPost, path, body)
	request.Header.Set("X-Device-Token", "device-token-command")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"status":"success"`)) ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"command_id":"session-command"`)) {
		t.Fatalf("command = %d %s", response.Code, response.Body.String())
	}
}

func TestInternalAPIStreamPreservesOpenAPICapabilityContract(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(1_718_000_000_000)
	bindingRepository := &httpBindingRepository{binding: binding.Binding{
		ID: "018f0000-0000-7000-8000-000000000002", DeviceID: "018f0000-0000-7000-8000-000000000001",
		DeviceSN: "fdr-00000000000000000000000000000001", InternalAppID: "livpet-staging", Active: true,
	}}
	bindingService := binding.NewService(bindingRepository, func() (string, error) { return "device-token-stream", nil }, func() time.Time { return now }, nil)
	if _, err := bindingService.IssueToken(context.Background(), "livpet-staging", bindingRepository.binding.DeviceSN); err != nil {
		t.Fatalf("IssueToken() error = %v", err)
	}
	streamService := media.NewStreamService(
		httpKVS{}, httpStreamCommander{}, &httpStreamRepository{}, "aws-iot-gateway-staging-",
		func() (string, error) { return "viewer-stream-1", nil },
	)
	handler := newSignedTestHandler(t, activation.NewService(), now, Dependencies{Binding: bindingService, Media: streamService})
	path := "/api/v1/openapi/devices/" + bindingRepository.binding.DeviceSN + "/streams"
	body := []byte(`{"type":"webrtc","requested_capabilities":{"video_receive":true,"audio_receive":false,"audio_send":false,"data_channels":[]}}`)
	request := signedInternalRequest(t, now, "nonce-stream-valid1", http.MethodPost, path, body)
	request.Header.Set("X-Device-Token", "device-token-stream")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("stream status = %d, want 201: %s", response.Code, response.Body.String())
	}
	var result struct {
		StreamID                  string                   `json:"stream_id"`
		CapabilityContractVersion string                   `json:"capability_contract_version"`
		FirmwareVersion           string                   `json:"firmware_version"`
		SupportedCapabilities     media.StreamCapabilities `json:"supported_capabilities"`
		GrantedCapabilities       media.StreamCapabilities `json:"granted_capabilities"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode stream response: %v", err)
	}
	if result.StreamID != "viewer-stream-1" || result.CapabilityContractVersion != "webrtc.v1" ||
		result.FirmwareVersion != "v0.0.0" || !result.SupportedCapabilities.AudioReceive || !result.GrantedCapabilities.VideoReceive {
		t.Fatalf("stream response = %#v", result)
	}
}

func TestLivPetLegacyWebRTCStreamDerivesAudioVideoCapabilities(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(1_718_000_000_000)
	bindingRepository := &httpBindingRepository{binding: binding.Binding{
		ID: "018f0000-0000-7000-8000-000000000002", DeviceID: "018f0000-0000-7000-8000-000000000001",
		DeviceSN: "fdr-00000000000000000000000000000001", InternalAppID: "livpet-staging", Active: true,
	}}
	bindingService := binding.NewService(bindingRepository, func() (string, error) { return "device-token-stream", nil }, func() time.Time { return now }, nil)
	if _, err := bindingService.IssueToken(context.Background(), "livpet-staging", bindingRepository.binding.DeviceSN); err != nil {
		t.Fatalf("IssueToken() error = %v", err)
	}
	commander := &httpCapturingStreamCommander{}
	streamService := media.NewStreamService(
		httpKVS{}, commander, &httpStreamRepository{}, "aws-iot-gateway-staging-",
		func() (string, error) { return "viewer-stream-legacy", nil },
	)
	handler := newSignedTestHandler(t, activation.NewService(), now, Dependencies{Binding: bindingService, Media: streamService})
	path := "/api/v1/openapi/devices/" + bindingRepository.binding.DeviceSN + "/streams"
	body := []byte(`{"type":"webrtc","wait_ack":true}`)
	request := signedInternalRequest(t, now, "nonce-stream-legacy1", http.MethodPost, path, body)
	request.Header.Set("X-Device-Token", "device-token-stream")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("legacy LivPet stream status = %d, want 201: %s", response.Code, response.Body.String())
	}
	if !commander.requested.VideoReceive || !commander.requested.AudioReceive || !commander.requested.AudioSend ||
		len(commander.requested.DataChannels) != 0 {
		t.Fatalf("legacy LivPet requested capabilities = %#v", commander.requested)
	}
	for index, invalidBody := range [][]byte{
		[]byte(`{"type":"webrtc"}`),
		[]byte(`{"type":"webrtc","wait_ack":true,"requested_capabilities":{"video_receive":true}}`),
	} {
		request := signedInternalRequest(t, now, fmt.Sprintf("nonce-stream-invalid%d", index), http.MethodPost, path, invalidBody)
		request.Header.Set("X-Device-Token", "device-token-stream")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || !bytes.Contains(response.Body.Bytes(), []byte(`"error_code":"invalid_stream_capabilities"`)) {
			t.Fatalf("invalid stream body %s = %d %s", invalidBody, response.Code, response.Body.String())
		}
	}
	commander.partialGrant = true
	partialRequest := signedInternalRequest(t, now, "nonce-stream-partial1", http.MethodPost, path, body)
	partialRequest.Header.Set("X-Device-Token", "device-token-stream")
	partialResponse := httptest.NewRecorder()
	handler.ServeHTTP(partialResponse, partialRequest)
	if partialResponse.Code != http.StatusConflict ||
		!bytes.Contains(partialResponse.Body.Bytes(), []byte(`"error_code":"stream_capability_unavailable"`)) {
		t.Fatalf("partial legacy grant = %d %s", partialResponse.Code, partialResponse.Body.String())
	}
	if commander.calls != 3 {
		t.Fatalf("legacy LivPet commander calls = %d, want start + partial start + cleanup", commander.calls)
	}
}

func TestInternalAPIRequiresHMACForOpenAPIPath(t *testing.T) {
	t.Parallel()

	now := time.UnixMilli(1_718_000_000_000)
	handler := newSignedTestHandler(t, activation.NewService(), now)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/openapi/devices/list", bytes.NewBufferString(`{"binding_external_user_id":"user-1"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned devices/list status = %d, want 401", response.Code)
	}
}

type httpDeviceListRepository struct {
	activation.Repository
	devices []activation.Device
}

func (repository *httpDeviceListRepository) ListDevices(context.Context, string, string) ([]activation.Device, error) {
	return append([]activation.Device(nil), repository.devices...), nil
}

func TestDeviceListUsesPersistedPresenceWithoutRequiringShadow(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC().Truncate(time.Second)
	repository := &httpDeviceListRepository{devices: []activation.Device{{
		ID: "fdr-0001", ActivationID: "018f0000-0000-7000-8000-000000000001",
		Endpoint: "example-ats.iot.ap-east-1.amazonaws.com", BindingExternalUserID: "user-1",
		Status: "provisioned", Online: true, LastSeenAt: &now,
	}}}
	handler := newSignedTestHandler(t, activation.NewServiceWithRepository(nil, repository), now)
	request := signedInternalRequest(t, now, "nonce-presence-001", http.MethodPost,
		"/api/v1/openapi/devices/list", []byte(`{"binding_external_user_id":"user-1"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("devices/list status = %d: %s", response.Code, response.Body.String())
	}
	var result struct {
		Devices []struct {
			DeviceSN   string `json:"device_sn"`
			Online     bool   `json:"online"`
			LastSeenAt string `json:"last_seen_at"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode devices/list: %v", err)
	}
	if len(result.Devices) != 1 || result.Devices[0].DeviceSN != "fdr-0001" ||
		!result.Devices[0].Online || result.Devices[0].LastSeenAt != now.Format(time.RFC3339) {
		t.Fatalf("devices/list = %#v", result.Devices)
	}
}

func TestLivPetDeviceRoutesReachTheCompatibleDeviceOperationHandler(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(1_718_000_000_000)
	handler := newSignedTestHandler(t, activation.NewService(), now)

	for index, endpoint := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/api/v1/openapi/devices/fdr-0001"},
		{method: http.MethodGet, path: "/api/v1/openapi/devices/fdr-0001/shadow"},
		{method: http.MethodPost, path: "/api/v1/openapi/devices/fdr-0001/commands"},
		{method: http.MethodPut, path: "/api/v1/openapi/devices/fdr-0001/schedules"},
		{method: http.MethodGet, path: "/api/v1/openapi/devices/fdr-0001/events?type=feed&limit=25"},
		{method: http.MethodPost, path: "/api/v1/openapi/devices/fdr-0001/streams"},
		{method: http.MethodDelete, path: "/api/v1/openapi/devices/fdr-0001/streams/stream-1"},
		{method: http.MethodPost, path: "/api/v1/openapi/devices/fdr-0001/snapshots"},
		{method: http.MethodGet, path: "/api/v1/openapi/devices/fdr-0001/snapshots/snapshot-1"},
		{method: http.MethodPost, path: "/api/v1/openapi/devices/fdr-0001/recordings"},
		{method: http.MethodGet, path: "/api/v1/openapi/devices/fdr-0001/recordings?limit=100"},
		{method: http.MethodGet, path: "/api/v1/openapi/devices/fdr-0001/recordings/recording-1"},
		{method: http.MethodGet, path: "/api/v1/openapi/devices/fdr-0001/recordings/local?offset=0&limit=200"},
		{method: http.MethodPost, path: "/api/v1/openapi/devices/fdr-0001/feed-audio"},
	} {
		request := signedInternalRequest(t, now, fmt.Sprintf("nested-route-%016d", index), endpoint.method, endpoint.path, nil)
		request.Header.Set("X-Device-Token", "device-token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusServiceUnavailable || !bytes.Contains(response.Body.Bytes(), []byte(`"error_code":"device_service_unavailable"`)) {
			t.Fatalf("%s %s = %d %s, want routed device operation", endpoint.method, endpoint.path, response.Code, response.Body.String())
		}
	}
}

func TestDevicePrepareUsesCompatibilityDeviceActivationPath(t *testing.T) {
	t.Parallel()

	service := activation.NewServiceWith(testProvisioner{})
	created, err := service.Create(context.Background(), activation.CreateInput{
		InternalAppID: "livpet-staging", BindingExternalUserID: "user-42",
		IdempotencyKey: "operation-qr", Method: activation.MethodQR, PID: "feeder-x1",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	now := time.UnixMilli(1_718_000_000_000)
	handler := newSignedTestHandler(t, service, now, Dependencies{DeviceRuntime: DeviceRuntimeConfig{
		AWSRegion: "ap-east-1", CredentialEndpoint: "example.credentials.iot.ap-east-1.amazonaws.com",
		RoleAlias: "aws-iot-gateway-staging-kvs", TopicPrefix: "aws-iot-gateway-staging",
		ShadowName: "internal-aws", KVSChannelPrefix: "aws-iot-gateway-staging-",
	}})
	body := []byte(fmt.Sprintf(`{"activation_id":%q,"activation_code":%q,"csr_pem":"csr-pem"}`, created.ID, created.Code))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/device-activations/qr/prepare-installation", bytes.NewReader(body))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("device prepare status = %d, want 200: %s", response.Code, response.Body.String())
	}
	var installation map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &installation); err != nil {
		t.Fatalf("decode installation: %v", err)
	}
	deviceSN, _ := installation["device_sn"].(string)
	if len(deviceSN) != 36 || deviceSN[:4] != "fdr-" || installation["certificate_id"] != "certificate-1" {
		t.Fatalf("installation = %#v", installation)
	}
	for key, expected := range map[string]string{
		"device_id": deviceSN, "aws_region": "ap-east-1",
		"aws_iot_credential_endpoint": "example.credentials.iot.ap-east-1.amazonaws.com",
		"aws_iot_role_alias":          "aws-iot-gateway-staging-kvs",
		"topic_prefix":                "aws-iot-gateway-staging", "aws_iot_shadow_name": "internal-aws",
		"aws_kvs_channel_prefix": "aws-iot-gateway-staging-",
	} {
		if installation[key] != expected {
			t.Fatalf("installation[%s] = %#v, want %q", key, installation[key], expected)
		}
	}
}

func TestLivPetActivationCreateAcceptsLegacyCommissioningEnvelope(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(1_718_000_000_000)
	handler := newSignedTestHandler(t, activation.NewService(), now)
	body := []byte(`{"method":"ble","pid":"feeder-x1","session_id":"session-1","client_hello_cbor":"client","device_hello_cbor":"device","binding_external_user_id":"user-1","idempotency_key":"idem-legacy-1"}`)
	request := signedInternalRequest(t, now, "nonce-legacy-create", http.MethodPost, "/api/v1/openapi/device-activations", body)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", response.Code, response.Body.String())
	}
	var created struct {
		ActivationID   string `json:"activation_id"`
		ActivationCode string `json:"activation_code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil || created.ActivationID == "" || created.ActivationCode == "" {
		t.Fatalf("create response = %#v, %v", created, err)
	}
	sessionBody := []byte(`{"session_id":"session-2","client_hello_cbor":"client-2","device_hello_cbor":"device-2","binding_external_user_id":"user-1","idempotency_key":"idem-session-1"}`)
	sessionPath := "/api/v1/openapi/device-activations/" + created.ActivationID + "/sessions"
	sessionRequest := signedInternalRequest(t, now, "nonce-legacy-session", http.MethodPost, sessionPath, sessionBody)
	sessionResponse := httptest.NewRecorder()
	handler.ServeHTTP(sessionResponse, sessionRequest)
	if sessionResponse.Code != http.StatusOK {
		t.Fatalf("session status = %d, body = %s", sessionResponse.Code, sessionResponse.Body.String())
	}
}

func TestQRActivationReturnsLegacyAuthorizationCodeAlias(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(1_718_000_000_000)
	handler := newSignedTestHandler(t, activation.NewService(), now)
	body := []byte(`{"method":"qr","pid":"feeder-x1","binding_external_user_id":"user-1","idempotency_key":"idem-qr-alias"}`)
	request := signedInternalRequest(t, now, "nonce-qr-alias-001", http.MethodPost, "/api/v1/openapi/device-activations", body)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", response.Code, response.Body.String())
	}
	var created struct {
		ActivationCode      string `json:"activation_code"`
		QRAuthorizationCode string `json:"qr_authorization_code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if created.ActivationCode == "" || created.QRAuthorizationCode != created.ActivationCode || len(created.QRAuthorizationCode) != 43 {
		t.Fatalf("QR code aliases = %#v", created)
	}
}

func newSignedTestHandler(t *testing.T, service *activation.Service, now time.Time, dependencies ...Dependencies) http.Handler {
	t.Helper()
	verifier, err := internalauth.NewVerifier(
		[]internalauth.Credential{{InternalAppID: "livpet-staging", APIKey: "ak_livpet", APISecret: "livpet-secret"}},
		&hmacNonceStore{used: map[string]struct{}{}},
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatalf("NewVerifier() error = %v", err)
	}
	return NewInternalAPI(service, verifier, dependencies...)
}

func signedInternalRequest(t *testing.T, now time.Time, nonce, method, path string, body []byte) *http.Request {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	timestamp := fmt.Sprintf("0000001%013d", now.UnixMilli())
	signature, err := internalauth.Sign("livpet-secret", internalauth.SignatureRequest{
		APIKey: "ak_livpet", Timestamp: timestamp, Nonce: nonce,
		Method: method, Path: request.URL.EscapedPath(), RawQuery: request.URL.RawQuery, Body: body,
	})
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	request.Header.Set("X-Api-Key", "ak_livpet")
	request.Header.Set("X-Timestamp", timestamp)
	request.Header.Set("X-Nonce", nonce)
	request.Header.Set("X-Signature", signature)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

type testProvisioner struct{}

func (testProvisioner) Provision(_ context.Context, deviceSN, _ string) (activation.PreparedIdentity, error) {
	return activation.PreparedIdentity{
		DeviceID:       deviceSN,
		CertificateID:  "certificate-1",
		CertificatePEM: "certificate-pem",
		AWSIoTEndpoint: "example-ats.iot.ap-east-1.amazonaws.com",
	}, nil
}

type httpBindingRepository struct {
	binding         binding.Binding
	token           binding.DeviceToken
	plaintextStored bool
}

type httpPublisher struct {
	calls   int
	topic   string
	payload []byte
}

type httpRecommissioner struct {
	receipt    binding.ResetReceipt
	err        error
	calls      int
	resetScope string
}

func (recommissioner *httpRecommissioner) Reset(_ context.Context, _ binding.Binding, resetScope string) (binding.ResetReceipt, error) {
	recommissioner.calls++
	recommissioner.resetScope = resetScope
	return recommissioner.receipt, recommissioner.err
}

func (publisher *httpPublisher) Publish(_ context.Context, topic string, payload []byte) error {
	publisher.calls++
	publisher.topic = topic
	publisher.payload = append([]byte(nil), payload...)
	return nil
}

type httpACKWaiter struct{}

func (httpACKWaiter) Wait(_ context.Context, _ string, sessionID string, _ time.Duration) (map[string]any, error) {
	return map[string]any{"session_id": sessionID, "code": float64(0)}, nil
}

type httpPTZACKWaiter struct{}

func (httpPTZACKWaiter) Wait(_ context.Context, _ string, sessionID string, _ time.Duration) (map[string]any, error) {
	return map[string]any{
		"action": "ptz", "session_id": sessionID, "code": float64(0), "op": "get_position",
		"pan": float64(1800), "tilt": float64(-500), "limit_h": float64(4000), "limit_v": float64(2000),
		"pan_normalized": 0.45, "tilt_normalized": -0.25, "moving": true,
	}, nil
}

type httpCommandRepository struct{}

func (*httpCommandRepository) Create(context.Context, devicemodule.Command) error { return nil }
func (*httpCommandRepository) Complete(context.Context, string, string, map[string]any, string) error {
	return nil
}

type httpKVS struct{}

func (httpKVS) EnsureChannel(_ context.Context, name string) (awskvs.Channel, error) {
	return awskvs.Channel{Name: name, ARN: "arn:aws:kinesisvideo:ap-east-1:123456789012:channel/test/1"}, nil
}

func (httpKVS) Playback(_ context.Context, channel awskvs.Channel, viewerID string, _ time.Duration) (awskvs.PlaybackConfig, error) {
	return awskvs.PlaybackConfig{
		Provider: "aws_kvs", ChannelName: channel.Name, ChannelARN: channel.ARN, ClientID: viewerID,
		WSSURL: "wss://signaling.example.test", ICEServers: []awskvs.ICEServer{{URLs: "stun:example.test:443"}},
		ExpiresAt: time.UnixMilli(1_718_000_300_000),
	}, nil
}

type httpStreamCommander struct{}

func (httpStreamCommander) SendCommands(_ context.Context, _, _, _ string, commands []devicemodule.CommandInput, _ bool) (devicemodule.SendCommandsResult, error) {
	var value map[string]any
	if len(commands) != 1 || json.Unmarshal(commands[0].Value, &value) != nil {
		return devicemodule.SendCommandsResult{}, devicemodule.ErrInvalidInput
	}
	viewerID, _ := value["viewer_client_id"].(string)
	return devicemodule.SendCommandsResult{Results: []devicemodule.CommandResult{{
		Status: "success",
		Data: map[string]any{
			"code": float64(0), "action": "stream", "type": "webrtc", "start": float64(1), "status": "ready",
			"viewer_client_id": viewerID, "capability_contract_version": "webrtc.v1", "firmware_version": "v0.0.0",
			"supported": map[string]any{
				"video_receive": true, "audio_receive": true, "audio_send": true,
				"data_channels": []any{"explorex.control.v1"},
			},
			"accepted": map[string]any{
				"video_receive": true, "audio_receive": false, "audio_send": false, "data_channels": []any{},
			},
		},
	}}}, nil
}

type httpCapturingStreamCommander struct {
	requested    media.StreamCapabilities
	calls        int
	partialGrant bool
}

func (commander *httpCapturingStreamCommander) SendCommands(_ context.Context, _, _, _ string, commands []devicemodule.CommandInput, _ bool) (devicemodule.SendCommandsResult, error) {
	var value struct {
		ViewerID              string                   `json:"viewer_client_id"`
		Start                 int                      `json:"start"`
		RequestedCapabilities media.StreamCapabilities `json:"requested_capabilities"`
	}
	if len(commands) != 1 || json.Unmarshal(commands[0].Value, &value) != nil {
		return devicemodule.SendCommandsResult{}, devicemodule.ErrInvalidInput
	}
	commander.calls++
	if value.Start == 0 {
		return devicemodule.SendCommandsResult{}, nil
	}
	commander.requested = value.RequestedCapabilities
	supported := map[string]any{
		"video_receive": true, "audio_receive": true, "audio_send": true, "data_channels": []any{},
	}
	accepted := supported
	if commander.partialGrant {
		accepted = map[string]any{
			"video_receive": true, "audio_receive": false, "audio_send": false, "data_channels": []any{},
		}
	}
	return devicemodule.SendCommandsResult{Results: []devicemodule.CommandResult{{
		Status: "success",
		Data: map[string]any{
			"code": float64(0), "action": "stream", "type": "webrtc", "start": float64(1), "status": "ready",
			"viewer_client_id": value.ViewerID, "capability_contract_version": "webrtc.v1", "firmware_version": "v0.0.0",
			"supported": supported, "accepted": accepted,
		},
	}}}, nil
}

type httpStreamRepository struct{}

func (*httpStreamRepository) Create(context.Context, media.Stream) error { return nil }
func (*httpStreamRepository) Find(context.Context, string, string, string) (bool, error) {
	return true, nil
}
func (*httpStreamRepository) Close(context.Context, string, string, string) (bool, error) {
	return true, nil
}

func (repository *httpBindingRepository) FindActiveBinding(_ context.Context, internalAppID, deviceSN string) (binding.Binding, error) {
	if !repository.binding.Active || repository.binding.InternalAppID != internalAppID || repository.binding.DeviceSN != deviceSN {
		return binding.Binding{}, binding.ErrBindingNotFound
	}
	return repository.binding, nil
}

func (repository *httpBindingRepository) StoreToken(_ context.Context, token binding.DeviceToken) error {
	repository.token = token
	return nil
}

func (repository *httpBindingRepository) ValidateToken(_ context.Context, internalAppID string, tokenHash [32]byte, deviceSN string) (binding.Binding, error) {
	if !repository.binding.Active || repository.binding.InternalAppID != internalAppID || repository.binding.DeviceSN != deviceSN || repository.token.TokenHash != tokenHash {
		return binding.Binding{}, binding.ErrTokenNotFound
	}
	return repository.binding, nil
}

func (repository *httpBindingRepository) FindBindingByToken(_ context.Context, tokenHash [32]byte) (binding.Binding, error) {
	if !repository.binding.Active || repository.token.TokenHash != tokenHash {
		return binding.Binding{}, binding.ErrTokenNotFound
	}
	return repository.binding, nil
}

func (repository *httpBindingRepository) ReleaseBinding(_ context.Context, bindingID, _ string) (binding.Binding, error) {
	if !repository.binding.Active || repository.binding.ID != bindingID {
		return binding.Binding{}, binding.ErrBindingNotFound
	}
	released := repository.binding
	repository.binding.Active = false
	return released, nil
}

func TestInternalAPIDeviceStatusQueriesPTZThroughGateway(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(1_718_000_000_000)
	bindingRepository := &httpBindingRepository{binding: binding.Binding{
		ID: "018f0000-0000-7000-8000-000000000002", DeviceID: "018f0000-0000-7000-8000-000000000001",
		DeviceSN: "fdr-00000000000000000000000000000001", InternalAppID: "livpet-staging", Active: true,
	}}
	bindingService := binding.NewService(bindingRepository, func() (string, error) { return "device-token-status", nil }, func() time.Time { return now }, nil)
	if _, err := bindingService.IssueToken(context.Background(), "livpet-staging", bindingRepository.binding.DeviceSN); err != nil {
		t.Fatalf("IssueToken() error = %v", err)
	}
	publisher := &httpPublisher{}
	deviceService := devicemodule.NewService(publisher, httpPTZACKWaiter{}, &httpCommandRepository{},
		"aws-iot-gateway-staging", func() (string, error) { return "session-status", nil })
	handler := newSignedTestHandler(t, activation.NewService(), now, Dependencies{Binding: bindingService, Device: deviceService})
	path := "/api/v1/openapi/devices/" + bindingRepository.binding.DeviceSN + "/status"
	request := signedInternalRequest(t, now, "nonce-device-status", http.MethodGet, path, nil)
	request.Header.Set("X-Device-Token", "device-token-status")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		PTZ devicemodule.PTZStatus `json:"ptz"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.PTZ.Pan != 1800 || payload.PTZ.Tilt != -500 || !payload.PTZ.Moving {
		t.Fatalf("payload = %#v", payload)
	}
	var command map[string]any
	if err := json.Unmarshal(publisher.payload, &command); err != nil {
		t.Fatalf("decode command: %v", err)
	}
	if command["action"] != "ptz" || command["op"] != "get_position" || command["session_id"] != "session-status" {
		t.Fatalf("command = %#v", command)
	}
}

func TestWriteMediaErrorReportsSnapshotProcessingFailure(t *testing.T) {
	t.Parallel()
	response := httptest.NewRecorder()

	writeMediaError(response, media.ErrSnapshotProcessing)

	if response.Code != http.StatusServiceUnavailable ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"error_code":"snapshot_processing_failed"`)) {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}
