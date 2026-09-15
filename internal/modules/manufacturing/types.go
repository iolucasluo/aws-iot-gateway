// Package manufacturing 承载代工厂侧的产线与质量证据。
//
// 边界：它不拥有设备身份，也不参与激活门禁。工位只是代工厂内部编号，
// 工位报告是 append-only 证据，允许引用尚未拥有运行身份的 device_sn。
package manufacturing

import (
	"errors"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/portalaccount"
)

const (
	StatusActive   = "active"
	StatusDisabled = "disabled"

	RunOpen   = "open"
	RunClosed = "closed"

	ResultPass = "pass"
	ResultFail = "fail"
)

var (
	ErrInvalidInput        = errors.New("invalid manufacturing input")
	ErrForbidden           = errors.New("manufacturing operation forbidden")
	ErrStationNotFound     = errors.New("production station not found")
	ErrRunNotFound         = errors.New("production run not found")
	ErrReportNotFound      = errors.New("production station report not found")
	ErrRunClosed           = errors.New("production run is closed")
	ErrProjectNotEligible  = errors.New("project is not an oem project of this manufacturer")
	ErrProductNotInProject = errors.New("product is not part of the project")
)

// Actor 复用门户账号投影；代工厂操作者必须带 manufacturer 归属。
type Actor = portalaccount.Actor

// Station 是代工厂内部工位。
type Station struct {
	ID             string
	ManufacturerID string
	Code           string
	Name           string
	Status         string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Run 是一次产线批次/测试 Run。
type Run struct {
	ID                string
	ProjectID         string
	ProductPID        string
	ManufacturerID    string
	BatchCode         string
	PlannedQuantity   *int
	Status            string
	OpenedByAccountID string
	OpenedAt          time.Time
	ClosedByAccountID string
	ClosedAt          *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// StationReport 是一条 append-only 工位质量证据。
type StationReport struct {
	ID                   string
	RunID                string
	StationID            string
	SubmittedByAccountID string
	SequenceNo           int
	UnitsTotal           int
	UnitsPassed          int
	UnitsFailed          int
	OverallResult        string
	UnitResults          []UnitResult
	EvidenceSHA256       string
	Note                 string
	CreatedAt            time.Time
}

// UnitResult 描述单台设备的产线结果。
// device_sn 允许指向尚未登记运行身份的整机（产线先于激活）。
type UnitResult struct {
	DeviceSN string         `json:"device_sn"`
	ChipUID  string         `json:"chip_uid,omitempty"`
	Result   string         `json:"result"`
	Items    map[string]any `json:"items,omitempty"`
	TestedAt string         `json:"tested_at,omitempty"`
}

// ProjectScope 是产线归属校验需要的项目事实（由仓库读取，不接受客户端传入）。
type ProjectScope struct {
	ProjectID      string
	ProjectType    string
	ManufacturerID string
	Status         string
	AllowedPIDs    []string
}

// StationFilter 与 RunFilter 是列表过滤条件。
type StationFilter struct {
	Status string
	Limit  int
}

type RunFilter struct {
	ProjectID string
	Status    string
	Limit     int
}

// CreateStationInput / StationPatch 描述工位写入。
type CreateStationInput struct {
	Code string
	Name string
}

type StationPatch struct {
	Name   *string
	Status *string
}

// CreateRunInput 描述批次创建；ManufacturerID 由操作者 scope 决定，不接受客户端传入。
type CreateRunInput struct {
	ProjectID       string
	ProductPID      string
	BatchCode       string
	PlannedQuantity *int
}

// SubmitReportInput 描述工位报告提交。
type SubmitReportInput struct {
	StationID      string
	UnitResults    []UnitResult
	EvidenceSHA256 string
	Note           string
}
