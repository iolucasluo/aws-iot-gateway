package platformapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/manufacturing"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/portalaccount"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/project"
)

// writeManufacturingError 把制造面错误映射到 HTTP 语义。
// 越权与不存在统一 404，不泄露其他代工厂的资源是否存在。
func writeManufacturingError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, manufacturing.ErrStationNotFound),
		errors.Is(err, manufacturing.ErrRunNotFound),
		errors.Is(err, manufacturing.ErrReportNotFound),
		errors.Is(err, manufacturing.ErrManufacturerNotFound),
		errors.Is(err, manufacturing.ErrProjectNotEligible):
		writeError(writer, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, manufacturing.ErrProductNotInProject):
		writeError(writer, http.StatusBadRequest, "product_not_in_project", "product is not part of the project")
	case errors.Is(err, manufacturing.ErrRunClosed):
		writeError(writer, http.StatusConflict, "run_closed", "production run is closed")
	case errors.Is(err, manufacturing.ErrForbidden):
		writeError(writer, http.StatusForbidden, "forbidden", "operation is not allowed")
	case errors.Is(err, manufacturing.ErrInvalidInput):
		writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid")
	default:
		writeError(writer, http.StatusInternalServerError, "internal_error", "request could not be completed")
	}
}

type manufacturerResponse struct {
	ID           string `json:"id"`
	Code         string `json:"code"`
	Name         string `json:"name"`
	ContactName  string `json:"contact_name,omitempty"`
	ContactEmail string `json:"contact_email,omitempty"`
	Status       string `json:"status"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

func newManufacturerResponse(value manufacturing.Manufacturer) manufacturerResponse {
	return manufacturerResponse{
		ID: value.ID, Code: value.Code, Name: value.Name, ContactName: value.ContactName,
		ContactEmail: value.ContactEmail, Status: value.Status,
		CreatedAt: value.CreatedAt.UTC().Format(timeLayout), UpdatedAt: value.UpdatedAt.UTC().Format(timeLayout),
	}
}

type stationResponse struct {
	ID        string `json:"id"`
	Code      string `json:"code"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func newStationResponse(station manufacturing.Station) stationResponse {
	return stationResponse{
		ID: station.ID, Code: station.Code, Name: station.Name, Status: station.Status,
		CreatedAt: station.CreatedAt.UTC().Format(timeLayout), UpdatedAt: station.UpdatedAt.UTC().Format(timeLayout),
	}
}

type runResponse struct {
	ID                string `json:"id"`
	ProjectID         string `json:"project_id"`
	ProductPID        string `json:"product_pid"`
	BatchCode         string `json:"batch_code"`
	PlannedQuantity   *int   `json:"planned_quantity,omitempty"`
	Status            string `json:"status"`
	OpenedByAccountID string `json:"opened_by_account_id"`
	OpenedAt          string `json:"opened_at"`
	ClosedByAccountID string `json:"closed_by_account_id,omitempty"`
	ClosedAt          string `json:"closed_at,omitempty"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

func newRunResponse(run manufacturing.Run) runResponse {
	response := runResponse{
		ID: run.ID, ProjectID: run.ProjectID, ProductPID: run.ProductPID, BatchCode: run.BatchCode,
		PlannedQuantity: run.PlannedQuantity, Status: run.Status,
		OpenedByAccountID: run.OpenedByAccountID, OpenedAt: run.OpenedAt.UTC().Format(timeLayout),
		CreatedAt: run.CreatedAt.UTC().Format(timeLayout), UpdatedAt: run.UpdatedAt.UTC().Format(timeLayout),
	}
	if run.ClosedByAccountID != "" {
		response.ClosedByAccountID = run.ClosedByAccountID
	}
	if run.ClosedAt != nil {
		response.ClosedAt = run.ClosedAt.UTC().Format(timeLayout)
	}
	return response
}

type stationReportResponse struct {
	ID                   string                     `json:"id"`
	RunID                string                     `json:"run_id"`
	StationID            string                     `json:"station_id"`
	SubmittedByAccountID string                     `json:"submitted_by_account_id"`
	SequenceNo           int                        `json:"sequence_no"`
	UnitsTotal           int                        `json:"units_total"`
	UnitsPassed          int                        `json:"units_passed"`
	UnitsFailed          int                        `json:"units_failed"`
	OverallResult        string                     `json:"overall_result"`
	UnitResults          []manufacturing.UnitResult `json:"unit_results"`
	EvidenceSHA256       string                     `json:"evidence_sha256,omitempty"`
	Note                 string                     `json:"note,omitempty"`
	CreatedAt            string                     `json:"created_at"`
}

func newStationReportResponse(report manufacturing.StationReport) stationReportResponse {
	units := report.UnitResults
	if units == nil {
		units = []manufacturing.UnitResult{}
	}
	return stationReportResponse{
		ID: report.ID, RunID: report.RunID, StationID: report.StationID,
		SubmittedByAccountID: report.SubmittedByAccountID, SequenceNo: report.SequenceNo,
		UnitsTotal: report.UnitsTotal, UnitsPassed: report.UnitsPassed, UnitsFailed: report.UnitsFailed,
		OverallResult: report.OverallResult, UnitResults: units, EvidenceSHA256: report.EvidenceSHA256,
		Note: report.Note, CreatedAt: report.CreatedAt.UTC().Format(timeLayout),
	}
}

// withManufacturerActor 只允许 manufacturer audience 会话。
func (server *Server) withManufacturerActor(
	writer http.ResponseWriter, request *http.Request,
	handle func(ctx context.Context, actor manufacturing.Actor),
) {
	server.authenticated(writer, request, portalaccount.AudienceManufacturer,
		func(ctx context.Context, account portalaccount.Account, _ portalaccount.Session) {
			handle(ctx, portalaccount.ActorFromAccount(account))
		})
}

// serveManufacturer 处理代工厂门户资源。会话面已在 servePortal 处理。
func (server *Server) serveManufacturer(writer http.ResponseWriter, request *http.Request, path string) {
	switch {
	case path == "stations" && request.Method == http.MethodGet:
		server.listStations(writer, request)
	case path == "stations" && request.Method == http.MethodPost:
		server.createStation(writer, request)
	case strings.HasPrefix(path, "stations/") && request.Method == http.MethodPatch:
		server.updateStation(writer, request, strings.TrimPrefix(path, "stations/"))
	case path == "production-runs" && request.Method == http.MethodGet:
		server.listProductionRuns(writer, request)
	case path == "production-runs" && request.Method == http.MethodPost:
		server.createProductionRun(writer, request)
	case path == "test-reports" && request.Method == http.MethodGet:
		server.listManufacturerTestReports(writer, request)
	case path == "projects" && request.Method == http.MethodGet:
		server.listManufacturerProjects(writer, request)
	case strings.HasPrefix(path, "production-runs/"):
		server.serveManufacturerRun(writer, request, strings.TrimPrefix(path, "production-runs/"))
	default:
		writeError(writer, http.StatusNotFound, "not_found", "endpoint not found")
	}
}

func (server *Server) serveManufacturerRun(writer http.ResponseWriter, request *http.Request, rest string) {
	runID, sub, hasSub := strings.Cut(rest, "/")
	if runID == "" {
		writeError(writer, http.StatusNotFound, "not_found", "endpoint not found")
		return
	}
	switch {
	case !hasSub && request.Method == http.MethodGet:
		server.withManufacturerActor(writer, request, func(ctx context.Context, actor manufacturing.Actor) {
			run, err := server.manufacturing().GetRun(ctx, actor, runID)
			if err != nil {
				writeManufacturingError(writer, err)
				return
			}
			writeJSON(writer, http.StatusOK, map[string]any{"run": newRunResponse(run)})
		})
	case sub == "close" && request.Method == http.MethodPost:
		server.withManufacturerActor(writer, request, func(ctx context.Context, actor manufacturing.Actor) {
			run, err := server.manufacturing().CloseRun(ctx, actor, runID)
			if err != nil {
				writeManufacturingError(writer, err)
				return
			}
			writeJSON(writer, http.StatusOK, map[string]any{"run": newRunResponse(run)})
		})
	case sub == "station-reports" && request.Method == http.MethodGet:
		server.withManufacturerActor(writer, request, func(ctx context.Context, actor manufacturing.Actor) {
			reports, err := server.manufacturing().ListReports(ctx, actor, runID)
			if err != nil {
				writeManufacturingError(writer, err)
				return
			}
			writePage(writer, newStationReportResponses(reports), "")
		})
	case sub == "station-reports" && request.Method == http.MethodPost:
		server.withManufacturerActor(writer, request, func(ctx context.Context, actor manufacturing.Actor) {
			var body struct {
				StationID      string                     `json:"station_id"`
				UnitResults    []manufacturing.UnitResult `json:"unit_results"`
				EvidenceSHA256 string                     `json:"evidence_sha256"`
				Note           string                     `json:"note"`
			}
			if !decodeBody(writer, request, &body) {
				return
			}
			report, err := server.manufacturing().SubmitReport(ctx, actor, runID, manufacturing.SubmitReportInput{
				StationID: body.StationID, UnitResults: body.UnitResults,
				EvidenceSHA256: body.EvidenceSHA256, Note: body.Note,
			})
			if err != nil {
				writeManufacturingError(writer, err)
				return
			}
			writeJSON(writer, http.StatusCreated, map[string]any{"station_report": newStationReportResponse(report)})
		})
	default:
		writeError(writer, http.StatusNotFound, "not_found", "endpoint not found")
	}
}

func newStationReportResponses(reports []manufacturing.StationReport) []stationReportResponse {
	items := make([]stationReportResponse, 0, len(reports))
	for _, report := range reports {
		items = append(items, newStationReportResponse(report))
	}
	return items
}

func (server *Server) listStations(writer http.ResponseWriter, request *http.Request) {
	server.withManufacturerActor(writer, request, func(ctx context.Context, actor manufacturing.Actor) {
		stations, err := server.manufacturing().ListStations(ctx, actor, manufacturing.StationFilter{
			Status: strings.TrimSpace(request.URL.Query().Get("status")),
		})
		if err != nil {
			writeManufacturingError(writer, err)
			return
		}
		items := make([]stationResponse, 0, len(stations))
		for _, station := range stations {
			items = append(items, newStationResponse(station))
		}
		writePage(writer, items, "")
	})
}

func (server *Server) createStation(writer http.ResponseWriter, request *http.Request) {
	server.withManufacturerActor(writer, request, func(ctx context.Context, actor manufacturing.Actor) {
		var body struct {
			Code string `json:"code"`
			Name string `json:"name"`
		}
		if !decodeBody(writer, request, &body) {
			return
		}
		station, err := server.manufacturing().CreateStation(ctx, actor, manufacturing.CreateStationInput{
			Code: body.Code, Name: body.Name,
		})
		if err != nil {
			writeManufacturingError(writer, err)
			return
		}
		writeJSON(writer, http.StatusCreated, map[string]any{"station": newStationResponse(station)})
	})
}

func (server *Server) updateStation(writer http.ResponseWriter, request *http.Request, stationID string) {
	if stationID == "" || strings.Contains(stationID, "/") {
		writeError(writer, http.StatusNotFound, "not_found", "endpoint not found")
		return
	}
	server.withManufacturerActor(writer, request, func(ctx context.Context, actor manufacturing.Actor) {
		var body struct {
			Name   *string `json:"name"`
			Status *string `json:"status"`
		}
		if !decodeBody(writer, request, &body) {
			return
		}
		station, err := server.manufacturing().UpdateStation(ctx, actor, stationID,
			manufacturing.StationPatch{Name: body.Name, Status: body.Status})
		if err != nil {
			writeManufacturingError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"station": newStationResponse(station)})
	})
}

func (server *Server) listProductionRuns(writer http.ResponseWriter, request *http.Request) {
	server.withManufacturerActor(writer, request, func(ctx context.Context, actor manufacturing.Actor) {
		runs, err := server.manufacturing().ListRuns(ctx, actor, manufacturing.RunFilter{
			ProjectID: strings.TrimSpace(request.URL.Query().Get("project_id")),
			Status:    strings.TrimSpace(request.URL.Query().Get("status")),
		})
		if err != nil {
			writeManufacturingError(writer, err)
			return
		}
		items := make([]runResponse, 0, len(runs))
		for _, run := range runs {
			items = append(items, newRunResponse(run))
		}
		writePage(writer, items, "")
	})
}

func (server *Server) createProductionRun(writer http.ResponseWriter, request *http.Request) {
	server.withManufacturerActor(writer, request, func(ctx context.Context, actor manufacturing.Actor) {
		var body struct {
			ProjectID       string `json:"project_id"`
			ProductPID      string `json:"product_pid"`
			BatchCode       string `json:"batch_code"`
			PlannedQuantity *int   `json:"planned_quantity"`
		}
		if !decodeBody(writer, request, &body) {
			return
		}
		run, err := server.manufacturing().CreateRun(ctx, actor, manufacturing.CreateRunInput{
			ProjectID: body.ProjectID, ProductPID: body.ProductPID, BatchCode: body.BatchCode,
			PlannedQuantity: body.PlannedQuantity,
		})
		if err != nil {
			writeManufacturingError(writer, err)
			return
		}
		writeJSON(writer, http.StatusCreated, map[string]any{"run": newRunResponse(run)})
	})
}

func (server *Server) listManufacturerTestReports(writer http.ResponseWriter, request *http.Request) {
	server.withManufacturerActor(writer, request, func(ctx context.Context, actor manufacturing.Actor) {
		reports, err := server.manufacturing().ListManufacturerReports(ctx, actor)
		if err != nil {
			writeManufacturingError(writer, err)
			return
		}
		writePage(writer, newStationReportResponses(reports), "")
	})
}

// listManufacturerProjects 返回本厂商名下的项目（项目服务按 actor scope 派生，只读）。
func (server *Server) listManufacturerProjects(writer http.ResponseWriter, request *http.Request) {
	if !server.requireProjects(writer) {
		return
	}
	server.withManufacturerActor(writer, request, func(ctx context.Context, actor manufacturing.Actor) {
		projects, err := server.projects().ListProjects(ctx, project.Actor(actor), project.ProjectFilter{
			Status: strings.TrimSpace(request.URL.Query().Get("status")),
		})
		if err != nil {
			writeProjectError(writer, err)
			return
		}
		items := make([]projectResponse, 0, len(projects))
		for _, value := range projects {
			items = append(items, newProjectResponse(value))
		}
		writePage(writer, items, "")
	})
}

func (server *Server) listAdminManufacturers(writer http.ResponseWriter, request *http.Request) {
	server.withStaff(writer, request, func(ctx context.Context, account portalaccount.Account) {
		if server.manufacturing() == nil {
			writeError(writer, http.StatusServiceUnavailable, "manufacturing_unavailable", "manufacturing services are unavailable")
			return
		}
		manufacturers, err := server.manufacturing().ListManufacturers(ctx, portalaccount.ActorFromAccount(account), manufacturing.ManufacturerFilter{
			Status: strings.TrimSpace(request.URL.Query().Get("status")),
		})
		if err != nil {
			writeManufacturingError(writer, err)
			return
		}
		items := make([]manufacturerResponse, 0, len(manufacturers))
		for _, value := range manufacturers {
			items = append(items, newManufacturerResponse(value))
		}
		writePage(writer, items, "")
	})
}

func (server *Server) createAdminManufacturer(writer http.ResponseWriter, request *http.Request) {
	server.withStaff(writer, request, func(ctx context.Context, account portalaccount.Account) {
		if server.manufacturing() == nil {
			writeError(writer, http.StatusServiceUnavailable, "manufacturing_unavailable", "manufacturing services are unavailable")
			return
		}
		var body struct {
			Code         string `json:"code"`
			Name         string `json:"name"`
			ContactName  string `json:"contact_name"`
			ContactEmail string `json:"contact_email"`
		}
		if !decodeBody(writer, request, &body) {
			return
		}
		value, err := server.manufacturing().CreateManufacturer(ctx, portalaccount.ActorFromAccount(account), manufacturing.CreateManufacturerInput{
			Code: body.Code, Name: body.Name, ContactName: body.ContactName, ContactEmail: body.ContactEmail,
		})
		if err != nil {
			writeManufacturingError(writer, err)
			return
		}
		writeJSON(writer, http.StatusCreated, map[string]any{"manufacturer": newManufacturerResponse(value)})
	})
}

func (server *Server) updateAdminManufacturer(writer http.ResponseWriter, request *http.Request, manufacturerID string) {
	if manufacturerID == "" || strings.Contains(manufacturerID, "/") {
		writeError(writer, http.StatusNotFound, "not_found", "endpoint not found")
		return
	}
	server.withStaff(writer, request, func(ctx context.Context, account portalaccount.Account) {
		if server.manufacturing() == nil {
			writeError(writer, http.StatusServiceUnavailable, "manufacturing_unavailable", "manufacturing services are unavailable")
			return
		}
		var body struct {
			Name         *string `json:"name"`
			ContactName  *string `json:"contact_name"`
			ContactEmail *string `json:"contact_email"`
			Status       *string `json:"status"`
		}
		if !decodeBody(writer, request, &body) {
			return
		}
		value, err := server.manufacturing().UpdateManufacturer(ctx, portalaccount.ActorFromAccount(account), manufacturerID,
			manufacturing.ManufacturerPatch{
				Name: body.Name, ContactName: body.ContactName, ContactEmail: body.ContactEmail, Status: body.Status,
			})
		if err != nil {
			writeManufacturingError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"manufacturer": newManufacturerResponse(value)})
	})
}
