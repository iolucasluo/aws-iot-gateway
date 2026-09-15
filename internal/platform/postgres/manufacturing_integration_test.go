package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/manufacturing"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/portalaccount"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/project"
)

const (
	integrationFactoryAdminID = "10000000-0000-0000-0000-0000000000d7"
	integrationFactoryRunID   = "70000000-0000-0000-0000-0000000000d1"
)

// seedManufacturingFixture 准备一个"厂商 + OEM 项目 + 白名单产品"的真实库状态。
func seedManufacturingFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	seedIntegrationManufacturer(t, ctx, pool)
	seedProjectOwner(t, ctx, pool)
	applyProductSeeds(t, ctx, pool)

	accounts, err := NewPortalAccountRepository(pool)
	if err != nil {
		t.Fatalf("NewPortalAccountRepository() error = %v", err)
	}
	if err := accounts.CreateAccount(ctx, portalaccount.Account{
		ID: integrationFactoryAdminID, Kind: portalaccount.KindManufacturer, Role: portalaccount.RoleManufacturerAdmin,
		Email: "factory-admin@example.com", ManufacturerID: integrationManufacturerID,
		Status:    portalaccount.StatusActive,
		CreatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed manufacturer admin: %v", err)
	}

	projects, err := NewProjectRepository(pool)
	if err != nil {
		t.Fatalf("NewProjectRepository() error = %v", err)
	}
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	if err := projects.CreateProject(ctx, project.Project{
		ID: integrationProjectID, OwnerAccountID: integrationProjectOwnerID, Type: project.TypeOEM,
		Name: "OEM Line", ManufacturerID: integrationManufacturerID,
		Status: project.StatusActive, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed oem project: %v", err)
	}
	var productID string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM products WHERE pid='feeder-x1'`).Scan(&productID); err != nil {
		t.Fatalf("read seeded product: %v", err)
	}
	if err := projects.AddProjectProduct(ctx, "70000000-0000-0000-0000-0000000000d9", integrationProjectID, productID, now); err != nil {
		t.Fatalf("attach project product: %v", err)
	}
	return productID
}

func TestManufacturingRepositoryRoundTripAndAdmissionScope(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatalf("ApplyMigrations() error = %v", err)
	}
	seedManufacturingFixture(t, ctx, pool)
	repository, err := NewManufacturingRepository(pool)
	if err != nil {
		t.Fatalf("NewManufacturingRepository() error = %v", err)
	}
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

	scope, err := repository.FindProjectScope(ctx, integrationProjectID)
	if err != nil {
		t.Fatalf("FindProjectScope() error = %v", err)
	}
	if scope.ProjectType != "oem" || scope.ManufacturerID != integrationManufacturerID ||
		len(scope.AllowedPIDs) != 1 || scope.AllowedPIDs[0] != "feeder-x1" {
		t.Fatalf("project scope = %+v", scope)
	}
	if _, err := repository.FindProjectScope(ctx, "50000000-0000-0000-0000-0000000000ff"); !errors.Is(err, manufacturing.ErrProjectNotEligible) {
		t.Fatalf("FindProjectScope(unknown) error = %v, want ErrProjectNotEligible", err)
	}

	station := manufacturing.Station{
		ID: "70000000-0000-0000-0000-0000000000e1", ManufacturerID: integrationManufacturerID,
		Code: "ST-01", Name: "工位一", Status: manufacturing.StatusActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateStation(ctx, station); err != nil {
		t.Fatalf("CreateStation() error = %v", err)
	}
	if err := repository.CreateStation(ctx, station); !errors.Is(err, manufacturing.ErrInvalidInput) {
		t.Fatalf("CreateStation(duplicate) error = %v, want ErrInvalidInput", err)
	}
	stations, err := repository.ListStations(ctx, integrationManufacturerID, manufacturing.StationFilter{})
	if err != nil || len(stations) != 1 {
		t.Fatalf("ListStations() = %d, err = %v; want 1", len(stations), err)
	}
	loaded, err := repository.FindStationByID(ctx, station.ID)
	if err != nil || loaded.Code != "ST-01" || loaded.Name != "工位一" {
		t.Fatalf("FindStationByID() = %+v, err = %v", loaded, err)
	}
	if _, err := repository.FindStationByID(ctx, "70000000-0000-0000-0000-0000000000ff"); !errors.Is(err, manufacturing.ErrStationNotFound) {
		t.Fatalf("FindStationByID(unknown) error = %v, want ErrStationNotFound", err)
	}

	planned := 100
	run := manufacturing.Run{
		ID: integrationFactoryRunID, ProjectID: integrationProjectID, ProductPID: "feeder-x1",
		ManufacturerID: integrationManufacturerID, BatchCode: "BATCH-001", PlannedQuantity: &planned,
		Status: manufacturing.RunOpen, OpenedByAccountID: integrationFactoryAdminID,
		OpenedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	if err := repository.CreateRun(ctx, run); !errors.Is(err, manufacturing.ErrInvalidInput) {
		t.Fatalf("CreateRun(duplicate batch) error = %v, want ErrInvalidInput", err)
	}
	loadedRun, err := repository.FindRunByID(ctx, run.ID)
	if err != nil || loadedRun.BatchCode != "BATCH-001" || loadedRun.PlannedQuantity == nil || *loadedRun.PlannedQuantity != 100 {
		t.Fatalf("FindRunByID() = %+v, err = %v", loadedRun, err)
	}
	runs, err := repository.ListRuns(ctx, integrationManufacturerID, manufacturing.RunFilter{ProjectID: integrationProjectID})
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListRuns() = %d, err = %v; want 1", len(runs), err)
	}
	// 其它厂商看不到这批。
	if other, err := repository.ListRuns(ctx, "20000000-0000-0000-0000-0000000000ff", manufacturing.RunFilter{}); err != nil || len(other) != 0 {
		t.Fatalf("ListRuns(other manufacturer) = %d, err = %v; want 0", len(other), err)
	}

	report := manufacturing.StationReport{
		ID: "70000000-0000-0000-0000-0000000000e2", RunID: run.ID, StationID: station.ID,
		SubmittedByAccountID: integrationFactoryAdminID, SequenceNo: 1,
		UnitsTotal: 2, UnitsPassed: 1, UnitsFailed: 1, OverallResult: manufacturing.ResultFail,
		UnitResults: []manufacturing.UnitResult{
			{DeviceSN: "fdr-00000000000000000000000000000001", Result: manufacturing.ResultPass},
			{DeviceSN: "fdr-00000000000000000000000000000002", Result: manufacturing.ResultFail},
		},
		EvidenceSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		CreatedAt:      now,
	}
	if err := repository.CreateStationReport(ctx, report); err != nil {
		t.Fatalf("CreateStationReport() error = %v", err)
	}
	// 序号在同一工位内唯一。
	if err := repository.CreateStationReport(ctx, report); !errors.Is(err, manufacturing.ErrInvalidInput) {
		t.Fatalf("CreateStationReport(duplicate sequence) error = %v, want ErrInvalidInput", err)
	}
	// 计数必须自洽（由 CHECK 兜底）。
	broken := report
	broken.ID = "70000000-0000-0000-0000-0000000000e3"
	broken.SequenceNo = 2
	broken.UnitsTotal = 5
	if err := repository.CreateStationReport(ctx, broken); err == nil {
		t.Fatal("CreateStationReport(inconsistent counts) was accepted despite the check constraint")
	}

	reports, err := repository.ListStationReports(ctx, run.ID)
	if err != nil || len(reports) != 1 {
		t.Fatalf("ListStationReports() = %d, err = %v; want 1", len(reports), err)
	}
	if len(reports[0].UnitResults) != 2 || reports[0].UnitResults[1].Result != manufacturing.ResultFail {
		t.Fatalf("report unit results = %+v", reports[0].UnitResults)
	}
	if reports[0].EvidenceSHA256 != report.EvidenceSHA256 {
		t.Fatalf("evidence hash = %q", reports[0].EvidenceSHA256)
	}
	byManufacturer, err := repository.ListStationReportsByManufacturer(ctx, integrationManufacturerID, 10)
	if err != nil || len(byManufacturer) != 1 {
		t.Fatalf("ListStationReportsByManufacturer() = %d, err = %v; want 1", len(byManufacturer), err)
	}
	if other, err := repository.ListStationReportsByManufacturer(ctx, "20000000-0000-0000-0000-0000000000ff", 10); err != nil || len(other) != 0 {
		t.Fatalf("ListStationReportsByManufacturer(other) = %d, err = %v; want 0", len(other), err)
	}

	closed := loadedRun
	closed.Status = manufacturing.RunClosed
	closed.ClosedByAccountID = integrationFactoryAdminID
	closed.ClosedAt = &now
	closed.UpdatedAt = now
	if err := repository.UpdateRun(ctx, closed); err != nil {
		t.Fatalf("UpdateRun() error = %v", err)
	}
	reloaded, err := repository.FindRunByID(ctx, run.ID)
	if err != nil || reloaded.Status != manufacturing.RunClosed || reloaded.ClosedAt == nil {
		t.Fatalf("closed run = %+v, err = %v", reloaded, err)
	}
	// 生命周期单向：closed 不能再回到 open。
	if err := repository.UpdateRun(ctx, manufacturing.Run{ID: run.ID, Status: manufacturing.RunOpen}); !errors.Is(err, manufacturing.ErrRunClosed) {
		t.Fatalf("UpdateRun(reopen) error = %v, want ErrRunClosed", err)
	}
	// 关闭字段必须与状态自洽（由 CHECK 兜底）：状态回到 open 却保留 closed_at 会被拒绝。
	if err := repository.UpdateRun(ctx, manufacturing.Run{
		ID: run.ID, Status: manufacturing.RunClosed, ClosedAt: &now, ClosedByAccountID: integrationFactoryAdminID,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("UpdateRun(idempotent close) error = %v", err)
	}
	if err := repository.UpdateRun(ctx, manufacturing.Run{
		ID: run.ID, Status: manufacturing.RunOpen, ClosedAt: &now, ClosedByAccountID: integrationFactoryAdminID,
	}); err == nil {
		t.Fatal("UpdateRun(open with closed_at set) was accepted despite the check constraint")
	}
}
