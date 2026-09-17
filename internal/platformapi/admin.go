package platformapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/operations"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/portalaccount"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/project"
)

const (
	defaultPageLimit = 50
	maxPageLimit     = 200
)

// writeOperationsError 把运维视图错误映射到 HTTP 语义。
func writeOperationsError(writer http.ResponseWriter, err error) {
	writeError(writer, http.StatusInternalServerError, "internal_error", "request could not be completed")
}

// parsePage 解析 limit 与不透明 cursor。任何一个非法都返回 400，
// 不静默退回第一页——那会让前端在游标损坏时无限循环拉同一页。
func parsePage(writer http.ResponseWriter, request *http.Request) (operations.Page, bool) {
	query := request.URL.Query()
	page := operations.Page{Limit: defaultPageLimit}
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > maxPageLimit {
			writeError(writer, http.StatusBadRequest, "invalid_request", "limit must be between 1 and 200")
			return operations.Page{}, false
		}
		page.Limit = limit
	}
	if raw := strings.TrimSpace(query.Get("cursor")); raw != "" {
		cursor, ok := operations.DecodeCursor(raw)
		if !ok {
			writeError(writer, http.StatusBadRequest, "invalid_request", "cursor is invalid")
			return operations.Page{}, false
		}
		page.Before = &cursor
	}
	return page, true
}

func parseOptionalTime(writer http.ResponseWriter, value string) (*time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, true
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", "timestamps must be RFC3339")
		return nil, false
	}
	utc := parsed.UTC()
	return &utc, true
}

func parseOptionalBool(writer http.ResponseWriter, value string) (*bool, bool) {
	switch strings.TrimSpace(value) {
	case "":
		return nil, true
	case "true":
		flag := true
		return &flag, true
	case "false":
		flag := false
		return &flag, true
	default:
		writeError(writer, http.StatusBadRequest, "invalid_request", "online must be true or false")
		return nil, false
	}
}

// nextCursor 只在整页返回时给出：最后一行就是下一页的起点。
func nextCursor(limit int, returned int, createdAt time.Time, id string) string {
	if returned == 0 || returned < limit || id == "" {
		return ""
	}
	return operations.Cursor{CreatedAt: createdAt, ID: id}.Encode()
}

func writePage(writer http.ResponseWriter, items any, cursor string) {
	payload := map[string]any{"items": items}
	if cursor != "" {
		payload["next_cursor"] = cursor
	}
	writeJSON(writer, http.StatusOK, payload)
}

type bindingSummary struct {
	SubjectID             string `json:"subject_id"`
	BindingExternalUserID string `json:"binding_external_user_id"`
	ActivatedAt           string `json:"activated_at,omitempty"`
}

type deviceResponse struct {
	DeviceSN      string          `json:"device_sn"`
	PID           string          `json:"pid"`
	Status        string          `json:"status"`
	Online        bool            `json:"online"`
	LastSeenAt    string          `json:"last_seen_at,omitempty"`
	CertificateID string          `json:"certificate_id,omitempty"`
	CreatedAt     string          `json:"created_at"`
	Binding       *bindingSummary `json:"binding,omitempty"`
}

func newDeviceResponse(device operations.Device) deviceResponse {
	response := deviceResponse{
		DeviceSN: device.DeviceSN, PID: device.PID, Status: device.Status, Online: device.Online,
		CertificateID: device.CertificateID, CreatedAt: device.CreatedAt.UTC().Format(timeLayout),
	}
	if device.LastSeenAt != nil {
		response.LastSeenAt = device.LastSeenAt.UTC().Format(timeLayout)
	}
	if device.BindingProjectID != "" || device.BindingExternalUserID != "" {
		summary := &bindingSummary{
			SubjectID: device.BindingProjectID, BindingExternalUserID: device.BindingExternalUserID,
		}
		if device.BindingActivatedAt != nil {
			summary.ActivatedAt = device.BindingActivatedAt.UTC().Format(timeLayout)
		}
		response.Binding = summary
	}
	return response
}

type bindingResponse struct {
	ID                    string `json:"id"`
	DeviceSN              string `json:"device_sn"`
	PID                   string `json:"pid"`
	SubjectID             string `json:"subject_id"`
	BindingExternalUserID string `json:"binding_external_user_id"`
	Status                string `json:"status"`
	ActivatedAt           string `json:"activated_at"`
	ReleasedAt            string `json:"released_at,omitempty"`
	ReleaseReason         string `json:"release_reason,omitempty"`
	CreatedAt             string `json:"created_at"`
}

func newBindingResponse(binding operations.Binding) bindingResponse {
	response := bindingResponse{
		ID: binding.ID, DeviceSN: binding.DeviceSN, PID: binding.PID, SubjectID: binding.SubjectID,
		BindingExternalUserID: binding.BindingExternalUserID, Status: binding.Status,
		ActivatedAt:   binding.ActivatedAt.UTC().Format(timeLayout),
		ReleaseReason: binding.ReleaseReason, CreatedAt: binding.CreatedAt.UTC().Format(timeLayout),
	}
	if binding.ReleasedAt != nil {
		response.ReleasedAt = binding.ReleasedAt.UTC().Format(timeLayout)
	}
	return response
}

type activationResponse struct {
	ID                    string `json:"id"`
	PID                   string `json:"pid"`
	DeviceSN              string `json:"device_sn,omitempty"`
	SubjectID             string `json:"subject_id"`
	BindingExternalUserID string `json:"binding_external_user_id"`
	Method                string `json:"method"`
	Status                string `json:"status"`
	CreatedAt             string `json:"created_at"`
	UpdatedAt             string `json:"updated_at"`
}

func newActivationResponse(activation operations.Activation) activationResponse {
	return activationResponse{
		ID: activation.ID, PID: activation.PID, DeviceSN: activation.DeviceSN, SubjectID: activation.SubjectID,
		BindingExternalUserID: activation.BindingExternalUserID, Method: activation.Method,
		Status: activation.Status, CreatedAt: activation.CreatedAt.UTC().Format(timeLayout),
		UpdatedAt: activation.UpdatedAt.UTC().Format(timeLayout),
	}
}

type auditLogResponse struct {
	ID             string         `json:"id"`
	ActorKind      string         `json:"actor_kind,omitempty"`
	ActorAccountID string         `json:"actor_account_id,omitempty"`
	ActorLabel     string         `json:"actor_label,omitempty"`
	InternalAppID  string         `json:"internal_app_id,omitempty"`
	Operation      string         `json:"operation"`
	ResourceType   string         `json:"resource_type,omitempty"`
	ResourceID     string         `json:"resource_id,omitempty"`
	RequestID      string         `json:"request_id,omitempty"`
	Outcome        string         `json:"outcome"`
	ErrorCode      string         `json:"error_code,omitempty"`
	Metadata       map[string]any `json:"metadata"`
	CreatedAt      string         `json:"created_at"`
}

func newAuditLogResponse(entry operations.AuditEntry) auditLogResponse {
	metadata := entry.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	return auditLogResponse{
		ID: entry.ID, ActorKind: entry.ActorKind, ActorAccountID: entry.ActorAccountID,
		ActorLabel: entry.ActorLabel, InternalAppID: entry.InternalAppID, Operation: entry.Operation,
		ResourceType: entry.ResourceType, ResourceID: entry.ResourceID, RequestID: entry.RequestID,
		Outcome: entry.Outcome, ErrorCode: entry.ErrorCode, Metadata: metadata,
		CreatedAt: entry.CreatedAt.UTC().Format(timeLayout),
	}
}

// withStaff 只允许 admin audience 的会话；其它门户的 token 在这里被拒绝。
func (server *Server) withStaff(
	writer http.ResponseWriter, request *http.Request,
	handle func(ctx context.Context, account portalaccount.Account),
) {
	server.authenticated(writer, request, portalaccount.AudienceAdmin,
		func(ctx context.Context, account portalaccount.Account, _ portalaccount.Session) {
			handle(ctx, account)
		})
}

func (server *Server) requireOperations(writer http.ResponseWriter) bool {
	if server.operations() == nil {
		writeError(writer, http.StatusServiceUnavailable, "operations_unavailable", "operations views are unavailable")
		return false
	}
	return true
}

func (server *Server) requireProjects(writer http.ResponseWriter) bool {
	if server.projects() == nil {
		writeError(writer, http.StatusServiceUnavailable, "projects_unavailable", "project services are unavailable")
		return false
	}
	return true
}

func (server *Server) listDevices(writer http.ResponseWriter, request *http.Request) {
	if !server.requireOperations(writer) {
		return
	}
	page, ok := parsePage(writer, request)
	if !ok {
		return
	}
	online, ok := parseOptionalBool(writer, request.URL.Query().Get("online"))
	if !ok {
		return
	}
	server.withStaff(writer, request, func(ctx context.Context, _ portalaccount.Account) {
		devices, err := server.operations().ListDevices(ctx, operations.DeviceFilter{
			PID:    strings.TrimSpace(request.URL.Query().Get("pid")),
			Status: strings.TrimSpace(request.URL.Query().Get("status")),
			Online: online, Query: request.URL.Query().Get("q"), Page: page,
		})
		if err != nil {
			writeOperationsError(writer, err)
			return
		}
		items := make([]deviceResponse, 0, len(devices))
		for _, device := range devices {
			items = append(items, newDeviceResponse(device))
		}
		cursor := ""
		if last := len(devices) - 1; last >= 0 {
			cursor = nextCursor(page.Limit, len(devices), devices[last].CreatedAt, devices[last].ID)
		}
		writePage(writer, items, cursor)
	})
}

func (server *Server) getDevice(writer http.ResponseWriter, request *http.Request, deviceSN string) {
	if !server.requireOperations(writer) {
		return
	}
	server.withStaff(writer, request, func(ctx context.Context, _ portalaccount.Account) {
		device, err := server.operations().FindDevice(ctx, deviceSN)
		if err != nil {
			writeError(writer, http.StatusNotFound, "not_found", "device not found")
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"device": newDeviceResponse(device)})
	})
}

func (server *Server) listBindings(writer http.ResponseWriter, request *http.Request) {
	if !server.requireOperations(writer) {
		return
	}
	page, ok := parsePage(writer, request)
	if !ok {
		return
	}
	server.withStaff(writer, request, func(ctx context.Context, _ portalaccount.Account) {
		bindings, err := server.operations().ListBindings(ctx, operations.BindingFilter{
			DeviceSN:  strings.TrimSpace(request.URL.Query().Get("device_sn")),
			ProjectID: strings.TrimSpace(request.URL.Query().Get("subject_id")),
			Status:    strings.TrimSpace(request.URL.Query().Get("status")), Page: page,
		})
		if err != nil {
			writeOperationsError(writer, err)
			return
		}
		items := make([]bindingResponse, 0, len(bindings))
		for _, binding := range bindings {
			items = append(items, newBindingResponse(binding))
		}
		cursor := ""
		if last := len(bindings) - 1; last >= 0 {
			cursor = nextCursor(page.Limit, len(bindings), bindings[last].CreatedAt, bindings[last].ID)
		}
		writePage(writer, items, cursor)
	})
}

func (server *Server) listActivations(writer http.ResponseWriter, request *http.Request) {
	if !server.requireOperations(writer) {
		return
	}
	page, ok := parsePage(writer, request)
	if !ok {
		return
	}
	server.withStaff(writer, request, func(ctx context.Context, _ portalaccount.Account) {
		activations, err := server.operations().ListActivations(ctx, operations.ActivationFilter{
			PID:    strings.TrimSpace(request.URL.Query().Get("pid")),
			Status: strings.TrimSpace(request.URL.Query().Get("status")), Page: page,
		})
		if err != nil {
			writeOperationsError(writer, err)
			return
		}
		items := make([]activationResponse, 0, len(activations))
		for _, activation := range activations {
			items = append(items, newActivationResponse(activation))
		}
		cursor := ""
		if last := len(activations) - 1; last >= 0 {
			cursor = nextCursor(page.Limit, len(activations), activations[last].CreatedAt, activations[last].ID)
		}
		writePage(writer, items, cursor)
	})
}

func (server *Server) listAuditLogs(writer http.ResponseWriter, request *http.Request) {
	if !server.requireOperations(writer) {
		return
	}
	page, ok := parsePage(writer, request)
	if !ok {
		return
	}
	query := request.URL.Query()
	from, ok := parseOptionalTime(writer, query.Get("from"))
	if !ok {
		return
	}
	to, ok := parseOptionalTime(writer, query.Get("to"))
	if !ok {
		return
	}
	server.withStaff(writer, request, func(ctx context.Context, _ portalaccount.Account) {
		entries, err := server.operations().ListAuditLogs(ctx, operations.AuditFilter{
			ActorKind:      strings.TrimSpace(query.Get("actor_kind")),
			ActorAccountID: strings.TrimSpace(query.Get("actor_account_id")),
			Operation:      strings.TrimSpace(query.Get("operation")),
			Outcome:        strings.TrimSpace(query.Get("outcome")),
			From:           from, To: to, Page: page,
		})
		if err != nil {
			writeOperationsError(writer, err)
			return
		}
		items := make([]auditLogResponse, 0, len(entries))
		for _, entry := range entries {
			items = append(items, newAuditLogResponse(entry))
		}
		cursor := ""
		if last := len(entries) - 1; last >= 0 {
			cursor = nextCursor(page.Limit, len(entries), entries[last].CreatedAt, entries[last].ID)
		}
		writePage(writer, items, cursor)
	})
}

type firmwareVersionResponse struct {
	ID               string `json:"id"`
	PID              string `json:"pid"`
	Version          string `json:"version"`
	FirmwareBuild    int64  `json:"firmware_build"`
	SHA256           string `json:"sha256"`
	FileSize         int64  `json:"file_size"`
	ReleaseStatement string `json:"release_statement"`
	Status           string `json:"status"`
	PublishedBy      string `json:"published_by,omitempty"`
	AvailableAt      string `json:"available_at,omitempty"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

func newFirmwareVersionResponse(version operations.FirmwareVersion) firmwareVersionResponse {
	response := firmwareVersionResponse{
		ID: version.ID, PID: version.PID, Version: version.Version,
		FirmwareBuild: version.FirmwareBuild, SHA256: version.SHA256, FileSize: version.FileSize,
		ReleaseStatement: version.ReleaseStatement, Status: version.Status, PublishedBy: version.PublishedBy,
		CreatedAt: version.CreatedAt.UTC().Format(timeLayout),
		UpdatedAt: version.UpdatedAt.UTC().Format(timeLayout),
	}
	if version.AvailableAt != nil {
		response.AvailableAt = version.AvailableAt.UTC().Format(timeLayout)
	}
	return response
}

type otaDeploymentResponse struct {
	ID            string `json:"id"`
	DeviceSN      string `json:"device_sn"`
	PID           string `json:"pid"`
	ProjectID     string `json:"project_id,omitempty"`
	TargetVersion string `json:"target_version"`
	TargetBuild   int64  `json:"target_build"`
	State         string `json:"state"`
	FailureReason string `json:"failure_reason,omitempty"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
	CompletedAt   string `json:"completed_at,omitempty"`
}

func newOTADeploymentResponse(deployment operations.OTADeployment) otaDeploymentResponse {
	response := otaDeploymentResponse{
		ID: deployment.ID, DeviceSN: deployment.DeviceSN, PID: deployment.PID,
		ProjectID: deployment.ProjectID, TargetVersion: deployment.TargetVersion,
		TargetBuild: deployment.TargetBuild, State: deployment.State,
		FailureReason: deployment.FailureReason,
		CreatedAt:     deployment.CreatedAt.UTC().Format(timeLayout),
		UpdatedAt:     deployment.UpdatedAt.UTC().Format(timeLayout),
	}
	if deployment.CompletedAt != nil {
		response.CompletedAt = deployment.CompletedAt.UTC().Format(timeLayout)
	}
	return response
}

// listAdminFirmwareVersions 是 OTA 版本目录的 staff 只读视图。
//
// 它读平台自己的只读投影（与 gateway 同一个数据库），不借用任何项目的凭证：
// staff 看全量版本，而 developer 侧的写入仍必须走项目凭证 + gateway 授权。
func (server *Server) listAdminFirmwareVersions(writer http.ResponseWriter, request *http.Request) {
	if !server.requireOperations(writer) {
		return
	}
	page, ok := parsePage(writer, request)
	if !ok {
		return
	}
	server.withStaff(writer, request, func(ctx context.Context, _ portalaccount.Account) {
		versions, err := server.operations().ListFirmwareVersions(ctx, operations.FirmwareVersionFilter{
			PID:    strings.TrimSpace(request.URL.Query().Get("pid")),
			Status: strings.TrimSpace(request.URL.Query().Get("status")),
			Page:   page,
		})
		if err != nil {
			writeOperationsError(writer, err)
			return
		}
		items := make([]firmwareVersionResponse, 0, len(versions))
		for _, version := range versions {
			items = append(items, newFirmwareVersionResponse(version))
		}
		cursor := ""
		if last := len(versions) - 1; last >= 0 {
			cursor = nextCursor(page.Limit, len(versions), versions[last].CreatedAt, versions[last].ID)
		}
		writePage(writer, items, cursor)
	})
}

// listAdminOTADeployments 是 OTA 下发记录的 staff 只读视图（全量设备、全量项目）。
func (server *Server) listAdminOTADeployments(writer http.ResponseWriter, request *http.Request) {
	if !server.requireOperations(writer) {
		return
	}
	page, ok := parsePage(writer, request)
	if !ok {
		return
	}
	server.withStaff(writer, request, func(ctx context.Context, _ portalaccount.Account) {
		deployments, err := server.operations().ListOTADeployments(ctx, operations.OTADeploymentFilter{
			DeviceSN:  strings.TrimSpace(request.URL.Query().Get("device_sn")),
			PID:       strings.TrimSpace(request.URL.Query().Get("pid")),
			ProjectID: strings.TrimSpace(request.URL.Query().Get("project_id")),
			State:     strings.TrimSpace(request.URL.Query().Get("state")),
			Page:      page,
		})
		if err != nil {
			writeOperationsError(writer, err)
			return
		}
		items := make([]otaDeploymentResponse, 0, len(deployments))
		for _, deployment := range deployments {
			items = append(items, newOTADeploymentResponse(deployment))
		}
		cursor := ""
		if last := len(deployments) - 1; last >= 0 {
			cursor = nextCursor(page.Limit, len(deployments), deployments[last].CreatedAt, deployments[last].ID)
		}
		writePage(writer, items, cursor)
	})
}

func (server *Server) listAdminProjects(writer http.ResponseWriter, request *http.Request) {
	if !server.requireProjects(writer) {
		return
	}
	server.withStaff(writer, request, func(ctx context.Context, account portalaccount.Account) {
		values, err := server.projects().ListProjects(ctx, portalaccount.ActorFromAccount(account), project.ProjectFilter{
			Type:   project.ProjectType(strings.TrimSpace(request.URL.Query().Get("project_type"))),
			Status: strings.TrimSpace(request.URL.Query().Get("status")),
		})
		if err != nil {
			writeProjectError(writer, err)
			return
		}
		items := make([]projectResponse, 0, len(values))
		for _, value := range values {
			items = append(items, newProjectResponse(value))
		}
		writePage(writer, items, "")
	})
}

func (server *Server) getAdminProject(writer http.ResponseWriter, request *http.Request, projectID string) {
	if !server.requireProjects(writer) {
		return
	}
	server.withStaff(writer, request, func(ctx context.Context, account portalaccount.Account) {
		value, err := server.projects().GetProject(ctx, portalaccount.ActorFromAccount(account), projectID)
		if err != nil {
			writeProjectError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"project": newProjectResponse(value)})
	})
}

func (server *Server) listAdminProducts(writer http.ResponseWriter, request *http.Request) {
	if !server.requireProjects(writer) {
		return
	}
	server.withStaff(writer, request, func(ctx context.Context, account portalaccount.Account) {
		values, err := server.projects().ListProducts(ctx, portalaccount.ActorFromAccount(account), project.ProductFilter{
			Status:         strings.TrimSpace(request.URL.Query().Get("status")),
			PID:            strings.TrimSpace(request.URL.Query().Get("pid")),
			OwnerAccountID: strings.TrimSpace(request.URL.Query().Get("owner_account_id")),
		})
		if err != nil {
			writeProjectError(writer, err)
			return
		}
		items := make([]productResponse, 0, len(values))
		for _, value := range values {
			items = append(items, newProductResponse(value))
		}
		writePage(writer, items, "")
	})
}

func (server *Server) getAdminProduct(writer http.ResponseWriter, request *http.Request, pid string) {
	if !server.requireProjects(writer) {
		return
	}
	server.withStaff(writer, request, func(ctx context.Context, account portalaccount.Account) {
		value, err := server.projects().GetProduct(ctx, portalaccount.ActorFromAccount(account), pid)
		if err != nil {
			writeProjectError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"product": newProductResponse(value)})
	})
}

func (server *Server) listAdminCredentials(writer http.ResponseWriter, request *http.Request) {
	if !server.requireProjects(writer) {
		return
	}
	projectID := strings.TrimSpace(request.URL.Query().Get("project_id"))
	if projectID == "" {
		writeError(writer, http.StatusBadRequest, "invalid_request", "project_id is required")
		return
	}
	server.withStaff(writer, request, func(ctx context.Context, account portalaccount.Account) {
		credentials, err := server.projects().ListCredentials(ctx, portalaccount.ActorFromAccount(account), projectID)
		if err != nil {
			writeProjectError(writer, err)
			return
		}
		items := make([]credentialResponse, 0, len(credentials))
		for _, credential := range credentials {
			items = append(items, newCredentialResponse(credential))
		}
		writePage(writer, items, "")
	})
}
