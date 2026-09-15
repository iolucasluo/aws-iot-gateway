package platformapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/operations"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/portalaccount"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/project"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/credentialcipher"
)

const (
	adminPassword      = "admin ops passphrase"
	adminEmail         = "ops-admin@example.com"
	adminAccountID     = "10000000-0000-0000-0000-0000000000f1"
	opsFixtureDeviceSN = "fdr-00000000000000000000000000000f01"
)

// stubOperationsRepository 让 admin 契约测试不依赖数据库。
type stubOperationsRepository struct {
	devices         []operations.Device
	bindings        []operations.Binding
	activations     []operations.Activation
	auditLogs       []operations.AuditEntry
	lastDevice      operations.DeviceFilter
	lastAudit       operations.AuditFilter
	lastToken       operations.DeviceTokenFilter
	lastCertificate operations.CertificateFilter
	tokens          []operations.DeviceToken
	certificates    []operations.Certificate
	failDevices     bool
}

func (stub *stubOperationsRepository) ListDevices(_ context.Context, filter operations.DeviceFilter) ([]operations.Device, error) {
	stub.lastDevice = filter
	if stub.failDevices {
		return nil, operations.ErrInvalidQuery
	}
	if filter.SubjectID == "" {
		return stub.devices, nil
	}
	// 真实实现按 active binding 的主体过滤；stub 保持同样的可见性，
	// 否则契约测试会看到本不该出现的设备。
	scoped := make([]operations.Device, 0, len(stub.devices))
	for _, device := range stub.devices {
		if device.BindingProjectID == filter.SubjectID {
			scoped = append(scoped, device)
		}
	}
	return scoped, nil
}

func (stub *stubOperationsRepository) FindDevice(_ context.Context, deviceSN string) (operations.Device, error) {
	for _, device := range stub.devices {
		if device.DeviceSN == deviceSN {
			return device, nil
		}
	}
	return operations.Device{}, operations.ErrInvalidQuery
}

func (stub *stubOperationsRepository) ListBindings(_ context.Context, _ operations.BindingFilter) ([]operations.Binding, error) {
	return stub.bindings, nil
}

func (stub *stubOperationsRepository) ListActivations(_ context.Context, _ operations.ActivationFilter) ([]operations.Activation, error) {
	return stub.activations, nil
}

func (stub *stubOperationsRepository) ListAuditLogs(_ context.Context, filter operations.AuditFilter) ([]operations.AuditEntry, error) {
	stub.lastAudit = filter
	return stub.auditLogs, nil
}

func (stub *stubOperationsRepository) ListDeviceTokens(_ context.Context, filter operations.DeviceTokenFilter) ([]operations.DeviceToken, error) {
	stub.lastToken = filter
	return stub.tokens, nil
}

func (stub *stubOperationsRepository) ListCertificates(_ context.Context, filter operations.CertificateFilter) ([]operations.Certificate, error) {
	stub.lastCertificate = filter
	return stub.certificates, nil
}

type adminFixture struct {
	server *Server
	stub   *stubOperationsRepository
}

func newAdminFixture(t *testing.T, withOperations bool) adminFixture {
	t.Helper()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	accountRepository := portalaccount.NewMemoryRepository()
	accounts, err := portalaccount.NewService(accountRepository, func() time.Time { return now })
	if err != nil {
		t.Fatalf("portalaccount.NewService() error = %v", err)
	}
	hash, err := portalaccount.HashPassword(adminPassword)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if err := accountRepository.CreateAccount(context.Background(), portalaccount.Account{
		ID: adminAccountID, Kind: portalaccount.KindStaff, Role: portalaccount.RoleSuperAdmin,
		Email: adminEmail, PasswordHash: hash, Status: portalaccount.StatusActive,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed staff account: %v", err)
	}
	key := make([]byte, credentialcipher.KeyBytes)
	credentialCipher, err := credentialcipher.New(key, "kek-v1")
	if err != nil {
		t.Fatalf("credentialcipher.New() error = %v", err)
	}
	projects, err := project.NewService(project.NewMemoryRepository(), credentialCipher, func() time.Time { return now })
	if err != nil {
		t.Fatalf("project.NewService() error = %v", err)
	}
	stub := &stubOperationsRepository{
		devices: []operations.Device{{
			ID: "018f0000-0000-7000-8000-0000000000f1", DeviceSN: opsFixtureDeviceSN, PID: "feeder-x1",
			Status: "active", Online: true, CertificateID: "cert-1", CreatedAt: now,
			BindingProjectID: "50000000-0000-0000-0000-0000000000f1", BindingExternalUserID: "user-1",
		}},
		bindings: []operations.Binding{{
			ID: "018f0000-0000-7000-8000-0000000000f2", DeviceSN: opsFixtureDeviceSN, PID: "feeder-x1",
			SubjectID: "50000000-0000-0000-0000-0000000000f1", BindingExternalUserID: "user-1",
			Status: "active", ActivatedAt: now, CreatedAt: now,
		}},
		activations: []operations.Activation{{
			ID: "018f0000-0000-7000-8000-0000000000f3", PID: "feeder-x1", DeviceSN: opsFixtureDeviceSN,
			SubjectID: "50000000-0000-0000-0000-0000000000f1", BindingExternalUserID: "user-1",
			Method: "ble", Status: "activated", CreatedAt: now, UpdatedAt: now,
		}},
		auditLogs: []operations.AuditEntry{{
			ID: "018f0000-0000-7000-8000-0000000000f4", ActorKind: "portal_account",
			ActorAccountID: adminAccountID, ActorLabel: adminEmail, Operation: "POST auth.login",
			Outcome: "succeeded", Metadata: map[string]any{"status": 200}, CreatedAt: now,
		}},
		tokens: []operations.DeviceToken{{
			ID: "018f0000-0000-7000-8000-0000000000f5", BindingID: "018f0000-0000-7000-8000-0000000000f2",
			DeviceSN: opsFixtureDeviceSN, SubjectID: "50000000-0000-0000-0000-0000000000f1",
			ExpiresAt: now.Add(24 * time.Hour), CreatedAt: now,
		}},
		certificates: []operations.Certificate{{
			DeviceSN: opsFixtureDeviceSN, PID: "feeder-x1", Status: "active",
			CertificateID: "cert-1", CreatedAt: now, UpdatedAt: now,
		}},
	}
	dependencies := Dependencies{Accounts: accounts, Projects: projects}
	if withOperations {
		operationsService, err := operations.NewQueryService(stub)
		if err != nil {
			t.Fatalf("operations.NewQueryService() error = %v", err)
		}
		dependencies.Operations = operationsService
	}
	server, err := New(dependencies, &stubLimiter{allowed: true}, Options{
		LoginRateLimit: 5, LoginRateWindow: 15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return adminFixture{server: server, stub: stub}
}

func (fixture adminFixture) call(t *testing.T, method, path, body, token string) *httptest.ResponseRecorder {
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

func (fixture adminFixture) login(t *testing.T) string {
	t.Helper()
	recorder := fixture.call(t, http.MethodPost, "/api/v1/admin/auth/login",
		`{"email":"`+adminEmail+`","password":"`+adminPassword+`"}`, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("staff login status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	return payload.Token
}

func TestAdminOperationsViewsAreReadOnlyAndScoped(t *testing.T) {
	t.Parallel()
	fixture := newAdminFixture(t, true)
	token := fixture.login(t)

	devices := fixture.call(t, http.MethodGet, "/api/v1/admin/devices?pid=feeder-x1&online=true&limit=1", "", token)
	if devices.Code != http.StatusOK {
		t.Fatalf("devices status = %d body = %s", devices.Code, devices.Body.String())
	}
	payload := decodeJSON[struct {
		Items []struct {
			DeviceSN string `json:"device_sn"`
			Online   bool   `json:"online"`
			Binding  *struct {
				SubjectID string `json:"subject_id"`
			} `json:"binding"`
		} `json:"items"`
		NextCursor string `json:"next_cursor"`
	}](t, devices)
	if len(payload.Items) != 1 || payload.Items[0].DeviceSN != opsFixtureDeviceSN || !payload.Items[0].Online {
		t.Fatalf("device items = %+v", payload.Items)
	}
	if payload.Items[0].Binding == nil || payload.Items[0].Binding.SubjectID == "" {
		t.Fatalf("device projection lost its binding: %+v", payload.Items[0])
	}
	// 整页返回时必须给出游标，且游标可被回传。
	if payload.NextCursor == "" {
		t.Fatal("full page did not return next_cursor")
	}
	if fixture.stub.lastDevice.PID != "feeder-x1" || fixture.stub.lastDevice.Online == nil || !*fixture.stub.lastDevice.Online {
		t.Fatalf("device filter was not applied: %+v", fixture.stub.lastDevice)
	}
	if again := fixture.call(t, http.MethodGet, "/api/v1/admin/devices?limit=1&cursor="+payload.NextCursor, "", token); again.Code != http.StatusOK {
		t.Fatalf("cursor round trip status = %d body = %s", again.Code, again.Body.String())
	}

	detail := fixture.call(t, http.MethodGet, "/api/v1/admin/devices/"+opsFixtureDeviceSN, "", token)
	if detail.Code != http.StatusOK {
		t.Fatalf("device detail status = %d body = %s", detail.Code, detail.Body.String())
	}
	if missing := fixture.call(t, http.MethodGet, "/api/v1/admin/devices/fdr-unknown", "", token); missing.Code != http.StatusNotFound {
		t.Fatalf("unknown device status = %d, want 404", missing.Code)
	}
	for name, path := range map[string]string{
		"bindings":      "/api/v1/admin/bindings?subject_id=50000000-0000-0000-0000-0000000000f1",
		"device-tokens": "/api/v1/admin/device-tokens?device_sn=" + opsFixtureDeviceSN,
		"certificates":  "/api/v1/admin/certificates?device_sn=" + opsFixtureDeviceSN,
		"activations":   "/api/v1/admin/activations?status=activated",
		"audit-logs":    "/api/v1/admin/audit-logs?operation=POST%20auth.login",
		"projects":      "/api/v1/admin/projects",
		"products":      "/api/v1/admin/products",
	} {
		recorder := fixture.call(t, http.MethodGet, path, "", token)
		if recorder.Code != http.StatusOK {
			t.Errorf("%s status = %d body = %s", name, recorder.Code, recorder.Body.String())
		}
	}
	if fixture.stub.lastAudit.Operation != "POST auth.login" {
		t.Fatalf("audit filter was not applied: %+v", fixture.stub.lastAudit)
	}
	// credentials 必须显式指定 project_id，避免悄悄返回全量。
	if recorder := fixture.call(t, http.MethodGet, "/api/v1/admin/credentials", "", token); recorder.Code != http.StatusBadRequest {
		t.Fatalf("credentials without project_id status = %d, want 400", recorder.Code)
	}
}

func TestAdminOperationsViewsRejectBadPagingAndOtherAudiences(t *testing.T) {
	t.Parallel()
	fixture := newAdminFixture(t, true)
	token := fixture.login(t)

	for name, path := range map[string]string{
		"limit-too-high": "/api/v1/admin/devices?limit=500",
		"limit-not-int":  "/api/v1/admin/devices?limit=many",
		"bad-cursor":     "/api/v1/admin/devices?cursor=!!!",
		"bad-online":     "/api/v1/admin/devices?online=maybe",
		"bad-from":       "/api/v1/admin/audit-logs?from=yesterday",
	} {
		if recorder := fixture.call(t, http.MethodGet, path, "", token); recorder.Code != http.StatusBadRequest {
			t.Errorf("%s status = %d, want 400", name, recorder.Code)
		}
	}
	if recorder := fixture.call(t, http.MethodGet, "/api/v1/admin/devices", "", ""); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("devices without a session status = %d, want 401", recorder.Code)
	}
	if recorder := fixture.call(t, http.MethodGet, "/api/v1/admin/devices", "", "not-a-token"); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("devices with a bogus token status = %d, want 401", recorder.Code)
	}
}

// 缺少只读模型时运维视图整体 503，而账号治理不受影响。
func TestAdminOperationsViewsFailClosedWithoutTheReadModel(t *testing.T) {
	t.Parallel()
	fixture := newAdminFixture(t, false)
	token := fixture.login(t)

	if recorder := fixture.call(t, http.MethodGet, "/api/v1/admin/devices", "", token); recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("devices without a read model status = %d, want 503", recorder.Code)
	}
	if recorder := fixture.call(t, http.MethodGet, "/api/v1/admin/accounts", "", token); recorder.Code != http.StatusOK {
		t.Fatalf("accounts must not depend on the read model, status = %d", recorder.Code)
	}
}
