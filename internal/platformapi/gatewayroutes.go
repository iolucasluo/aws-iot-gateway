package platformapi

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/operations"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/portalaccount"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/project"
)

const maxProxyBodyBytes = 1 << 20

func (server *Server) requireGateway(writer http.ResponseWriter) bool {
	if server.dependencies.Gateway == nil {
		writeError(writer, http.StatusServiceUnavailable, "gateway_unavailable", "gateway client is not configured")
		return false
	}
	return true
}

// withDeviceInProject 解析设备并确认它当前绑定在操作者可见的项目上。
// 平台层做这道检查是为了给出稳定的 404；真正的授权仍由 gateway 按项目主体执行。
func (server *Server) withDeviceInProject(
	writer http.ResponseWriter, request *http.Request, deviceSN string,
	handle func(ctx context.Context, actor project.Actor, projectID string),
) {
	if !server.requireOperations(writer) || !server.requireProjects(writer) {
		return
	}
	server.withDeveloperActor(writer, request, func(ctx context.Context, actor project.Actor) {
		device, err := server.operations().FindDevice(ctx, deviceSN)
		if err != nil || device.BindingProjectID == "" {
			writeError(writer, http.StatusNotFound, "not_found", "device not found")
			return
		}
		if _, err := server.projects().GetProject(ctx, actor, device.BindingProjectID); err != nil {
			writeError(writer, http.StatusNotFound, "not_found", "device not found")
			return
		}
		handle(ctx, actor, device.BindingProjectID)
	})
}

// deviceInProjectPID 返回项目作用域，并校验给定 pid 在白名单内。
func (server *Server) deviceInProjectPID(
	writer http.ResponseWriter, ctx context.Context, actor project.Actor, projectID, pid string,
) bool {
	pid = strings.TrimSpace(pid)
	if pid == "" {
		writeError(writer, http.StatusBadRequest, "invalid_request", "pid is required")
		return false
	}
	scope, err := server.projects().ScopeFor(ctx, actor, projectID)
	if err != nil {
		writeProjectError(writer, err)
		return false
	}
	for _, allowed := range scope.AllowedPIDs {
		if allowed == pid {
			return true
		}
	}
	// OTA 版本目录是全局按 pid 索引的；平台层必须把它收窄到项目白名单，
	// 否则一个项目能发布/查看任意产品的固件。
	writeError(writer, http.StatusForbidden, "pid_not_in_project", "product is not part of the project")
	return false
}

func readProxyBody(writer http.ResponseWriter, request *http.Request) ([]byte, bool) {
	if request.Body == nil {
		return nil, true
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maxProxyBodyBytes))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", "unable to read request body")
		return nil, false
	}
	if len(body) == 0 {
		return nil, true
	}
	return body, true
}

func (server *Server) proxyDeviceCommand(writer http.ResponseWriter, request *http.Request, deviceSN string) {
	if !server.requireGateway(writer) {
		return
	}
	body, ok := readProxyBody(writer, request)
	if !ok {
		return
	}
	server.withDeviceInProject(writer, request, deviceSN, func(ctx context.Context, _ project.Actor, projectID string) {
		deviceToken, err := server.dependencies.Gateway.DeviceToken(ctx, projectID, deviceSN)
		if err != nil {
			writeGatewayResponse(writer, Response{}, err)
			return
		}
		response, err := server.dependencies.Gateway.Call(ctx, projectID, "devices:operate", http.MethodPost,
			"/api/v1/openapi/devices/"+url.PathEscape(deviceSN)+"/commands", nil, body, deviceToken)
		writeGatewayResponse(writer, response, err)
	})
}

func (server *Server) proxyDeviceStatus(writer http.ResponseWriter, request *http.Request, deviceSN string) {
	if !server.requireGateway(writer) {
		return
	}
	server.withDeviceInProject(writer, request, deviceSN, func(ctx context.Context, _ project.Actor, projectID string) {
		deviceToken, err := server.dependencies.Gateway.DeviceToken(ctx, projectID, deviceSN)
		if err != nil {
			writeGatewayResponse(writer, Response{}, err)
			return
		}
		response, err := server.dependencies.Gateway.Call(ctx, projectID, "devices:read", http.MethodGet,
			"/api/v1/openapi/devices/"+url.PathEscape(deviceSN)+"/status", nil, nil, deviceToken)
		writeGatewayResponse(writer, response, err)
	})
}

func (server *Server) proxyCreateDeployment(writer http.ResponseWriter, request *http.Request, deviceSN string) {
	if !server.requireGateway(writer) {
		return
	}
	body, ok := readProxyBody(writer, request)
	if !ok {
		return
	}
	server.withDeviceInProject(writer, request, deviceSN, func(ctx context.Context, _ project.Actor, projectID string) {
		deviceToken, err := server.dependencies.Gateway.DeviceToken(ctx, projectID, deviceSN)
		if err != nil {
			writeGatewayResponse(writer, Response{}, err)
			return
		}
		response, err := server.dependencies.Gateway.Call(ctx, projectID, "ota:deploy", http.MethodPost,
			"/api/v1/openapi/devices/"+url.PathEscape(deviceSN)+"/ota-deployments", nil, body, deviceToken)
		writeGatewayResponse(writer, response, err)
	})
}

func (server *Server) proxyGetDeployment(writer http.ResponseWriter, request *http.Request, deviceSN, deploymentID string) {
	if !server.requireGateway(writer) {
		return
	}
	server.withDeviceInProject(writer, request, deviceSN, func(ctx context.Context, _ project.Actor, projectID string) {
		deviceToken, err := server.dependencies.Gateway.DeviceToken(ctx, projectID, deviceSN)
		if err != nil {
			writeGatewayResponse(writer, Response{}, err)
			return
		}
		response, err := server.dependencies.Gateway.Call(ctx, projectID, "ota:deploy", http.MethodGet,
			"/api/v1/openapi/devices/"+url.PathEscape(deviceSN)+"/ota-deployments/"+url.PathEscape(deploymentID),
			nil, nil, deviceToken)
		writeGatewayResponse(writer, response, err)
	})
}

func (server *Server) proxyListFirmwareVersions(writer http.ResponseWriter, request *http.Request) {
	if !server.requireGateway(writer) {
		return
	}
	pid := request.URL.Query().Get("pid")
	server.withDeveloperActor(writer, request, func(ctx context.Context, actor project.Actor) {
		projectID, ok := server.developerProjectForPID(writer, ctx, actor, pid)
		if !ok {
			return
		}
		response, err := server.dependencies.Gateway.Call(ctx, projectID, "ota:deploy", http.MethodGet,
			"/api/v1/openapi/ota/firmware-versions", url.Values{"pid": []string{strings.TrimSpace(pid)}}, nil, "")
		writeGatewayResponse(writer, response, err)
	})
}

func (server *Server) proxyCreateFirmwareVersion(writer http.ResponseWriter, request *http.Request) {
	if !server.requireGateway(writer) {
		return
	}
	body, ok := readProxyBody(writer, request)
	if !ok {
		return
	}
	var payload struct {
		PID string `json:"pid"`
	}
	if len(body) > 0 {
		if !decodeBodyInto(writer, body, &payload) {
			return
		}
	}
	server.withDeveloperActor(writer, request, func(ctx context.Context, actor project.Actor) {
		projectID, ok := server.developerProjectForPID(writer, ctx, actor, payload.PID)
		if !ok {
			return
		}
		response, err := server.dependencies.Gateway.Call(ctx, projectID, "ota:publish", http.MethodPost,
			"/api/v1/openapi/ota/firmware-versions", nil, body, "")
		writeGatewayResponse(writer, response, err)
	})
}

func (server *Server) proxyCompleteFirmwareVersion(writer http.ResponseWriter, request *http.Request, versionID string) {
	if !server.requireGateway(writer) {
		return
	}
	body, ok := readProxyBody(writer, request)
	if !ok {
		return
	}
	var payload struct {
		PID string `json:"pid"`
	}
	if len(body) > 0 {
		if !decodeBodyInto(writer, body, &payload) {
			return
		}
	}
	server.withDeveloperActor(writer, request, func(ctx context.Context, actor project.Actor) {
		projectID, ok := server.developerProjectForPID(writer, ctx, actor, payload.PID)
		if !ok {
			return
		}
		response, err := server.dependencies.Gateway.Call(ctx, projectID, "ota:publish", http.MethodPost,
			"/api/v1/openapi/ota/firmware-versions/"+url.PathEscape(versionID)+"/complete", nil, body, "")
		writeGatewayResponse(writer, response, err)
	})
}

// developerProjectForPID 找到"白名单里含该 pid"的操作者项目。
// 找不到时报 404/403，而不是把全局固件目录暴露给任意项目。
func (server *Server) developerProjectForPID(
	writer http.ResponseWriter, ctx context.Context, actor project.Actor, pid string,
) (string, bool) {
	pid = strings.TrimSpace(pid)
	if pid == "" {
		writeError(writer, http.StatusBadRequest, "invalid_request", "pid is required")
		return "", false
	}
	projects, err := server.projects().ListProjects(ctx, actor, project.ProjectFilter{Status: project.StatusActive})
	if err != nil {
		writeProjectError(writer, err)
		return "", false
	}
	for _, candidate := range projects {
		scope, err := server.projects().ScopeFor(ctx, actor, candidate.ID)
		if err != nil {
			continue
		}
		for _, allowed := range scope.AllowedPIDs {
			if allowed == pid {
				return candidate.ID, true
			}
		}
	}
	writeError(writer, http.StatusForbidden, "pid_not_in_project", "product is not part of any of your projects")
	return "", false
}

var _ = operations.Device{}
var _ = portalaccount.AudienceDeveloper

// serveDeveloperDevice 处理 /developer/devices/{device_sn}[/...]：
// 只读详情走本进程的运维视图，设备 I/O 与 OTA 经 gateway 代理。
func (server *Server) serveDeveloperDevice(writer http.ResponseWriter, request *http.Request, rest string) {
	deviceSN, sub, hasSub := strings.Cut(rest, "/")
	if deviceSN == "" {
		writeError(writer, http.StatusNotFound, "not_found", "endpoint not found")
		return
	}
	switch {
	case !hasSub && request.Method == http.MethodGet:
		server.getDeveloperDevice(writer, request, deviceSN)
	case sub == "status" && request.Method == http.MethodGet:
		server.proxyDeviceStatus(writer, request, deviceSN)
	case sub == "commands" && request.Method == http.MethodPost:
		server.proxyDeviceCommand(writer, request, deviceSN)
	case sub == "ota-deployments" && request.Method == http.MethodPost:
		server.proxyCreateDeployment(writer, request, deviceSN)
	case strings.HasPrefix(sub, "ota-deployments/") && request.Method == http.MethodGet:
		deploymentID := strings.TrimPrefix(sub, "ota-deployments/")
		if deploymentID == "" || strings.Contains(deploymentID, "/") {
			writeError(writer, http.StatusNotFound, "not_found", "endpoint not found")
			return
		}
		server.proxyGetDeployment(writer, request, deviceSN, deploymentID)
	default:
		writeError(writer, http.StatusNotFound, "not_found", "endpoint not found")
	}
}

// serveDeveloperOTA 处理 /developer/ota/*：固件版本发布与查看，全部经 gateway 代理。
func (server *Server) serveDeveloperOTA(writer http.ResponseWriter, request *http.Request, rest string) {
	switch {
	case rest == "firmware-versions" && request.Method == http.MethodGet:
		server.proxyListFirmwareVersions(writer, request)
	case rest == "firmware-versions" && request.Method == http.MethodPost:
		server.proxyCreateFirmwareVersion(writer, request)
	case strings.HasPrefix(rest, "firmware-versions/") && strings.HasSuffix(rest, "/complete") && request.Method == http.MethodPost:
		versionID := strings.TrimSuffix(strings.TrimPrefix(rest, "firmware-versions/"), "/complete")
		if versionID == "" || strings.Contains(versionID, "/") {
			writeError(writer, http.StatusNotFound, "not_found", "endpoint not found")
			return
		}
		server.proxyCompleteFirmwareVersion(writer, request, versionID)
	default:
		writeError(writer, http.StatusNotFound, "not_found", "endpoint not found")
	}
}
