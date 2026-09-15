package manufacturing

import (
	"context"
	"errors"
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/portalaccount"
)

const (
	testManufacturerID  = "20000000-0000-0000-0000-0000000000a1"
	otherManufacturerID = "20000000-0000-0000-0000-0000000000a2"
	testProjectID       = "50000000-0000-0000-0000-0000000000a1"
	testProductPID      = "feeder-x1"
)

type testClock struct{ moment time.Time }

func (clock *testClock) now() time.Time { return clock.moment }

func newTestService(t *testing.T) (*Service, *MemoryRepository, *testClock) {
	t.Helper()
	clock := &testClock{moment: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	repository := NewMemoryRepository()
	repository.AddProjectScope(ProjectScope{
		ProjectID: testProjectID, ProjectType: "oem", ManufacturerID: testManufacturerID,
		Status: StatusActive, AllowedPIDs: []string{testProductPID},
	})
	service, err := NewService(repository, clock.now)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service, repository, clock
}

func adminActor() Actor {
	return Actor{
		AccountID: "10000000-0000-0000-0000-0000000000b1", Kind: portalaccount.KindManufacturer,
		Role: portalaccount.RoleManufacturerAdmin, ManufacturerID: testManufacturerID,
	}
}

func operatorActor() Actor {
	return Actor{
		AccountID: "10000000-0000-0000-0000-0000000000b2", Kind: portalaccount.KindManufacturer,
		Role: portalaccount.RoleManufacturerOperator, ManufacturerID: testManufacturerID,
	}
}

func otherFactoryActor() Actor {
	return Actor{
		AccountID: "10000000-0000-0000-0000-0000000000b3", Kind: portalaccount.KindManufacturer,
		Role: portalaccount.RoleManufacturerAdmin, ManufacturerID: otherManufacturerID,
	}
}

func TestStationLifecycleIsConfinedToTheManufacturer(t *testing.T) {
	t.Parallel()
	service, _, _ := newTestService(t)
	ctx := context.Background()

	station, err := service.CreateStation(ctx, adminActor(), CreateStationInput{Code: "ST-01", Name: " 工位一 "})
	if err != nil {
		t.Fatalf("CreateStation() error = %v", err)
	}
	if station.ManufacturerID != testManufacturerID || station.Name != "工位一" || station.Status != StatusActive {
		t.Fatalf("station = %+v", station)
	}
	// 操作员不能建工位，但能看到。
	if _, err := service.CreateStation(ctx, operatorActor(), CreateStationInput{Code: "ST-02", Name: "x"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("CreateStation(operator) error = %v, want ErrForbidden", err)
	}
	for name, input := range map[string]CreateStationInput{
		"bad-code":   {Code: "工位", Name: "x"},
		"empty-name": {Code: "ST-03"},
	} {
		if _, err := service.CreateStation(ctx, adminActor(), input); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("CreateStation(%s) error = %v, want ErrInvalidInput", name, err)
		}
	}
	if _, err := service.CreateStation(ctx, adminActor(), CreateStationInput{Code: "ST-01", Name: "重复"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("CreateStation(duplicate code) error = %v, want ErrInvalidInput", err)
	}
	stations, err := service.ListStations(ctx, operatorActor(), StationFilter{})
	if err != nil || len(stations) != 1 {
		t.Fatalf("ListStations() = %d, err = %v; want 1", len(stations), err)
	}
	// 跨厂商按不存在处理。
	if _, err := service.UpdateStation(ctx, otherFactoryActor(), station.ID, StationPatch{Status: strPtr(StatusDisabled)}); !errors.Is(err, ErrStationNotFound) {
		t.Fatalf("UpdateStation(other factory) error = %v, want ErrStationNotFound", err)
	}
	updated, err := service.UpdateStation(ctx, adminActor(), station.ID, StationPatch{Status: strPtr(StatusDisabled)})
	if err != nil || updated.Status != StatusDisabled {
		t.Fatalf("UpdateStation() = %+v, err = %v", updated, err)
	}
}

func TestCreateRunRequiresOwnedOEMProjectAndAllowedProduct(t *testing.T) {
	t.Parallel()
	service, _, _ := newTestService(t)
	ctx := context.Background()

	run, err := service.CreateRun(ctx, adminActor(), CreateRunInput{
		ProjectID: testProjectID, ProductPID: testProductPID, BatchCode: "BATCH-001",
	})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	if run.ManufacturerID != testManufacturerID || run.Status != RunOpen || run.OpenedByAccountID != adminActor().AccountID {
		t.Fatalf("run = %+v", run)
	}
	// 操作员也能建批次（产线日常由操作员发起）。
	if _, err := service.CreateRun(ctx, operatorActor(), CreateRunInput{
		ProjectID: testProjectID, ProductPID: testProductPID, BatchCode: "BATCH-002",
	}); err != nil {
		t.Fatalf("CreateRun(operator) error = %v", err)
	}
	// 别的厂商不能为本项目建批次。
	if _, err := service.CreateRun(ctx, otherFactoryActor(), CreateRunInput{
		ProjectID: testProjectID, ProductPID: testProductPID, BatchCode: "BATCH-003",
	}); !errors.Is(err, ErrProjectNotEligible) {
		t.Fatalf("CreateRun(other factory) error = %v, want ErrProjectNotEligible", err)
	}
	// 产品必须在项目白名单内。
	if _, err := service.CreateRun(ctx, adminActor(), CreateRunInput{
		ProjectID: testProjectID, ProductPID: "lamp-x1", BatchCode: "BATCH-004",
	}); !errors.Is(err, ErrProductNotInProject) {
		t.Fatalf("CreateRun(foreign product) error = %v, want ErrProductNotInProject", err)
	}
	for name, input := range map[string]CreateRunInput{
		"bad-batch":         {ProjectID: testProjectID, ProductPID: testProductPID, BatchCode: "批次"},
		"missing-project":   {ProductPID: testProductPID, BatchCode: "BATCH-005"},
		"negative-quantity": {ProjectID: testProjectID, ProductPID: testProductPID, BatchCode: "BATCH-006", PlannedQuantity: intPtr(-1)},
	} {
		if _, err := service.CreateRun(ctx, adminActor(), input); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("CreateRun(%s) error = %v, want ErrInvalidInput", name, err)
		}
	}
	if _, err := service.CreateRun(ctx, adminActor(), CreateRunInput{
		ProjectID: testProjectID, ProductPID: testProductPID, BatchCode: "BATCH-001",
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("CreateRun(duplicate batch) error = %v, want ErrInvalidInput", err)
	}
	// 跨厂商读取按不存在处理。
	if _, err := service.GetRun(ctx, otherFactoryActor(), run.ID); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("GetRun(other factory) error = %v, want ErrRunNotFound", err)
	}
	runs, err := service.ListRuns(ctx, adminActor(), RunFilter{})
	if err != nil || len(runs) != 2 {
		t.Fatalf("ListRuns() = %d, err = %v; want 2", len(runs), err)
	}
}

func TestStationReportDerivesCountsAndRespectsLifecycle(t *testing.T) {
	t.Parallel()
	service, repository, clock := newTestService(t)
	ctx := context.Background()
	station, err := service.CreateStation(ctx, adminActor(), CreateStationInput{Code: "ST-01", Name: "工位一"})
	if err != nil {
		t.Fatalf("CreateStation() error = %v", err)
	}
	run, err := service.CreateRun(ctx, adminActor(), CreateRunInput{
		ProjectID: testProjectID, ProductPID: testProductPID, BatchCode: "BATCH-001",
	})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}

	report, err := service.SubmitReport(ctx, operatorActor(), run.ID, SubmitReportInput{
		StationID: station.ID,
		UnitResults: []UnitResult{
			{DeviceSN: "fdr-00000000000000000000000000000001", ChipUID: "A1B2", Result: ResultPass},
			{DeviceSN: "fdr-00000000000000000000000000000002", Result: ResultFail},
		},
		EvidenceSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	})
	if err != nil {
		t.Fatalf("SubmitReport() error = %v", err)
	}
	if report.UnitsTotal != 2 || report.UnitsPassed != 1 || report.UnitsFailed != 1 || report.OverallResult != ResultFail {
		t.Fatalf("report counts = %+v", report)
	}
	if report.SequenceNo != 1 || report.SubmittedByAccountID != operatorActor().AccountID {
		t.Fatalf("report identity = %+v", report)
	}

	// 同一工位再报一次：序号递增。
	second, err := service.SubmitReport(ctx, operatorActor(), run.ID, SubmitReportInput{
		StationID:   station.ID,
		UnitResults: []UnitResult{{DeviceSN: "fdr-00000000000000000000000000000003", Result: ResultPass}},
	})
	if err != nil {
		t.Fatalf("SubmitReport(second) error = %v", err)
	}
	if second.SequenceNo != 2 || second.OverallResult != ResultPass {
		t.Fatalf("second report = %+v", second)
	}

	for name, input := range map[string]SubmitReportInput{
		"unknown-result":  {StationID: station.ID, UnitResults: []UnitResult{{DeviceSN: "fdr-1", Result: "maybe"}}},
		"bad-device-sn":   {StationID: station.ID, UnitResults: []UnitResult{{DeviceSN: "设备一", Result: ResultPass}}},
		"bad-evidence":    {StationID: station.ID, UnitResults: []UnitResult{{DeviceSN: "fdr-1", Result: ResultPass}}, EvidenceSHA256: "NOT-A-HASH"},
		"bad-tested-at":   {StationID: station.ID, UnitResults: []UnitResult{{DeviceSN: "fdr-1", Result: ResultPass, TestedAt: "yesterday"}}},
		"foreign-station": {StationID: "30000000-0000-0000-0000-0000000000ff", UnitResults: []UnitResult{{DeviceSN: "fdr-1", Result: ResultPass}}},
	} {
		if _, err := service.SubmitReport(ctx, operatorActor(), run.ID, input); !errors.Is(err, ErrInvalidInput) && !errors.Is(err, ErrStationNotFound) {
			t.Errorf("SubmitReport(%s) error = %v, want a validation error", name, err)
		}
	}

	// 关闭后拒绝新增报告；重复关闭幂等。
	clock.moment = clock.moment.Add(time.Minute)
	closed, err := service.CloseRun(ctx, adminActor(), run.ID)
	if err != nil || closed.Status != RunClosed || closed.ClosedAt == nil {
		t.Fatalf("CloseRun() = %+v, err = %v", closed, err)
	}
	if again, err := service.CloseRun(ctx, adminActor(), run.ID); err != nil || again.Status != RunClosed {
		t.Fatalf("CloseRun(again) = %+v, err = %v", again, err)
	}
	if _, err := service.SubmitReport(ctx, operatorActor(), run.ID, SubmitReportInput{
		StationID: station.ID, UnitResults: []UnitResult{{DeviceSN: "fdr-4", Result: ResultPass}},
	}); !errors.Is(err, ErrRunClosed) {
		t.Fatalf("SubmitReport(closed run) error = %v, want ErrRunClosed", err)
	}
	reports, err := service.ListReports(ctx, adminActor(), run.ID)
	if err != nil || len(reports) != 2 {
		t.Fatalf("ListReports() = %d, err = %v; want 2", len(reports), err)
	}
	if _, err := service.ListReports(ctx, otherFactoryActor(), run.ID); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("ListReports(other factory) error = %v, want ErrRunNotFound", err)
	}
	byManufacturer, err := service.ListManufacturerReports(ctx, adminActor())
	if err != nil || len(byManufacturer) != 2 {
		t.Fatalf("ListManufacturerReports() = %d, err = %v; want 2", len(byManufacturer), err)
	}
	_ = repository
}

func strPtr(value string) *string { return &value }
func intPtr(value int) *int       { return &value }
