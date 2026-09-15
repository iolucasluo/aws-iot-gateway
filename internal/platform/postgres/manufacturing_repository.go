package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/manufacturing"
)

// ManufacturingRepository 是制造面的 PostgreSQL 实现。
type ManufacturingRepository struct{ pool *pgxpool.Pool }

func NewManufacturingRepository(pool *pgxpool.Pool) (*ManufacturingRepository, error) {
	if pool == nil {
		return nil, fmt.Errorf("PostgreSQL manufacturing repository requires a pool")
	}
	return &ManufacturingRepository{pool: pool}, nil
}

const stationColumns = `
	id::text, manufacturer_id::text, code, name, status, created_at, updated_at
`

func (repository *ManufacturingRepository) CreateStation(ctx context.Context, station manufacturing.Station) error {
	_, err := repository.pool.Exec(ctx, `
		INSERT INTO production_stations (id, manufacturer_id, code, name, status, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
	`, station.ID, station.ManufacturerID, station.Code, station.Name, station.Status,
		station.CreatedAt, station.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: station code already exists for this manufacturer", manufacturing.ErrInvalidInput)
		}
		return fmt.Errorf("create production station: %w", err)
	}
	return nil
}

func (repository *ManufacturingRepository) FindStationByID(ctx context.Context, stationID string) (manufacturing.Station, error) {
	return scanStation(repository.pool.QueryRow(ctx, `
		SELECT `+stationColumns+` FROM production_stations WHERE id=$1
	`, stationID))
}

func (repository *ManufacturingRepository) ListStations(ctx context.Context, manufacturerID string, filter manufacturing.StationFilter) ([]manufacturing.Station, error) {
	limit := normalizeOperationsLimit(filter.Limit)
	rows, err := repository.pool.Query(ctx, `
		SELECT `+stationColumns+`
		FROM production_stations
		WHERE manufacturer_id=$1 AND ($2 = '' OR status=$2)
		ORDER BY code ASC
		LIMIT $3
	`, manufacturerID, filter.Status, limit)
	if err != nil {
		return nil, fmt.Errorf("list production stations: %w", err)
	}
	defer rows.Close()
	stations := make([]manufacturing.Station, 0, limit)
	for rows.Next() {
		station, err := scanStation(rows)
		if err != nil {
			return nil, err
		}
		stations = append(stations, station)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list production stations: %w", err)
	}
	return stations, nil
}

func (repository *ManufacturingRepository) UpdateStation(ctx context.Context, station manufacturing.Station) error {
	tag, err := repository.pool.Exec(ctx, `
		UPDATE production_stations SET name=$2, status=$3, updated_at=$4 WHERE id=$1
	`, station.ID, station.Name, station.Status, station.UpdatedAt)
	if err != nil {
		return fmt.Errorf("update production station: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return manufacturing.ErrStationNotFound
	}
	return nil
}

const runColumns = `
	id::text, project_id::text, product_pid, manufacturer_id::text, batch_code, planned_quantity,
	status, opened_by_account_id::text, opened_at, COALESCE(closed_by_account_id::text, ''), closed_at,
	created_at, updated_at
`

func (repository *ManufacturingRepository) CreateRun(ctx context.Context, run manufacturing.Run) error {
	_, err := repository.pool.Exec(ctx, `
		INSERT INTO production_runs (
			id, project_id, product_pid, manufacturer_id, batch_code, planned_quantity, status,
			opened_by_account_id, opened_at, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
	`, run.ID, run.ProjectID, run.ProductPID, run.ManufacturerID, run.BatchCode, run.PlannedQuantity,
		run.Status, run.OpenedByAccountID, run.OpenedAt, run.CreatedAt, run.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: batch code already exists for this project", manufacturing.ErrInvalidInput)
		}
		return fmt.Errorf("create production run: %w", err)
	}
	return nil
}

func (repository *ManufacturingRepository) FindRunByID(ctx context.Context, runID string) (manufacturing.Run, error) {
	return scanRun(repository.pool.QueryRow(ctx, `
		SELECT `+runColumns+` FROM production_runs WHERE id=$1
	`, runID))
}

func (repository *ManufacturingRepository) ListRuns(ctx context.Context, manufacturerID string, filter manufacturing.RunFilter) ([]manufacturing.Run, error) {
	limit := normalizeOperationsLimit(filter.Limit)
	rows, err := repository.pool.Query(ctx, `
		SELECT `+runColumns+`
		FROM production_runs
		WHERE manufacturer_id=$1
		  AND ($2 = '' OR project_id=$2::uuid)
		  AND ($3 = '' OR status=$3)
		ORDER BY created_at DESC, id DESC
		LIMIT $4
	`, manufacturerID, filter.ProjectID, filter.Status, limit)
	if err != nil {
		return nil, fmt.Errorf("list production runs: %w", err)
	}
	defer rows.Close()
	runs := make([]manufacturing.Run, 0, limit)
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list production runs: %w", err)
	}
	return runs, nil
}

// UpdateRun 关闭批次。生命周期是单向的：closed 不能再回到 open。
// 该不变量用条件更新表达，避免依赖调用方自律（CHECK 只约束字段自洽，不约束方向）。
func (repository *ManufacturingRepository) UpdateRun(ctx context.Context, run manufacturing.Run) error {
	tag, err := repository.pool.Exec(ctx, `
		UPDATE production_runs
		SET status=$2, closed_by_account_id=NULLIF($3,'')::uuid, closed_at=$4, updated_at=$5
		WHERE id=$1 AND NOT (status = 'closed' AND $2 <> 'closed')
	`, run.ID, run.Status, run.ClosedByAccountID, run.ClosedAt, run.UpdatedAt)
	if err != nil {
		return fmt.Errorf("update production run: %w", err)
	}
	if tag.RowsAffected() == 0 {
		current, findErr := repository.FindRunByID(ctx, run.ID)
		if findErr == nil && current.Status == manufacturing.RunClosed && run.Status != manufacturing.RunClosed {
			return manufacturing.ErrRunClosed
		}
		return manufacturing.ErrRunNotFound
	}
	return nil
}

// reportColumns 全部带 r. 前缀：列表查询会和 production_runs 连接，未限定会歧义。
const reportColumns = `
	r.id::text, r.run_id::text, r.station_id::text, r.submitted_by_account_id::text, r.sequence_no,
	r.units_total, r.units_passed, r.units_failed, r.overall_result, r.unit_results,
	COALESCE(r.evidence_sha256, ''), COALESCE(r.note, ''), r.created_at
`

func (repository *ManufacturingRepository) CreateStationReport(ctx context.Context, report manufacturing.StationReport) error {
	units, err := encodeUnitResults(report.UnitResults)
	if err != nil {
		return err
	}
	_, err = repository.pool.Exec(ctx, `
		INSERT INTO production_station_reports (
			id, run_id, station_id, submitted_by_account_id, sequence_no,
			units_total, units_passed, units_failed, overall_result, unit_results,
			evidence_sha256, note, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULLIF($11,''),NULLIF($12,''),$13)
	`, report.ID, report.RunID, report.StationID, report.SubmittedByAccountID, report.SequenceNo,
		report.UnitsTotal, report.UnitsPassed, report.UnitsFailed, report.OverallResult, units,
		report.EvidenceSHA256, report.Note, report.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: report sequence already exists for this station and run", manufacturing.ErrInvalidInput)
		}
		return fmt.Errorf("create station report: %w", err)
	}
	return nil
}

func (repository *ManufacturingRepository) ListStationReports(ctx context.Context, runID string) ([]manufacturing.StationReport, error) {
	return repository.queryReports(ctx, `
		SELECT `+reportColumns+` FROM production_station_reports r
		WHERE r.run_id=$1 ORDER BY r.created_at ASC, r.sequence_no ASC
	`, runID)
}

func (repository *ManufacturingRepository) ListStationReportsByManufacturer(ctx context.Context, manufacturerID string, limit int) ([]manufacturing.StationReport, error) {
	return repository.queryReports(ctx, `
		SELECT `+reportColumns+`
		FROM production_station_reports r
		JOIN production_runs run ON run.id = r.run_id
		WHERE run.manufacturer_id=$1
		ORDER BY r.created_at DESC, r.id DESC
		LIMIT $2
	`, manufacturerID, normalizeOperationsLimit(limit))
}

// FindProjectScope 返回产线准入校验所需的项目事实；项目不存在按不可用处理。
func (repository *ManufacturingRepository) FindProjectScope(ctx context.Context, projectID string) (manufacturing.ProjectScope, error) {
	var scope manufacturing.ProjectScope
	var projectType, manufacturerID, status string
	err := repository.pool.QueryRow(ctx, `
		SELECT id::text, project_type, COALESCE(manufacturer_id::text, ''), status
		FROM projects WHERE id=$1
	`, projectID).Scan(&scope.ProjectID, &projectType, &manufacturerID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return manufacturing.ProjectScope{}, manufacturing.ErrProjectNotEligible
	}
	if err != nil {
		return manufacturing.ProjectScope{}, fmt.Errorf("find project scope: %w", err)
	}
	scope.ProjectType = projectType
	scope.ManufacturerID = manufacturerID
	scope.Status = status
	pids, err := repository.projectPIDs(ctx, projectID)
	if err != nil {
		return manufacturing.ProjectScope{}, err
	}
	scope.AllowedPIDs = pids
	return scope, nil
}

func (repository *ManufacturingRepository) projectPIDs(ctx context.Context, projectID string) ([]string, error) {
	rows, err := repository.pool.Query(ctx, `
		SELECT p.pid FROM project_products pp JOIN products p ON p.id = pp.product_id
		WHERE pp.project_id=$1 ORDER BY p.pid ASC
	`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list project pids: %w", err)
	}
	defer rows.Close()
	pids := make([]string, 0, 4)
	for rows.Next() {
		var pid string
		if err := rows.Scan(&pid); err != nil {
			return nil, fmt.Errorf("scan project pid: %w", err)
		}
		pids = append(pids, pid)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list project pids: %w", err)
	}
	return pids, nil
}

func (repository *ManufacturingRepository) queryReports(ctx context.Context, sql string, arguments ...any) ([]manufacturing.StationReport, error) {
	rows, err := repository.pool.Query(ctx, sql, arguments...)
	if err != nil {
		return nil, fmt.Errorf("list station reports: %w", err)
	}
	defer rows.Close()
	reports := make([]manufacturing.StationReport, 0, 8)
	for rows.Next() {
		report, err := scanStationReport(rows)
		if err != nil {
			return nil, err
		}
		reports = append(reports, report)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list station reports: %w", err)
	}
	return reports, nil
}

func scanStation(row pgx.Row) (manufacturing.Station, error) {
	var station manufacturing.Station
	err := row.Scan(&station.ID, &station.ManufacturerID, &station.Code, &station.Name,
		&station.Status, &station.CreatedAt, &station.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return manufacturing.Station{}, manufacturing.ErrStationNotFound
	}
	if err != nil {
		return manufacturing.Station{}, fmt.Errorf("scan production station: %w", err)
	}
	return station, nil
}

func scanRun(row pgx.Row) (manufacturing.Run, error) {
	var run manufacturing.Run
	err := row.Scan(&run.ID, &run.ProjectID, &run.ProductPID, &run.ManufacturerID, &run.BatchCode,
		&run.PlannedQuantity, &run.Status, &run.OpenedByAccountID, &run.OpenedAt,
		&run.ClosedByAccountID, &run.ClosedAt, &run.CreatedAt, &run.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return manufacturing.Run{}, manufacturing.ErrRunNotFound
	}
	if err != nil {
		return manufacturing.Run{}, fmt.Errorf("scan production run: %w", err)
	}
	return run, nil
}

func scanStationReport(row pgx.Row) (manufacturing.StationReport, error) {
	var report manufacturing.StationReport
	var units []byte
	err := row.Scan(&report.ID, &report.RunID, &report.StationID, &report.SubmittedByAccountID,
		&report.SequenceNo, &report.UnitsTotal, &report.UnitsPassed, &report.UnitsFailed,
		&report.OverallResult, &units, &report.EvidenceSHA256, &report.Note, &report.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return manufacturing.StationReport{}, manufacturing.ErrReportNotFound
	}
	if err != nil {
		return manufacturing.StationReport{}, fmt.Errorf("scan station report: %w", err)
	}
	if len(units) > 0 {
		if err := json.Unmarshal(units, &report.UnitResults); err != nil {
			return manufacturing.StationReport{}, fmt.Errorf("decode unit results: %w", err)
		}
	}
	if report.UnitResults == nil {
		report.UnitResults = []manufacturing.UnitResult{}
	}
	return report, nil
}

// encodeUnitResults 保证列内容始终是 JSON 数组（null 会违反 jsonb_typeof 约束）。
func encodeUnitResults(units []manufacturing.UnitResult) ([]byte, error) {
	if units == nil {
		units = []manufacturing.UnitResult{}
	}
	encoded, err := json.Marshal(units)
	if err != nil {
		return nil, fmt.Errorf("encode unit results: %w", err)
	}
	return encoded, nil
}

// normalizeOperationsLimit 与只读运维视图共用同一套列表上限。
func normalizeOperationsLimit(limit int) int {
	switch {
	case limit <= 0:
		return 50
	case limit > 200:
		return 200
	default:
		return limit
	}
}

var _ = pgconn.PgError{}

const manufacturerColumns = `
	id::text, code, name, COALESCE(contact_name, ''), COALESCE(contact_email, ''), status, created_at, updated_at
`

func (repository *ManufacturingRepository) CreateManufacturer(ctx context.Context, manufacturer manufacturing.Manufacturer) error {
	_, err := repository.pool.Exec(ctx, `
		INSERT INTO manufacturers (id, code, name, contact_name, contact_email, status, created_at, updated_at)
		VALUES ($1,$2,$3,NULLIF($4,''),NULLIF($5,''),$6,$7,$8)
	`, manufacturer.ID, manufacturer.Code, manufacturer.Name, manufacturer.ContactName,
		manufacturer.ContactEmail, manufacturer.Status, manufacturer.CreatedAt, manufacturer.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: manufacturer code already exists", manufacturing.ErrInvalidInput)
		}
		return fmt.Errorf("create manufacturer: %w", err)
	}
	return nil
}

func (repository *ManufacturingRepository) FindManufacturerByID(ctx context.Context, manufacturerID string) (manufacturing.Manufacturer, error) {
	return scanManufacturer(repository.pool.QueryRow(ctx, `
		SELECT `+manufacturerColumns+` FROM manufacturers WHERE id=$1
	`, manufacturerID))
}

func (repository *ManufacturingRepository) ListManufacturers(ctx context.Context, filter manufacturing.ManufacturerFilter) ([]manufacturing.Manufacturer, error) {
	limit := normalizeOperationsLimit(filter.Limit)
	rows, err := repository.pool.Query(ctx, `
		SELECT `+manufacturerColumns+`
		FROM manufacturers
		WHERE ($1 = '' OR status=$1)
		ORDER BY code ASC
		LIMIT $2
	`, filter.Status, limit)
	if err != nil {
		return nil, fmt.Errorf("list manufacturers: %w", err)
	}
	defer rows.Close()
	manufacturers := make([]manufacturing.Manufacturer, 0, limit)
	for rows.Next() {
		manufacturer, err := scanManufacturer(rows)
		if err != nil {
			return nil, err
		}
		manufacturers = append(manufacturers, manufacturer)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list manufacturers: %w", err)
	}
	return manufacturers, nil
}

func (repository *ManufacturingRepository) UpdateManufacturer(ctx context.Context, manufacturer manufacturing.Manufacturer) error {
	tag, err := repository.pool.Exec(ctx, `
		UPDATE manufacturers
		SET name=$2, contact_name=NULLIF($3,''), contact_email=NULLIF($4,''), status=$5, updated_at=$6
		WHERE id=$1
	`, manufacturer.ID, manufacturer.Name, manufacturer.ContactName, manufacturer.ContactEmail,
		manufacturer.Status, manufacturer.UpdatedAt)
	if err != nil {
		return fmt.Errorf("update manufacturer: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return manufacturing.ErrManufacturerNotFound
	}
	return nil
}

func scanManufacturer(row pgx.Row) (manufacturing.Manufacturer, error) {
	var manufacturer manufacturing.Manufacturer
	err := row.Scan(&manufacturer.ID, &manufacturer.Code, &manufacturer.Name, &manufacturer.ContactName,
		&manufacturer.ContactEmail, &manufacturer.Status, &manufacturer.CreatedAt, &manufacturer.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return manufacturing.Manufacturer{}, manufacturing.ErrManufacturerNotFound
	}
	if err != nil {
		return manufacturing.Manufacturer{}, fmt.Errorf("scan manufacturer: %w", err)
	}
	return manufacturer, nil
}
