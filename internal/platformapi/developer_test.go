package platformapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/audit"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/operations"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/portalaccount"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/project"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/credentialcipher"
)

const (
	developerPassword  = "developer portal passphrase"
	developerOneEmail  = "dev-one@example.com"
	developerTwoEmail  = "dev-two@example.com"
	developerOneID     = "10000000-0000-0000-0000-0000000000e1"
	developerTwoID     = "10000000-0000-0000-0000-0000000000e2"
	staffID            = "10000000-0000-0000-0000-0000000000e3"
	developerDPProfile = "feeder-v3"
)

type stubAuditRecorder struct {
	mutex   sync.Mutex
	entries []audit.Entry
	fail    error
}

func (recorder *stubAuditRecorder) Record(_ context.Context, entry audit.Entry) error {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	if recorder.fail != nil {
		return recorder.fail
	}
	recorder.entries = append(recorder.entries, entry)
	return nil
}

func (recorder *stubAuditRecorder) recorded() []audit.Entry {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	return append([]audit.Entry{}, recorder.entries...)
}

type developerFixture struct {
	server     *Server
	repository *project.MemoryRepository
	projects   *project.Service
	audit      *stubAuditRecorder
	ops        *stubOperationsRepository
}

func newDeveloperFixture(t *testing.T) developerFixture {
	t.Helper()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	accountRepository := portalaccount.NewMemoryRepository()
	accounts, err := portalaccount.NewService(accountRepository, func() time.Time { return now })
	if err != nil {
		t.Fatalf("portalaccount.NewService() error = %v", err)
	}
	hash, err := portalaccount.HashPassword(developerPassword)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	for id, email := range map[string]string{
		developerOneID: developerOneEmail,
		developerTwoID: developerTwoEmail,
	} {
		if err := accountRepository.CreateAccount(context.Background(), portalaccount.Account{
			ID: id, Kind: portalaccount.KindDeveloper, Role: portalaccount.RoleDeveloper,
			Email: email, PasswordHash: hash, Status: portalaccount.StatusActive,
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("seed developer account: %v", err)
		}
	}
	if err := accountRepository.CreateAccount(context.Background(), portalaccount.Account{
		ID: staffID, Kind: portalaccount.KindStaff, Role: portalaccount.RoleSuperAdmin,
		Email: "staff-portal@example.com", PasswordHash: hash, Status: portalaccount.StatusActive,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed staff account: %v", err)
	}

	key := make([]byte, credentialcipher.KeyBytes)
	credentialCipher, err := credentialcipher.New(key, "kek-v1")
	if err != nil {
		t.Fatalf("credentialcipher.New() error = %v", err)
	}
	projectRepository := project.NewMemoryRepository()
	projectRepository.AddDPProfile(developerDPProfile, "Feeder v3")
	// 生产里 portal_accounts 只有一个事实源；内存 fixture 需要显式登记同一批账号，
	// 否则产品归属校验会查不到 owner。
	for id, email := range map[string]string{
		developerOneID: developerOneEmail,
		developerTwoID: developerTwoEmail,
		staffID:        "staff-portal@example.com",
	} {
		kind := portalaccount.KindDeveloper
		role := portalaccount.RoleDeveloper
		if id == staffID {
			kind = portalaccount.KindStaff
			role = portalaccount.RoleSuperAdmin
		}
		projectRepository.AddPortalAccount(portalaccount.Account{
			ID: id, Kind: kind, Role: role, Email: email, Status: portalaccount.StatusActive,
		})
	}
	projects, err := project.NewService(projectRepository, credentialCipher, func() time.Time { return now })
	if err != nil {
		t.Fatalf("project.NewService() error = %v", err)
	}
	auditRecorder := &stubAuditRecorder{}
	opsStub := &stubOperationsRepository{}
	operationsService, err := operations.NewQueryService(opsStub)
	if err != nil {
		t.Fatalf("operations.NewQueryService() error = %v", err)
	}
	server, err := New(Dependencies{
		Accounts: accounts, Projects: projects, Audit: auditRecorder, Operations: operationsService,
	}, &stubLimiter{allowed: true}, Options{
		AllowedOrigins:  []string{"https://developer.example.com"},
		LoginRateLimit:  5,
		LoginRateWindow: 15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return developerFixture{
		server: server, repository: projectRepository, projects: projects,
		audit: auditRecorder, ops: opsStub,
	}
}

func (fixture developerFixture) call(t *testing.T, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
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

func (fixture developerFixture) login(t *testing.T, email string) string {
	t.Helper()
	recorder := fixture.call(t, http.MethodPost, "/api/v1/developer/auth/login",
		`{"email":"`+email+`","password":"`+developerPassword+`"}`, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("developer login status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	return payload.Token
}

func decodeJSON[T any](t *testing.T, recorder *httptest.ResponseRecorder) T {
	t.Helper()
	var payload T
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
	}
	return payload
}

// developer 会话不能命中 admin 前缀，反之亦然：audience 是硬边界。
func TestDeveloperSessionCannotCrossPortals(t *testing.T) {
	t.Parallel()
	fixture := newDeveloperFixture(t)
	token := fixture.login(t, developerOneEmail)

	if recorder := fixture.call(t, http.MethodGet, "/api/v1/developer/auth/me", "", token); recorder.Code != http.StatusOK {
		t.Fatalf("developer me status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if recorder := fixture.call(t, http.MethodGet, "/api/v1/admin/auth/me", "", token); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("developer token on admin prefix status = %d, want 401", recorder.Code)
	}
	if recorder := fixture.call(t, http.MethodGet, "/api/v1/admin/accounts", "", token); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("developer token on admin accounts status = %d, want 401", recorder.Code)
	}
	// staff 会话也不能进 developer 前缀。
	staff := fixture.call(t, http.MethodPost, "/api/v1/admin/auth/login",
		`{"email":"staff-portal@example.com","password":"`+developerPassword+`"}`, "")
	if staff.Code != http.StatusOK {
		t.Fatalf("staff login status = %d", staff.Code)
	}
	staffToken := decodeJSON[struct {
		Token string `json:"token"`
	}](t, staff).Token
	if recorder := fixture.call(t, http.MethodGet, "/api/v1/developer/projects", "", staffToken); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("staff token on developer prefix status = %d, want 401", recorder.Code)
	}
}

func TestDeveloperProjectCredentialAndProductFlow(t *testing.T) {
	t.Parallel()
	fixture := newDeveloperFixture(t)
	token := fixture.login(t, developerOneEmail)

	created := fixture.call(t, http.MethodPost, "/api/v1/developer/projects",
		`{"project_type":"cloud","name":"My Cloud Project"}`, token)
	if created.Code != http.StatusCreated {
		t.Fatalf("create project status = %d body = %s", created.Code, created.Body.String())
	}
	projectID := decodeJSON[struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}](t, created).Project.ID
	if projectID == "" {
		t.Fatal("create project returned an empty id")
	}

	listed := fixture.call(t, http.MethodGet, "/api/v1/developer/projects", "", token)
	if listed.Code != http.StatusOK {
		t.Fatalf("list projects status = %d", listed.Code)
	}
	if items := decodeJSON[struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}](t, listed).Items; len(items) != 1 {
		t.Fatalf("developer sees %d projects, want 1", len(items))
	}

	// 产品：先建产品，再加进项目白名单。
	productCreated := fixture.call(t, http.MethodPost, "/api/v1/developer/products",
		`{"pid":"feeder-x2","name":"Feeder X2","dp_profile_code":"`+developerDPProfile+`","capabilities":["webrtc.v1"]}`, token)
	if productCreated.Code != http.StatusCreated {
		t.Fatalf("create product status = %d body = %s", productCreated.Code, productCreated.Body.String())
	}
	productID := decodeJSON[struct {
		Product struct {
			ID string `json:"id"`
		} `json:"product"`
	}](t, productCreated).Product.ID

	attached := fixture.call(t, http.MethodPost, "/api/v1/developer/projects/"+projectID+"/products",
		`{"product_id":"`+productID+`"}`, token)
	if attached.Code != http.StatusOK {
		t.Fatalf("attach product status = %d body = %s", attached.Code, attached.Body.String())
	}
	whitelist := fixture.call(t, http.MethodGet, "/api/v1/developer/projects/"+projectID+"/products", "", token)
	if whitelist.Code != http.StatusOK {
		t.Fatalf("list project products status = %d", whitelist.Code)
	}
	if pids := decodeJSON[struct {
		PIDs []string `json:"pids"`
	}](t, whitelist).PIDs; len(pids) != 1 || pids[0] != "feeder-x2" {
		t.Fatalf("project pids = %v, want [feeder-x2]", pids)
	}

	// 凭证：secret 只在创建与轮换响应出现，列表里绝不含 secret 字段。
	issued := fixture.call(t, http.MethodPost, "/api/v1/developer/projects/"+projectID+"/credentials",
		`{"name":"primary","permissions":["devices:read"]}`, token)
	if issued.Code != http.StatusCreated {
		t.Fatalf("create credential status = %d body = %s", issued.Code, issued.Body.String())
	}
	issuedBody := decodeJSON[struct {
		Credential struct {
			ID string `json:"id"`
		} `json:"credential"`
		APISecret string `json:"api_secret"`
	}](t, issued)
	if issuedBody.APISecret == "" || issuedBody.Credential.ID == "" {
		t.Fatal("create credential did not return key material")
	}
	credentials := fixture.call(t, http.MethodGet, "/api/v1/developer/projects/"+projectID+"/credentials", "", token)
	if credentials.Code != http.StatusOK {
		t.Fatalf("list credentials status = %d", credentials.Code)
	}
	if strings.Contains(credentials.Body.String(), issuedBody.APISecret) ||
		strings.Contains(credentials.Body.String(), "api_secret") {
		t.Fatalf("credential list leaked secret material: %s", credentials.Body.String())
	}
	if recorder := fixture.call(t, http.MethodPost, "/api/v1/developer/credentials/"+issuedBody.Credential.ID+"/rotate", "", token); recorder.Code != http.StatusOK {
		t.Fatalf("rotate credential status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if recorder := fixture.call(t, http.MethodPost, "/api/v1/developer/credentials/"+issuedBody.Credential.ID+"/disable", "", token); recorder.Code != http.StatusOK {
		t.Fatalf("disable credential status = %d", recorder.Code)
	}
	if recorder := fixture.call(t, http.MethodPost, "/api/v1/developer/projects/"+projectID+"/credentials",
		`{"name":"bad","permissions":["devices:write"]}`, token); recorder.Code != http.StatusBadRequest {
		t.Fatalf("create credential with unknown permission status = %d, want 400", recorder.Code)
	}
}

// 跨开发者的资源按 404 处理，不泄露存在性；staff 不参与 developer 前缀。
func TestDeveloperScopeIsConfinedOverHTTP(t *testing.T) {
	t.Parallel()
	fixture := newDeveloperFixture(t)
	first := fixture.login(t, developerOneEmail)
	second := fixture.login(t, developerTwoEmail)

	created := fixture.call(t, http.MethodPost, "/api/v1/developer/projects",
		`{"project_type":"cloud","name":"First Only"}`, first)
	projectID := decodeJSON[struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}](t, created).Project.ID

	if recorder := fixture.call(t, http.MethodGet, "/api/v1/developer/projects/"+projectID, "", second); recorder.Code != http.StatusNotFound {
		t.Fatalf("cross-developer get status = %d, want 404", recorder.Code)
	}
	if recorder := fixture.call(t, http.MethodGet, "/api/v1/developer/projects/"+projectID+"/credentials", "", second); recorder.Code != http.StatusNotFound {
		t.Fatalf("cross-developer credentials status = %d, want 404", recorder.Code)
	}
	if recorder := fixture.call(t, http.MethodPatch, "/api/v1/developer/projects/"+projectID,
		`{"status":"disabled"}`, second); recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-developer patch status = %d, want 403", recorder.Code)
	}
	if recorder := fixture.call(t, http.MethodGet, "/api/v1/developer/dp-profiles", "", first); recorder.Code != http.StatusOK {
		t.Fatalf("dp-profiles status = %d body = %s", recorder.Code, recorder.Body.String())
	}
}

// 开发者只能看到自己项目当前绑定的设备：列表按项目过滤，详情越权按不存在处理。
func TestDeveloperDeviceViewsAreConfinedToOwnedProjects(t *testing.T) {
	t.Parallel()
	fixture := newDeveloperFixture(t)
	owner := fixture.login(t, developerOneEmail)
	stranger := fixture.login(t, developerTwoEmail)

	created := fixture.call(t, http.MethodPost, "/api/v1/developer/projects",
		`{"project_type":"cloud","name":"Device Owner"}`, owner)
	projectID := decodeJSON[struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}](t, created).Project.ID

	fixture.ops.devices = []operations.Device{
		{
			ID: "018f0000-0000-7000-8000-000000000101", DeviceSN: opsFixtureDeviceSN, PID: "feeder-x1",
			Status: "active", Online: true, CreatedAt: time.Now(),
			BindingProjectID: projectID, BindingExternalUserID: "user-1",
		},
		{
			ID: "018f0000-0000-7000-8000-000000000102", DeviceSN: "fdr-unbound", PID: "feeder-x1",
			Status: "active", CreatedAt: time.Now(),
		},
	}

	listed := fixture.call(t, http.MethodGet, "/api/v1/developer/projects/"+projectID+"/devices", "", owner)
	if listed.Code != http.StatusOK {
		t.Fatalf("project devices status = %d body = %s", listed.Code, listed.Body.String())
	}
	if fixture.ops.lastDevice.SubjectID != projectID {
		t.Fatalf("device filter subject = %q, want the project id", fixture.ops.lastDevice.SubjectID)
	}
	items := decodeJSON[struct {
		Items []struct {
			DeviceSN string `json:"device_sn"`
		} `json:"items"`
	}](t, listed).Items
	if len(items) != 1 || items[0].DeviceSN != opsFixtureDeviceSN {
		t.Fatalf("project devices = %+v", items)
	}

	if detail := fixture.call(t, http.MethodGet, "/api/v1/developer/devices/"+opsFixtureDeviceSN, "", owner); detail.Code != http.StatusOK {
		t.Fatalf("owned device detail status = %d body = %s", detail.Code, detail.Body.String())
	}
	if other := fixture.call(t, http.MethodGet, "/api/v1/developer/devices/"+opsFixtureDeviceSN, "", stranger); other.Code != http.StatusNotFound {
		t.Fatalf("cross-developer device detail status = %d, want 404", other.Code)
	}
	// 未绑定设备不属于任何项目。
	if unbound := fixture.call(t, http.MethodGet, "/api/v1/developer/devices/fdr-unbound", "", owner); unbound.Code != http.StatusNotFound {
		t.Fatalf("unbound device detail status = %d, want 404", unbound.Code)
	}
	// 别人的项目：列表按不存在处理。
	if foreign := fixture.call(t, http.MethodGet, "/api/v1/developer/projects/"+projectID+"/devices", "", stranger); foreign.Code != http.StatusNotFound {
		t.Fatalf("cross-developer project devices status = %d, want 404", foreign.Code)
	}
}
