package manufacturing

import (
	"context"
	"time"
)

// Repository 是制造面的持久化边界。
type Repository interface {
	CreateStation(ctx context.Context, station Station) error
	FindStationByID(ctx context.Context, stationID string) (Station, error)
	ListStations(ctx context.Context, manufacturerID string, filter StationFilter) ([]Station, error)
	UpdateStation(ctx context.Context, station Station) error

	CreateRun(ctx context.Context, run Run) error
	FindRunByID(ctx context.Context, runID string) (Run, error)
	ListRuns(ctx context.Context, manufacturerID string, filter RunFilter) ([]Run, error)
	UpdateRun(ctx context.Context, run Run) error

	CreateStationReport(ctx context.Context, report StationReport) error
	ListStationReports(ctx context.Context, runID string) ([]StationReport, error)
	ListStationReportsByManufacturer(ctx context.Context, manufacturerID string, limit int) ([]StationReport, error)

	CreateManufacturer(ctx context.Context, manufacturer Manufacturer) error
	FindManufacturerByID(ctx context.Context, manufacturerID string) (Manufacturer, error)
	ListManufacturers(ctx context.Context, filter ManufacturerFilter) ([]Manufacturer, error)
	UpdateManufacturer(ctx context.Context, manufacturer Manufacturer) error

	// FindProjectScope 返回产线归属校验所需的项目事实。
	FindProjectScope(ctx context.Context, projectID string) (ProjectScope, error)
}

// now 由 Service 注入，仓库只接收显式时间。
var _ = time.Time{}
