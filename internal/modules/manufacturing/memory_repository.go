package manufacturing

import (
	"context"
	"sort"
	"sync"
)

// MemoryRepository 供单元测试与非 DB 环境使用。
type MemoryRepository struct {
	mutex        sync.Mutex
	stations     map[string]Station
	runs         map[string]Run
	reports      []StationReport
	projectScope map[string]ProjectScope
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		stations: map[string]Station{}, runs: map[string]Run{}, projectScope: map[string]ProjectScope{},
	}
}

// AddProjectScope 登记产线归属校验所需的项目事实。
func (repository *MemoryRepository) AddProjectScope(scope ProjectScope) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	repository.projectScope[scope.ProjectID] = scope
}

func (repository *MemoryRepository) CreateStation(_ context.Context, station Station) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	for _, existing := range repository.stations {
		if existing.ManufacturerID == station.ManufacturerID && existing.Code == station.Code {
			return ErrInvalidInput
		}
	}
	repository.stations[station.ID] = station
	return nil
}

func (repository *MemoryRepository) FindStationByID(_ context.Context, stationID string) (Station, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	station, found := repository.stations[stationID]
	if !found {
		return Station{}, ErrStationNotFound
	}
	return station, nil
}

func (repository *MemoryRepository) ListStations(_ context.Context, manufacturerID string, filter StationFilter) ([]Station, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	stations := make([]Station, 0, len(repository.stations))
	for _, station := range repository.stations {
		if station.ManufacturerID != manufacturerID {
			continue
		}
		if filter.Status != "" && station.Status != filter.Status {
			continue
		}
		stations = append(stations, station)
	}
	sort.Slice(stations, func(left, right int) bool { return stations[left].Code < stations[right].Code })
	return stations, nil
}

func (repository *MemoryRepository) UpdateStation(_ context.Context, station Station) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	if _, found := repository.stations[station.ID]; !found {
		return ErrStationNotFound
	}
	repository.stations[station.ID] = station
	return nil
}

func (repository *MemoryRepository) CreateRun(_ context.Context, run Run) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	for _, existing := range repository.runs {
		if existing.ProjectID == run.ProjectID && existing.BatchCode == run.BatchCode {
			return ErrInvalidInput
		}
	}
	repository.runs[run.ID] = run
	return nil
}

func (repository *MemoryRepository) FindRunByID(_ context.Context, runID string) (Run, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	run, found := repository.runs[runID]
	if !found {
		return Run{}, ErrRunNotFound
	}
	return run, nil
}

func (repository *MemoryRepository) ListRuns(_ context.Context, manufacturerID string, filter RunFilter) ([]Run, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	runs := make([]Run, 0, len(repository.runs))
	for _, run := range repository.runs {
		if run.ManufacturerID != manufacturerID {
			continue
		}
		if filter.ProjectID != "" && run.ProjectID != filter.ProjectID {
			continue
		}
		if filter.Status != "" && run.Status != filter.Status {
			continue
		}
		runs = append(runs, run)
	}
	sort.Slice(runs, func(left, right int) bool { return runs[left].CreatedAt.After(runs[right].CreatedAt) })
	return runs, nil
}

func (repository *MemoryRepository) UpdateRun(_ context.Context, run Run) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	if _, found := repository.runs[run.ID]; !found {
		return ErrRunNotFound
	}
	repository.runs[run.ID] = run
	return nil
}

func (repository *MemoryRepository) CreateStationReport(_ context.Context, report StationReport) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	for _, existing := range repository.reports {
		if existing.RunID == report.RunID && existing.StationID == report.StationID && existing.SequenceNo == report.SequenceNo {
			return ErrInvalidInput
		}
	}
	repository.reports = append(repository.reports, report)
	return nil
}

func (repository *MemoryRepository) ListStationReports(_ context.Context, runID string) ([]StationReport, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	reports := make([]StationReport, 0, len(repository.reports))
	for _, report := range repository.reports {
		if report.RunID == runID {
			reports = append(reports, report)
		}
	}
	sort.Slice(reports, func(left, right int) bool { return reports[left].CreatedAt.Before(reports[right].CreatedAt) })
	return reports, nil
}

func (repository *MemoryRepository) ListStationReportsByManufacturer(_ context.Context, manufacturerID string, limit int) ([]StationReport, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	reports := make([]StationReport, 0, len(repository.reports))
	for _, report := range repository.reports {
		run, found := repository.runs[report.RunID]
		if !found || run.ManufacturerID != manufacturerID {
			continue
		}
		reports = append(reports, report)
	}
	sort.Slice(reports, func(left, right int) bool { return reports[left].CreatedAt.After(reports[right].CreatedAt) })
	if limit > 0 && len(reports) > limit {
		reports = reports[:limit]
	}
	return reports, nil
}

func (repository *MemoryRepository) FindProjectScope(_ context.Context, projectID string) (ProjectScope, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	scope, found := repository.projectScope[projectID]
	if !found {
		return ProjectScope{}, ErrProjectNotEligible
	}
	return scope, nil
}
