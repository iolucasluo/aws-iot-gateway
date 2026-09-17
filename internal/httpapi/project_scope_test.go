package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/activation"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/internalauth"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/project"
)

const (
	testProjectID         = "50000000-0000-0000-0000-0000000000a1"
	testProjectAPIKey     = "ak_project_scope"
	testProjectAPISecret  = "project-scope-secret"
	testProjectCredential = "project-credential"
)

type stubProjectScope struct {
	scopes map[string]project.Scope
	err    error
}

func (stub stubProjectScope) ScopeForPrincipal(_ context.Context, projectID string) (project.Scope, error) {
	if stub.err != nil {
		return project.Scope{}, stub.err
	}
	scope, found := stub.scopes[projectID]
	if !found {
		return project.Scope{}, project.ErrProjectNotFound
	}
	return scope, nil
}

type stubCredentialStore struct {
	credentials map[string]internalauth.Credential
}

func (store stubCredentialStore) FindCredentialByAPIKey(_ context.Context, apiKey string) (internalauth.Credential, error) {
	credential, found := store.credentials[apiKey]
	if !found {
		return internalauth.Credential{}, internalauth.ErrUnknownAPIKey
	}
	return credential, nil
}

func newProjectScopedHandler(t *testing.T, now time.Time, scope ProjectProductScope) http.Handler {
	t.Helper()
	store := stubCredentialStore{credentials: map[string]internalauth.Credential{
		testProjectAPIKey: {
			InternalAppID: testProjectID, ProjectID: testProjectID, APIKey: testProjectAPIKey,
			APISecret: testProjectAPISecret, Permissions: []string{"devices:read"},
		},
	}}
	verifier, err := internalauth.NewVerifier(
		[]internalauth.Credential{{
			InternalAppID: "livpet-staging", APIKey: "ak_livpet", APISecret: "livpet-secret",
			Permissions: []string{"ota:publish", "ota:deploy"},
		}},
		&hmacNonceStore{used: map[string]struct{}{}},
		func() time.Time { return now },
		store,
	)
	if err != nil {
		t.Fatalf("NewVerifier() error = %v", err)
	}
	dependencies := Dependencies{}
	if scope != nil {
		dependencies.ProjectScope = scope
	}
	return NewInternalAPI(activation.NewService(), verifier, dependencies)
}

func signedProjectRequest(t *testing.T, now time.Time, nonce, path string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	timestamp := fmt.Sprintf("0000001%013d", now.UnixMilli())
	signature, err := internalauth.Sign(testProjectAPISecret, internalauth.SignatureRequest{
		APIKey: testProjectAPIKey, Timestamp: timestamp, Nonce: nonce,
		Method: http.MethodGet, Path: request.URL.EscapedPath(), RawQuery: request.URL.RawQuery,
	})
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	request.Header.Set("X-Api-Key", testProjectAPIKey)
	request.Header.Set("X-Timestamp", timestamp)
	request.Header.Set("X-Nonce", nonce)
	request.Header.Set("X-Signature", signature)
	return request
}

func decodeProductPIDs(t *testing.T, recorder *httptest.ResponseRecorder) []string {
	t.Helper()
	var payload struct {
		Products []struct {
			PID string `json:"pid"`
		} `json:"products"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode products: %v", err)
	}
	pids := make([]string, 0, len(payload.Products))
	for _, product := range payload.Products {
		pids = append(pids, product.PID)
	}
	return pids
}

// DB 凭证主体的产品目录必须被项目白名单收窄，而静态 INTERNAL_APPS_JSON 主体保持全局目录。
func TestDeviceProductsAreScopedToTheDatabaseCredentialProject(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(1_718_000_000_000)
	handler := newProjectScopedHandler(t, now, stubProjectScope{scopes: map[string]project.Scope{
		testProjectID: {ProjectID: testProjectID, AllowedPIDs: []string{"feeder-x1"}},
	}})

	scoped := httptest.NewRecorder()
	handler.ServeHTTP(scoped, signedProjectRequest(t, now, "nonce-project-scope", "/api/v1/openapi/device-products"))
	if scoped.Code != http.StatusOK {
		t.Fatalf("project catalog status = %d body = %s", scoped.Code, scoped.Body.String())
	}
	pids := decodeProductPIDs(t, scoped)
	if len(pids) != 1 || pids[0] != "feeder-x1" {
		t.Fatalf("project catalog pids = %v, want only feeder-x1", pids)
	}

	// 静态凭证仍然看到完整目录：既有消费者的行为不变。
	staticRequest := signedInternalRequest(t, now, "nonce-static-catalog", http.MethodGet, "/api/v1/openapi/device-products", nil)
	staticRecorder := httptest.NewRecorder()
	handler.ServeHTTP(staticRecorder, staticRequest)
	if staticRecorder.Code != http.StatusOK {
		t.Fatalf("static catalog status = %d body = %s", staticRecorder.Code, staticRecorder.Body.String())
	}
	if staticPIDs := decodeProductPIDs(t, staticRecorder); len(staticPIDs) != 3 {
		t.Fatalf("static catalog pids = %v, want the global catalog", staticPIDs)
	}
}

// 作用域不可用时必须 fail closed，不能退回全局目录。
func TestDeviceProductsFailClosedWhenProjectScopeIsUnavailable(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(1_718_000_000_000)

	for name, scope := range map[string]ProjectProductScope{
		"store-error":        stubProjectScope{err: errors.New("postgres unavailable")},
		"unknown-project":    stubProjectScope{},
		"dependency-missing": nil,
	} {
		handler := newProjectScopedHandler(t, now, scope)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, signedProjectRequest(t, now, "nonce-"+name, "/api/v1/openapi/device-products"))
		if recorder.Code != http.StatusServiceUnavailable {
			t.Errorf("project catalog status for %s = %d body = %s, want 503", name, recorder.Code, recorder.Body.String())
		}
	}
}

// 设备侧其它路由不受项目作用域影响：它们仍然按 binding 归属鉴权。
func TestProjectCredentialKeepsStaticDeviceRoutesWorking(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(1_718_000_000_000)
	handler := newProjectScopedHandler(t, now, stubProjectScope{scopes: map[string]project.Scope{
		testProjectID: {ProjectID: testProjectID, AllowedPIDs: []string{"feeder-x1"}},
	}})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/openapi/devices/list", bytes.NewReader([]byte(`{}`)))
	timestamp := fmt.Sprintf("0000001%013d", now.UnixMilli())
	signature, err := internalauth.Sign(testProjectAPISecret, internalauth.SignatureRequest{
		APIKey: testProjectAPIKey, Timestamp: timestamp, Nonce: "nonce-device-list",
		Method: http.MethodPost, Path: request.URL.EscapedPath(), Body: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	request.Header.Set("X-Api-Key", testProjectAPIKey)
	request.Header.Set("X-Timestamp", timestamp)
	request.Header.Set("X-Nonce", "nonce-device-list")
	request.Header.Set("X-Signature", signature)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	// 没有设备仓库时返回 503/500 都说明请求已经通过签名与主体解析，
	// 关键是它不再走 product-scope 分支（该分支只作用于 catalog）。
	if recorder.Code == http.StatusUnauthorized {
		t.Fatalf("project credential was rejected on devices/list: %s", recorder.Body.String())
	}
}
