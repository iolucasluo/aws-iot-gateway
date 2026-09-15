package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/activation"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/httpapi"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/internalauth"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/project"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/credentialcipher"
)

type integrationNonceStore struct{ used map[string]struct{} }

func (store *integrationNonceStore) Consume(_ context.Context, key string, _ time.Duration) (bool, error) {
	if _, found := store.used[key]; found {
		return false, nil
	}
	store.used[key] = struct{}{}
	return true, nil
}

func signedIntegrationRequest(t *testing.T, now time.Time, apiKey, secret, nonce, path string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	timestamp := fmt.Sprintf("0000001%013d", now.UnixMilli())
	signature, err := internalauth.Sign(secret, internalauth.SignatureRequest{
		APIKey: apiKey, Timestamp: timestamp, Nonce: nonce,
		Method: http.MethodGet, Path: request.URL.EscapedPath(), RawQuery: request.URL.RawQuery,
	})
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	request.Header.Set("X-Api-Key", apiKey)
	request.Header.Set("X-Timestamp", timestamp)
	request.Header.Set("X-Nonce", nonce)
	request.Header.Set("X-Signature", signature)
	return request
}

func integrationProductPIDs(t *testing.T, recorder *httptest.ResponseRecorder) []string {
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

// V3 端到端证据：真实 PostgreSQL 里的项目凭证经真实 AES-256-GCM 解密后，
// 走完整 HMAC 中间件与业务 handler；同时证明静态 INTERNAL_APPS_JSON 主体行为不变、
// 以及停用后的凭证不会被静态来源复活。
func TestDatabaseCredentialDrivesHMACAuthenticationAndProductScope(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatalf("ApplyMigrations() error = %v", err)
	}
	applyProductSeeds(t, ctx, pool)
	seedProjectOwner(t, ctx, pool)

	key := make([]byte, credentialcipher.KeyBytes)
	for index := range key {
		key[index] = byte(index * 7)
	}
	credentialCipher, err := credentialcipher.New(key, "kek-v1")
	if err != nil {
		t.Fatalf("credentialcipher.New() error = %v", err)
	}
	repository, err := NewProjectRepository(pool)
	if err != nil {
		t.Fatalf("NewProjectRepository() error = %v", err)
	}
	now := time.UnixMilli(1_718_000_000_000)
	projectService, err := project.NewService(repository, credentialCipher, func() time.Time { return now })
	if err != nil {
		t.Fatalf("project.NewService() error = %v", err)
	}
	owner := project.Actor{AccountID: integrationProjectOwnerID, Kind: "developer", Role: "developer"}
	created, err := projectService.CreateProject(ctx, owner, project.CreateProjectInput{Type: project.TypeCloud, Name: "Integration"})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	var productID string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM products WHERE pid='feeder-x1'`).Scan(&productID); err != nil {
		t.Fatalf("read seeded product: %v", err)
	}
	if _, err := projectService.AttachProduct(ctx, owner, created.ID, productID); err != nil {
		t.Fatalf("AttachProduct() error = %v", err)
	}
	issued, err := projectService.CreateCredential(ctx, owner, created.ID, project.CreateCredentialInput{
		Name: "integration", Permissions: []string{"devices:read"},
	})
	if err != nil {
		t.Fatalf("CreateCredential() error = %v", err)
	}

	// DB 凭证 + 静态凭证同时存在：验证顺序与回退语义。
	verifier, err := internalauth.NewVerifier(
		[]internalauth.Credential{{
			InternalAppID: "livpet-staging", APIKey: "ak_livpet_static", APISecret: "livpet-static-secret",
			Permissions: []string{"ota:publish", "ota:deploy"},
		}},
		&integrationNonceStore{used: map[string]struct{}{}},
		func() time.Time { return now },
		projectService,
	)
	if err != nil {
		t.Fatalf("NewVerifier() error = %v", err)
	}
	handler := httpapi.NewInternalAPI(activation.NewService(), verifier, httpapi.Dependencies{ProjectScope: projectService})

	// 1) DB 凭证可用，且产品目录被项目白名单收窄。
	scoped := httptest.NewRecorder()
	handler.ServeHTTP(scoped, signedIntegrationRequest(t, now, issued.Credential.APIKey, issued.APISecret,
		"nonce-db-credential", "/api/v1/openapi/device-products"))
	if scoped.Code != http.StatusOK {
		t.Fatalf("database credential status = %d body = %s", scoped.Code, scoped.Body.String())
	}
	if pids := integrationProductPIDs(t, scoped); len(pids) != 1 || pids[0] != "feeder-x1" {
		t.Fatalf("database credential catalog = %v, want only feeder-x1", pids)
	}

	// 2) 静态凭证行为不变：仍然看到完整目录。
	static := httptest.NewRecorder()
	handler.ServeHTTP(static, signedIntegrationRequest(t, now, "ak_livpet_static", "livpet-static-secret",
		"nonce-static-credential", "/api/v1/openapi/device-products"))
	if static.Code != http.StatusOK {
		t.Fatalf("static credential status = %d body = %s", static.Code, static.Body.String())
	}
	if pids := integrationProductPIDs(t, static); len(pids) != 3 {
		t.Fatalf("static credential catalog = %v, want the global catalog", pids)
	}

	// 3) 错误 secret 仍然拒绝。
	wrong := httptest.NewRecorder()
	handler.ServeHTTP(wrong, signedIntegrationRequest(t, now, issued.Credential.APIKey, "not-the-secret",
		"nonce-wrong-secret", "/api/v1/openapi/device-products"))
	if wrong.Code != http.StatusUnauthorized {
		t.Fatalf("wrong secret status = %d, want 401", wrong.Code)
	}

	// 4) 停用后的 DB 凭证立即失效，且不会被静态来源复活（静态来源没有这个 key）。
	if _, err := projectService.DisableCredential(ctx, owner, issued.Credential.ID); err != nil {
		t.Fatalf("DisableCredential() error = %v", err)
	}
	disabled := httptest.NewRecorder()
	handler.ServeHTTP(disabled, signedIntegrationRequest(t, now, issued.Credential.APIKey, issued.APISecret,
		"nonce-disabled-credential", "/api/v1/openapi/device-products"))
	if disabled.Code != http.StatusUnauthorized {
		t.Fatalf("disabled credential status = %d body = %s, want 401", disabled.Code, disabled.Body.String())
	}

	// 5) 静态凭证在 DB 来源存在时依然可用（明确未命中的 key 才回退）。
	stillStatic := httptest.NewRecorder()
	handler.ServeHTTP(stillStatic, signedIntegrationRequest(t, now, "ak_livpet_static", "livpet-static-secret",
		"nonce-static-again", "/api/v1/openapi/device-products"))
	if stillStatic.Code != http.StatusOK {
		t.Fatalf("static credential after DB activity status = %d, want 200", stillStatic.Code)
	}
}
