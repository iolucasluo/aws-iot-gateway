package platformapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/manufacturing"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/portalaccount"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/project"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/credentialcipher"
)

const (
	manufacturerPassword  = "factory portal passphrase"
	factoryAdminEmail     = "factory-admin@example.com"
	factoryOperatorEmail  = "factory-operator@example.com"
	staffOpsEmail         = "ops-staff@example.com"
	factoryManufacturerID = "20000000-0000-0000-0000-0000000000c1"
	otherManufacturerID   = "20000000-0000-0000-0000-0000000000c2"
	factoryProjectID      = "50000000-0000-0000-0000-0000000000c1"
	factoryProductPID     = "feeder-x1"
)

type manufacturerFixture struct {
	server      *Server
	domain      *manufacturing.MemoryRepository
	projectRepo *project.MemoryRepository
}

func newManufacturerFixture(t *testing.T) manufacturerFixture {
	t.Helper()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	accountRepository := portalaccount.NewMemoryRepository()
	accounts, err := portalaccount.NewService(accountRepository, func() time.Time { return now })
	if err != nil {
		t.Fatalf("portalaccount.NewService() error = %v", err)
	}
	hash, err := portalaccount.HashPassword(manufacturerPassword)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	seed := func(id, email string, kind portalaccount.AccountKind, role, manufacturerID string) {
		if err := accountRepository.CreateAccount(context.Background(), portalaccount.Account{
			ID: id, Kind: kind, Role: role, Email: email, PasswordHash: hash, ManufacturerID: manufacturerID,
			Status: portalaccount.StatusActive, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("seed account %s: %v", email, err)
		}
	}
	seed("10000000-0000-0000-0000-0000000000c1", factoryAdminEmail,
		portalaccount.KindManufacturer, portalaccount.RoleManufacturerAdmin, factoryManufacturerID)
	seed("10000000-0000-0000-0000-0000000000c2", factoryOperatorEmail,
		portalaccount.KindManufacturer, portalaccount.RoleManufacturerOperator, factoryManufacturerID)
	seed("10000000-0000-0000-0000-0000000000c3", staffOpsEmail,
		portalaccount.KindStaff, portalaccount.RoleSuperAdmin, "")

	// 项目域：一个属于本厂商的 OEM 项目（供 /manufacturer/projects 与批次准入使用）。
	projectRepository := project.NewMemoryRepository()
	cipherKey := make([]byte, credentialcipher.KeyBytes)
	credentialCipher, err := credentialcipher.New(cipherKey, "kek-v1")
	if err != nil {
		t.Fatalf("credentialcipher.New() error = %v", err)
	}
	projects, err := project.NewService(projectRepository, credentialCipher, func() time.Time { return now })
	if err != nil {
		t.Fatalf("project.NewService() error = %v", err)
	}
	if err := projectRepository.CreateProject(context.Background(), project.Project{
		ID: factoryProjectID, OwnerAccountID: "10000000-0000-0000-0000-0000000000cf", Type: project.TypeOEM,
		Name: "OEM Line", ManufacturerID: factoryManufacturerID, Status: project.StatusActive,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed oem project: %v", err)
	}

	domainRepository := manufacturing.NewMemoryRepository()
	domainRepository.AddProjectScope(manufacturing.ProjectScope{
		ProjectID: factoryProjectID, ProjectType: "oem", ManufacturerID: factoryManufacturerID,
		Status: manufacturing.StatusActive, AllowedPIDs: []string{factoryProductPID},
	})
	manufacturingService, err := manufacturing.NewService(domainRepository, func() time.Time { return now })
	if err != nil {
		t.Fatalf("manufacturing.NewService() error = %v", err)
	}
	server, err := New(Dependencies{
		Accounts: accounts, Projects: projects, Manufacturing: manufacturingService,
		Audit: &stubAuditRecorder{},
	}, &stubLimiter{allowed: true}, Options{LoginRateLimit: 5, LoginRateWindow: 15 * time.Minute})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return manufacturerFixture{server: server, domain: domainRepository, projectRepo: projectRepository}
}

func (fixture manufacturerFixture) call(t *testing.T, method, path, body, token string) *httptest.ResponseRecorder {
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

func (fixture manufacturerFixture) login(t *testing.T, audience, email string) string {
	t.Helper()
	recorder := fixture.call(t, http.MethodPost, "/api/v1/"+audience+"/auth/login",
		`{"email":"`+email+`","password":"`+manufacturerPassword+`"}`, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("%s login status = %d body = %s", audience, recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	return payload.Token
}

func TestManufacturerPortalSessionIsAnIsolatedAudience(t *testing.T) {
	t.Parallel()
	fixture := newManufacturerFixture(t)
	factoryToken := fixture.login(t, "manufacturer", factoryAdminEmail)
	staffToken := fixture.login(t, "admin", staffOpsEmail)

	if recorder := fixture.call(t, http.MethodGet, "/api/v1/manufacturer/stations", "", factoryToken); recorder.Code != http.StatusOK {
		t.Fatalf("manufacturer stations status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	// 厂商会话不能进 admin / developer 前缀。
	for _, path := range []string{"/api/v1/admin/accounts", "/api/v1/developer/projects"} {
		if recorder := fixture.call(t, http.MethodGet, path, "", factoryToken); recorder.Code != http.StatusUnauthorized {
			t.Errorf("manufacturer token on %s status = %d, want 401", path, recorder.Code)
		}
	}
	// staff 会话不能进 manufacturer 前缀。
	if recorder := fixture.call(t, http.MethodGet, "/api/v1/manufacturer/stations", "", staffToken); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("staff token on manufacturer prefix status = %d, want 401", recorder.Code)
	}
}

func TestManufacturerStationRunAndReportFlow(t *testing.T) {
	t.Parallel()
	fixture := newManufacturerFixture(t)
	adminToken := fixture.login(t, "manufacturer", factoryAdminEmail)
	operatorToken := fixture.login(t, "manufacturer", factoryOperatorEmail)

	createdStation := fixture.call(t, http.MethodPost, "/api/v1/manufacturer/stations",
		`{"code":"ST-01","name":"工位一"}`, adminToken)
	if createdStation.Code != http.StatusCreated {
		t.Fatalf("create station status = %d body = %s", createdStation.Code, createdStation.Body.String())
	}
	stationID := decodeJSON[struct {
		Station struct {
			ID string `json:"id"`
		} `json:"station"`
	}](t, createdStation).Station.ID
	// 操作员不能建工位。
	if recorder := fixture.call(t, http.MethodPost, "/api/v1/manufacturer/stations",
		`{"code":"ST-02","name":"x"}`, operatorToken); recorder.Code != http.StatusForbidden {
		t.Fatalf("operator create station status = %d, want 403", recorder.Code)
	}
	if recorder := fixture.call(t, http.MethodPatch, "/api/v1/manufacturer/stations/"+stationID,
		`{"status":"disabled"}`, operatorToken); recorder.Code != http.StatusForbidden {
		t.Fatalf("operator patch station status = %d, want 403", recorder.Code)
	}

	createdRun := fixture.call(t, http.MethodPost, "/api/v1/manufacturer/production-runs",
		`{"project_id":"`+factoryProjectID+`","product_pid":"`+factoryProductPID+`","batch_code":"BATCH-001"}`, operatorToken)
	if createdRun.Code != http.StatusCreated {
		t.Fatalf("create run status = %d body = %s", createdRun.Code, createdRun.Body.String())
	}
	runID := decodeJSON[struct {
		Run struct {
			ID string `json:"id"`
		} `json:"run"`
	}](t, createdRun).Run.ID

	// 产品不在项目白名单：拒绝且给出稳定错误码。
	if recorder := fixture.call(t, http.MethodPost, "/api/v1/manufacturer/production-runs",
		`{"project_id":"`+factoryProjectID+`","product_pid":"lamp-x1","batch_code":"BATCH-002"}`, operatorToken); recorder.Code != http.StatusBadRequest {
		t.Fatalf("create run with foreign product status = %d, want 400", recorder.Code)
	}

	submitted := fixture.call(t, http.MethodPost, "/api/v1/manufacturer/production-runs/"+runID+"/station-reports",
		`{"station_id":"`+stationID+`","unit_results":[{"device_sn":"fdr-00000000000000000000000000000001","result":"pass"},{"device_sn":"fdr-00000000000000000000000000000002","result":"fail"}]}`,
		operatorToken)
	if submitted.Code != http.StatusCreated {
		t.Fatalf("submit report status = %d body = %s", submitted.Code, submitted.Body.String())
	}
	report := decodeJSON[struct {
		StationReport struct {
			UnitsTotal    int    `json:"units_total"`
			UnitsPassed   int    `json:"units_passed"`
			UnitsFailed   int    `json:"units_failed"`
			OverallResult string `json:"overall_result"`
			SequenceNo    int    `json:"sequence_no"`
		} `json:"station_report"`
	}](t, submitted).StationReport
	if report.UnitsTotal != 2 || report.UnitsPassed != 1 || report.UnitsFailed != 1 ||
		report.OverallResult != "fail" || report.SequenceNo != 1 {
		t.Fatalf("report = %+v", report)
	}
	list := fixture.call(t, http.MethodGet, "/api/v1/manufacturer/production-runs/"+runID+"/station-reports", "", adminToken)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "fdr-00000000000000000000000000000001") {
		t.Fatalf("list reports status = %d body = %s", list.Code, list.Body.String())
	}
	if reports := fixture.call(t, http.MethodGet, "/api/v1/manufacturer/test-reports", "", adminToken); reports.Code != http.StatusOK {
		t.Fatalf("test-reports status = %d body = %s", reports.Code, reports.Body.String())
	}
	// 项目只读视图：厂商只能看到自己名下的项目。
	projects := fixture.call(t, http.MethodGet, "/api/v1/manufacturer/projects", "", adminToken)
	if projects.Code != http.StatusOK {
		t.Fatalf("manufacturer projects status = %d body = %s", projects.Code, projects.Body.String())
	}
	if !strings.Contains(projects.Body.String(), factoryProjectID) {
		t.Fatalf("manufacturer projects = %s", projects.Body.String())
	}

	// 关闭后拒绝新报告。
	if closed := fixture.call(t, http.MethodPost, "/api/v1/manufacturer/production-runs/"+runID+"/close", "", adminToken); closed.Code != http.StatusOK {
		t.Fatalf("close run status = %d body = %s", closed.Code, closed.Body.String())
	}
	if after := fixture.call(t, http.MethodPost, "/api/v1/manufacturer/production-runs/"+runID+"/station-reports",
		`{"station_id":"`+stationID+`","unit_results":[{"device_sn":"fdr-3","result":"pass"}]}`, operatorToken); after.Code != http.StatusConflict {
		t.Fatalf("submit report on closed run status = %d, want 409", after.Code)
	}
}

func TestAdminManufacturerMasterDataRoutes(t *testing.T) {
	t.Parallel()
	fixture := newManufacturerFixture(t)
	staffToken := fixture.login(t, "admin", staffOpsEmail)

	created := fixture.call(t, http.MethodPost, "/api/v1/admin/manufacturers",
		`{"code":"FAC-01","name":"代工厂一","contact_email":"Contact@Example.com"}`, staffToken)
	if created.Code != http.StatusCreated {
		t.Fatalf("create manufacturer status = %d body = %s", created.Code, created.Body.String())
	}
	payload := decodeJSON[struct {
		Manufacturer struct {
			ID           string `json:"id"`
			ContactEmail string `json:"contact_email"`
		} `json:"manufacturer"`
	}](t, created)
	if payload.Manufacturer.ContactEmail != "contact@example.com" {
		t.Fatalf("contact email = %q, want trimmed and lowercased", payload.Manufacturer.ContactEmail)
	}
	if list := fixture.call(t, http.MethodGet, "/api/v1/admin/manufacturers", "", staffToken); list.Code != http.StatusOK {
		t.Fatalf("list manufacturers status = %d body = %s", list.Code, list.Body.String())
	}
	if patched := fixture.call(t, http.MethodPatch, "/api/v1/admin/manufacturers/"+payload.Manufacturer.ID,
		`{"status":"disabled"}`, staffToken); patched.Code != http.StatusOK {
		t.Fatalf("patch manufacturer status = %d body = %s", patched.Code, patched.Body.String())
	}
	if recorder := fixture.call(t, http.MethodPost, "/api/v1/admin/manufacturers",
		`{"code":"BAD CODE","name":"x"}`, staffToken); recorder.Code != http.StatusBadRequest {
		t.Fatalf("create manufacturer with bad code status = %d, want 400", recorder.Code)
	}
}
