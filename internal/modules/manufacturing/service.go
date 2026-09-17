package manufacturing

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/portalaccount"
)

const (
	maxUnitResults    = 500
	maxEvidenceLength = 64
	reportLimit       = 200
)

var (
	stationCodePattern = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$")
	batchCodePattern   = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	deviceSNPattern    = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9._-]{2,63}$")
	chipUIDPattern     = regexp.MustCompile("^[0-9a-f]{2,64}$")
	sha256Pattern      = regexp.MustCompile("^[0-9a-f]{64}$")
)

// Service 承载工位、批次与工位报告。
type Service struct {
	repository Repository
	now        func() time.Time
}

func NewService(repository Repository, now func() time.Time) (*Service, error) {
	if repository == nil {
		return nil, fmt.Errorf("manufacturing repository is required")
	}
	if now == nil {
		now = time.Now
	}
	return &Service{repository: repository, now: now}, nil
}

func isManufacturer(actor Actor) bool {
	return actor.Kind == portalaccount.KindManufacturer && actor.ManufacturerID != ""
}

func isManufacturerAdmin(actor Actor) bool {
	return isManufacturer(actor) && actor.Role == portalaccount.RoleManufacturerAdmin
}

func isStaff(actor Actor) bool {
	return actor.Kind == portalaccount.KindStaff && actor.Role == portalaccount.RoleSuperAdmin
}

// CreateStation 只允许厂商管理员建工位；工位属于操作者所在厂商。
func (service *Service) CreateStation(ctx context.Context, actor Actor, input CreateStationInput) (Station, error) {
	if !isManufacturerAdmin(actor) {
		return Station{}, ErrForbidden
	}
	input.Code = strings.TrimSpace(input.Code)
	input.Name = strings.TrimSpace(input.Name)
	if !stationCodePattern.MatchString(input.Code) || input.Name == "" {
		return Station{}, fmt.Errorf("%w: station code and name are required", ErrInvalidInput)
	}
	stationID, err := randomUUID()
	if err != nil {
		return Station{}, fmt.Errorf("generate station id: %w", err)
	}
	now := service.now().UTC()
	station := Station{
		ID: stationID, ManufacturerID: actor.ManufacturerID, Code: input.Code, Name: input.Name,
		Status: StatusActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := service.repository.CreateStation(ctx, station); err != nil {
		return Station{}, err
	}
	return station, nil
}

// ListStations 只返回操作者所在厂商的工位；staff 需要显式厂商归属，暂不支持跨厂商列表。
func (service *Service) ListStations(ctx context.Context, actor Actor, filter StationFilter) ([]Station, error) {
	if !isManufacturer(actor) {
		return nil, ErrForbidden
	}
	filter.Status = strings.TrimSpace(filter.Status)
	return service.repository.ListStations(ctx, actor.ManufacturerID, filter)
}

// UpdateStation 允许改名或启停；跨厂商按不存在处理。
func (service *Service) UpdateStation(ctx context.Context, actor Actor, stationID string, patch StationPatch) (Station, error) {
	if !isManufacturerAdmin(actor) {
		return Station{}, ErrForbidden
	}
	station, err := service.repository.FindStationByID(ctx, stationID)
	if err != nil {
		return Station{}, err
	}
	if station.ManufacturerID != actor.ManufacturerID {
		return Station{}, ErrStationNotFound
	}
	if patch.Name != nil {
		name := strings.TrimSpace(*patch.Name)
		if name == "" {
			return Station{}, fmt.Errorf("%w: station name must not be blank", ErrInvalidInput)
		}
		station.Name = name
	}
	if patch.Status != nil {
		status := strings.TrimSpace(*patch.Status)
		if status != StatusActive && status != StatusDisabled {
			return Station{}, fmt.Errorf("%w: unknown station status %q", ErrInvalidInput, status)
		}
		station.Status = status
	}
	station.UpdatedAt = service.now().UTC()
	if err := service.repository.UpdateStation(ctx, station); err != nil {
		return Station{}, err
	}
	return station, nil
}

// CreateRun 建批次。厂商归属由操作者 scope 决定；项目必须是本厂商的 OEM 项目，
// 且产品必须在项目白名单内——否则产线可以为任意产品造证据。
func (service *Service) CreateRun(ctx context.Context, actor Actor, input CreateRunInput) (Run, error) {
	if !isManufacturer(actor) {
		return Run{}, ErrForbidden
	}
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.ProductPID = strings.TrimSpace(input.ProductPID)
	input.BatchCode = strings.TrimSpace(input.BatchCode)
	if input.ProjectID == "" || !batchCodePattern.MatchString(input.BatchCode) {
		return Run{}, fmt.Errorf("%w: project and batch code are required", ErrInvalidInput)
	}
	if input.PlannedQuantity != nil && *input.PlannedQuantity <= 0 {
		return Run{}, fmt.Errorf("%w: planned quantity must be positive", ErrInvalidInput)
	}
	scope, err := service.repository.FindProjectScope(ctx, input.ProjectID)
	if err != nil {
		return Run{}, err
	}
	if scope.ProjectType != "oem" || scope.ManufacturerID != actor.ManufacturerID || scope.Status != StatusActive {
		return Run{}, ErrProjectNotEligible
	}
	if input.ProductPID == "" {
		return Run{}, fmt.Errorf("%w: product pid is required", ErrInvalidInput)
	}
	if !contains(scope.AllowedPIDs, input.ProductPID) {
		return Run{}, ErrProductNotInProject
	}
	runID, err := randomUUID()
	if err != nil {
		return Run{}, fmt.Errorf("generate run id: %w", err)
	}
	now := service.now().UTC()
	run := Run{
		ID: runID, ProjectID: scope.ProjectID, ProductPID: input.ProductPID,
		ManufacturerID: actor.ManufacturerID, BatchCode: input.BatchCode,
		PlannedQuantity: input.PlannedQuantity, Status: RunOpen,
		OpenedByAccountID: actor.AccountID, OpenedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := service.repository.CreateRun(ctx, run); err != nil {
		return Run{}, err
	}
	return run, nil
}

// ListRuns 只返回本厂商的批次。
func (service *Service) ListRuns(ctx context.Context, actor Actor, filter RunFilter) ([]Run, error) {
	if !isManufacturer(actor) {
		return nil, ErrForbidden
	}
	filter.ProjectID = strings.TrimSpace(filter.ProjectID)
	filter.Status = strings.TrimSpace(filter.Status)
	return service.repository.ListRuns(ctx, actor.ManufacturerID, filter)
}

// GetRun 跨厂商按不存在处理。
func (service *Service) GetRun(ctx context.Context, actor Actor, runID string) (Run, error) {
	if !isManufacturer(actor) {
		return Run{}, ErrForbidden
	}
	run, err := service.repository.FindRunByID(ctx, runID)
	if err != nil {
		return Run{}, err
	}
	if run.ManufacturerID != actor.ManufacturerID {
		return Run{}, ErrRunNotFound
	}
	return run, nil
}

// CloseRun 关闭批次；关闭后拒绝新增报告。重复关闭是幂等的。
func (service *Service) CloseRun(ctx context.Context, actor Actor, runID string) (Run, error) {
	run, err := service.GetRun(ctx, actor, runID)
	if err != nil {
		return Run{}, err
	}
	if run.Status == RunClosed {
		return run, nil
	}
	now := service.now().UTC()
	run.Status = RunClosed
	run.ClosedByAccountID = actor.AccountID
	run.ClosedAt = &now
	run.UpdatedAt = now
	if err := service.repository.UpdateRun(ctx, run); err != nil {
		return Run{}, err
	}
	return run, nil
}

// SubmitReport 提交工位报告。
// 校验：批次属于本厂商、批次未关闭、工位属于同一厂商、计数自洽、单机结果形状合法。
func (service *Service) SubmitReport(ctx context.Context, actor Actor, runID string, input SubmitReportInput) (StationReport, error) {
	if !isManufacturer(actor) {
		return StationReport{}, ErrForbidden
	}
	run, err := service.GetRun(ctx, actor, runID)
	if err != nil {
		return StationReport{}, err
	}
	if run.Status != RunOpen {
		return StationReport{}, ErrRunClosed
	}
	input.StationID = strings.TrimSpace(input.StationID)
	station, err := service.repository.FindStationByID(ctx, input.StationID)
	if err != nil {
		return StationReport{}, err
	}
	if station.ManufacturerID != actor.ManufacturerID || station.Status != StatusActive {
		return StationReport{}, ErrStationNotFound
	}
	units, passed, failed, overall, err := summarize(input.UnitResults)
	if err != nil {
		return StationReport{}, err
	}
	evidence := strings.ToLower(strings.TrimSpace(input.EvidenceSHA256))
	if evidence != "" && !sha256Pattern.MatchString(evidence) {
		return StationReport{}, fmt.Errorf("%w: evidence hash must be a lowercase sha256", ErrInvalidInput)
	}
	reports, err := service.repository.ListStationReports(ctx, runID)
	if err != nil {
		return StationReport{}, err
	}
	sequence := 1
	for _, existing := range reports {
		if existing.StationID == station.ID && existing.SequenceNo >= sequence {
			sequence = existing.SequenceNo + 1
		}
	}
	reportID, err := randomUUID()
	if err != nil {
		return StationReport{}, fmt.Errorf("generate report id: %w", err)
	}
	report := StationReport{
		ID: reportID, RunID: runID, StationID: station.ID, SubmittedByAccountID: actor.AccountID,
		SequenceNo: sequence, UnitsTotal: units, UnitsPassed: passed, UnitsFailed: failed,
		OverallResult: overall, UnitResults: input.UnitResults, EvidenceSHA256: evidence,
		Note: strings.TrimSpace(input.Note), CreatedAt: service.now().UTC(),
	}
	if err := service.repository.CreateStationReport(ctx, report); err != nil {
		return StationReport{}, err
	}
	return report, nil
}

// ListReports 返回批次下的工位报告（本厂商可见）。
func (service *Service) ListReports(ctx context.Context, actor Actor, runID string) ([]StationReport, error) {
	if _, err := service.GetRun(ctx, actor, runID); err != nil {
		return nil, err
	}
	return service.repository.ListStationReports(ctx, runID)
}

// ListManufacturerReports 返回本厂商最近的报告，供"测试报告"列表页使用。
func (service *Service) ListManufacturerReports(ctx context.Context, actor Actor) ([]StationReport, error) {
	if !isManufacturer(actor) {
		return nil, ErrForbidden
	}
	return service.repository.ListStationReportsByManufacturer(ctx, actor.ManufacturerID, reportLimit)
}

// summarize 从单机结果推导计数与总体结论；计数与结果必须自洽。
func summarize(units []UnitResult) (int, int, int, string, error) {
	if len(units) > maxUnitResults {
		return 0, 0, 0, "", fmt.Errorf("%w: too many unit results", ErrInvalidInput)
	}
	passed, failed := 0, 0
	for index, unit := range units {
		deviceSN := strings.TrimSpace(unit.DeviceSN)
		if !deviceSNPattern.MatchString(deviceSN) {
			return 0, 0, 0, "", fmt.Errorf("%w: invalid device sn at index %d", ErrInvalidInput, index)
		}
		if chipUID := strings.TrimSpace(unit.ChipUID); chipUID != "" && !chipUIDPattern.MatchString(strings.ToLower(chipUID)) {
			return 0, 0, 0, "", fmt.Errorf("%w: invalid chip uid at index %d", ErrInvalidInput, index)
		}
		switch strings.TrimSpace(unit.Result) {
		case ResultPass:
			passed++
		case ResultFail:
			failed++
		default:
			return 0, 0, 0, "", fmt.Errorf("%w: unknown unit result at index %d", ErrInvalidInput, index)
		}
		if testedAt := strings.TrimSpace(unit.TestedAt); testedAt != "" {
			if _, err := time.Parse(time.RFC3339, testedAt); err != nil {
				return 0, 0, 0, "", fmt.Errorf("%w: tested_at must be RFC3339", ErrInvalidInput)
			}
		}
	}
	overall := ResultPass
	if failed > 0 {
		overall = ResultFail
	}
	return len(units), passed, failed, overall, nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
