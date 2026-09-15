package platformapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/portalaccount"
)

type stubLimiter struct {
	mutex    sync.Mutex
	allowed  bool
	failure  error
	lastKey  string
	lastSeen int
}

func (limiter *stubLimiter) Allow(_ context.Context, key string, limit int, _ time.Duration) (bool, error) {
	limiter.mutex.Lock()
	defer limiter.mutex.Unlock()
	limiter.lastKey = key
	limiter.lastSeen = limit
	if limiter.failure != nil {
		return false, limiter.failure
	}
	return limiter.allowed, nil
}

type apiFixture struct {
	server     *Server
	repository *portalaccount.MemoryRepository
	accounts   *portalaccount.Service
	limiter    *stubLimiter
	staff      portalaccount.Account
	token      string
}

func newAPIFixture(t *testing.T) apiFixture {
	t.Helper()
	repository := portalaccount.NewMemoryRepository()
	accounts, err := portalaccount.NewService(repository, func() time.Time {
		return time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatalf("portalaccount.NewService() error = %v", err)
	}
	limiter := &stubLimiter{allowed: true}
	server, err := New(accounts, nil, nil, limiter, Options{
		AllowedOrigins:  []string{"https://admin.example.com"},
		LoginRateLimit:  5,
		LoginRateWindow: 15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	hash, err := portalaccount.HashPassword("correct horse battery")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	staff := portalaccount.Account{
		ID: "10000000-0000-0000-0000-0000000000a1", Kind: portalaccount.KindStaff, Role: portalaccount.RoleSuperAdmin,
		Email: "ops@example.com", PasswordHash: hash, Status: portalaccount.StatusActive,
		CreatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := repository.CreateAccount(context.Background(), staff); err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}
	return apiFixture{server: server, repository: repository, accounts: accounts, limiter: limiter, staff: staff}
}

func (fixture apiFixture) login(t *testing.T) string {
	t.Helper()
	recorder := fixture.do(t, http.MethodPost, "/api/v1/admin/auth/login",
		`{"email":"ops@example.com","password":"correct horse battery"}`, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("login status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode login body: %v", err)
	}
	return body.Token
}

func (fixture apiFixture) do(t *testing.T, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	request.RemoteAddr = "203.0.113.10:44321"
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	fixture.server.ServeHTTP(recorder, request)
	return recorder
}

func TestHealthAndUnknownRoutes(t *testing.T) {
	t.Parallel()
	fixture := newAPIFixture(t)
	if recorder := fixture.do(t, http.MethodGet, "/healthz", "", ""); recorder.Code != http.StatusOK {
		t.Fatalf("healthz status = %d", recorder.Code)
	}
	if recorder := fixture.do(t, http.MethodGet, "/api/v1/openapi/device-products", "", ""); recorder.Code != http.StatusNotFound {
		t.Fatalf("device route status = %d, want 404 (the platform process must not serve device routes)", recorder.Code)
	}
	if recorder := fixture.do(t, http.MethodGet, "/api/v1/manufacturer/projects", "", ""); recorder.Code != http.StatusNotFound {
		t.Fatalf("unimplemented portal prefix status = %d, want 404", recorder.Code)
	}
	// developer 前缀已实现；未注入项目服务时整个前缀按依赖缺失 fail closed（503），
	// 而不是悄悄退回 404 或放行。
	if recorder := fixture.do(t, http.MethodGet, "/api/v1/developer/projects", "", ""); recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("developer prefix without a project service status = %d, want 503", recorder.Code)
	}
	// 账号治理不依赖项目域：admin 前缀在同样条件下仍然正常工作。
	if recorder := fixture.do(t, http.MethodGet, "/api/v1/admin/accounts", "", ""); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("admin accounts without a project service status = %d, want 401", recorder.Code)
	}
	if recorder := fixture.do(t, http.MethodGet, "/healthz", "", ""); recorder.Header().Get("X-Request-Id") == "" {
		t.Fatal("response is missing X-Request-Id")
	}
}

func TestLoginIssuesSessionAndProtectsAccountRoutes(t *testing.T) {
	t.Parallel()
	fixture := newAPIFixture(t)

	if recorder := fixture.do(t, http.MethodGet, "/api/v1/admin/accounts", "", ""); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list status = %d, want 401", recorder.Code)
	}
	if recorder := fixture.do(t, http.MethodGet, "/api/v1/admin/accounts", "", "not-a-token"); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("invalid token status = %d, want 401", recorder.Code)
	}
	token := fixture.login(t)

	me := fixture.do(t, http.MethodGet, "/api/v1/admin/auth/me", "", token)
	if me.Code != http.StatusOK {
		t.Fatalf("me status = %d body = %s", me.Code, me.Body.String())
	}
	if strings.Contains(me.Body.String(), "argon2") || strings.Contains(me.Body.String(), "password") {
		t.Fatalf("me response leaked credential material: %s", me.Body.String())
	}

	created := fixture.do(t, http.MethodPost, "/api/v1/admin/accounts",
		`{"kind":"developer","role":"developer","email":"Dev@Example.com"}`, token)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d body = %s", created.Code, created.Body.String())
	}
	var createdBody struct {
		Account    map[string]any `json:"account"`
		SetupToken string         `json:"setup_token"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createdBody); err != nil {
		t.Fatalf("decode create body: %v", err)
	}
	if createdBody.SetupToken == "" {
		t.Fatal("create response is missing setup_token")
	}
	if createdBody.Account["email"] != "dev@example.com" {
		t.Fatalf("created email = %v, want normalised", createdBody.Account["email"])
	}
	accountID, _ := createdBody.Account["id"].(string)

	if recorder := fixture.do(t, http.MethodPatch, "/api/v1/admin/accounts/"+accountID,
		`{"status":"disabled"}`, token); recorder.Code != http.StatusOK {
		t.Fatalf("patch status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if recorder := fixture.do(t, http.MethodGet, "/api/v1/admin/accounts/"+accountID, "", token); recorder.Code != http.StatusOK {
		t.Fatalf("get account status = %d", recorder.Code)
	}
	if recorder := fixture.do(t, http.MethodGet, "/api/v1/admin/accounts/10000000-0000-0000-0000-0000000000ff", "", token); recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown account status = %d, want 404", recorder.Code)
	}
	if recorder := fixture.do(t, http.MethodGet, "/api/v1/admin/accounts/not-a-uuid/extra", "", token); recorder.Code != http.StatusNotFound {
		t.Fatalf("malformed account path status = %d, want 404", recorder.Code)
	}

	list := fixture.do(t, http.MethodGet, "/api/v1/admin/accounts", "", token)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "dev@example.com") {
		t.Fatalf("list status = %d body = %s", list.Code, list.Body.String())
	}

	if recorder := fixture.do(t, http.MethodPost, "/api/v1/admin/accounts/"+accountID+"/password-reset", "", token); recorder.Code != http.StatusOK {
		t.Fatalf("password-reset status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if recorder := fixture.do(t, http.MethodPost, "/api/v1/admin/accounts", `{"kind":"developer","role":"developer","email":"dev@example.com"}`, token); recorder.Code != http.StatusConflict {
		t.Fatalf("duplicate email status = %d, want 409", recorder.Code)
	}
	if recorder := fixture.do(t, http.MethodPost, "/api/v1/admin/accounts", `{"kind":"staff","role":"super_admin","email":"x@example.com","unknown":true}`, token); recorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d, want 400", recorder.Code)
	}
}

func TestLoginFailureModesAndRateLimiting(t *testing.T) {
	t.Parallel()
	fixture := newAPIFixture(t)

	if recorder := fixture.do(t, http.MethodPost, "/api/v1/admin/auth/login",
		`{"email":"ops@example.com","password":"wrong horse battery"}`, ""); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password status = %d, want 401", recorder.Code)
	}
	if recorder := fixture.do(t, http.MethodPost, "/api/v1/admin/auth/login", `{"email":""}`, ""); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("empty credentials status = %d, want 401", recorder.Code)
	}
	if recorder := fixture.do(t, http.MethodPost, "/api/v1/admin/auth/login", `{"email":"ops@example.com"}`, ""); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("missing password status = %d, want 401", recorder.Code)
	}

	token := fixture.login(t)
	if recorder := fixture.do(t, http.MethodPost, "/api/v1/admin/auth/logout", "", token); recorder.Code != http.StatusOK {
		t.Fatalf("logout status = %d", recorder.Code)
	}
	if recorder := fixture.do(t, http.MethodGet, "/api/v1/admin/auth/me", "", token); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("me after logout status = %d, want 401", recorder.Code)
	}

	fixture.limiter.allowed = false
	if recorder := fixture.do(t, http.MethodPost, "/api/v1/admin/auth/login",
		`{"email":"ops@example.com","password":"correct horse battery"}`, ""); recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("rate limited status = %d, want 429", recorder.Code)
	}
	if !strings.Contains(fixture.limiter.lastKey, "email:ops@example.com") {
		t.Fatalf("rate limit key = %q, want it keyed by email", fixture.limiter.lastKey)
	}

	fixture.limiter.allowed = true
	fixture.limiter.failure = errors.New("redis down")
	if recorder := fixture.do(t, http.MethodPost, "/api/v1/admin/auth/login",
		`{"email":"ops@example.com","password":"correct horse battery"}`, ""); recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("limiter failure status = %d, want 503 (fail closed)", recorder.Code)
	}
}

func TestCORSOnlyReflectsAllowlistedOrigins(t *testing.T) {
	t.Parallel()
	fixture := newAPIFixture(t)

	allowed := httptest.NewRequest(http.MethodOptions, "/api/v1/admin/auth/login", nil)
	allowed.Header.Set("Origin", "https://admin.example.com")
	allowed.Header.Set("Access-Control-Request-Method", "POST")
	allowedRecorder := httptest.NewRecorder()
	fixture.server.ServeHTTP(allowedRecorder, allowed)
	if allowedRecorder.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", allowedRecorder.Code)
	}
	if got := allowedRecorder.Header().Get("Access-Control-Allow-Origin"); got != "https://admin.example.com" {
		t.Fatalf("allow-origin = %q", got)
	}

	blocked := httptest.NewRequest(http.MethodOptions, "/api/v1/admin/auth/login", nil)
	blocked.Header.Set("Origin", "https://evil.example.com")
	blockedRecorder := httptest.NewRecorder()
	fixture.server.ServeHTTP(blockedRecorder, blocked)
	if got := blockedRecorder.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("non-allowlisted origin received allow-origin %q", got)
	}
}

func TestClientIPHonoursTrustedProxyHeaderOnlyWhenConfigured(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/auth/login", nil)
	request.RemoteAddr = "203.0.113.10:44321"
	request.Header.Set("X-Forwarded-For", "198.51.100.7, 10.0.0.1")
	if got := clientIP(request, ""); got != "203.0.113.10" {
		t.Fatalf("clientIP without a trusted proxy header = %q, want the connection address", got)
	}
	if got := clientIP(request, "X-Forwarded-For"); got != "198.51.100.7" {
		t.Fatalf("clientIP with a trusted proxy header = %q, want the first forwarded entry", got)
	}
}
func TestSetPasswordRedeemsSetupTokenOnlyForTheMatchingPortal(t *testing.T) {
	t.Parallel()
	fixture := newAPIFixture(t)
	ctx := context.Background()
	session := fixture.login(t)
	actor := portalaccount.ActorFromAccount(fixture.staff)

	adminInvite, err := fixture.accounts.CreateAccount(ctx, actor, portalaccount.CreateAccountInput{
		Kind: portalaccount.KindStaff, Role: portalaccount.RoleSuperAdmin, Email: "second@example.com",
	})
	if err != nil {
		t.Fatalf("CreateAccount(staff) error = %v", err)
	}
	developerInvite, err := fixture.accounts.CreateAccount(ctx, actor, portalaccount.CreateAccountInput{
		Kind: portalaccount.KindDeveloper, Role: portalaccount.RoleDeveloper, Email: "dev@example.com",
	})
	if err != nil {
		t.Fatalf("CreateAccount(developer) error = %v", err)
	}

	// 跨门户兑换被拒绝：developer 的 token 不能用于 admin 前缀。
	crossPortal := fixture.do(t, http.MethodPost, "/api/v1/admin/auth/set-password",
		`{"token":"`+developerInvite.SetupToken+`","new_password":"a brand new passphrase"}`, "")
	if crossPortal.Code != http.StatusUnauthorized {
		t.Fatalf("cross-portal set-password status = %d body = %s, want 401", crossPortal.Code, crossPortal.Body.String())
	}

	weak := fixture.do(t, http.MethodPost, "/api/v1/admin/auth/set-password",
		`{"token":"`+adminInvite.SetupToken+`","new_password":"short"}`, "")
	if weak.Code != http.StatusBadRequest {
		t.Fatalf("weak password status = %d, want 400", weak.Code)
	}

	redeemed := fixture.do(t, http.MethodPost, "/api/v1/admin/auth/set-password",
		`{"token":"`+adminInvite.SetupToken+`","new_password":"a brand new passphrase"}`, "")
	if redeemed.Code != http.StatusOK {
		t.Fatalf("set-password status = %d body = %s", redeemed.Code, redeemed.Body.String())
	}
	if strings.Contains(redeemed.Body.String(), "argon2") {
		t.Fatal("set-password response leaked the password hash")
	}
	replay := fixture.do(t, http.MethodPost, "/api/v1/admin/auth/set-password",
		`{"token":"`+adminInvite.SetupToken+`","new_password":"another brand new passphrase"}`, "")
	if replay.Code != http.StatusConflict {
		t.Fatalf("replay status = %d, want 409", replay.Code)
	}

	login := fixture.do(t, http.MethodPost, "/api/v1/admin/auth/login",
		`{"email":"second@example.com","password":"a brand new passphrase"}`, "")
	if login.Code != http.StatusOK {
		t.Fatalf("login with the redeemed password status = %d body = %s", login.Code, login.Body.String())
	}
	if recorder := fixture.do(t, http.MethodPost, "/api/v1/admin/auth/set-password", `{}`, ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("empty set-password body status = %d, want 400", recorder.Code)
	}
	_ = session
}
