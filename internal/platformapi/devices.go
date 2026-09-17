package platformapi

import (
	"context"
	"net/http"
	"strings"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/operations"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/portalaccount"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/project"
)

// deviceTokenResponse 刻意不含 token hash：那是凭据材料，门户只需要元数据。
type deviceTokenResponse struct {
	ID        string `json:"id"`
	BindingID string `json:"binding_id"`
	DeviceSN  string `json:"device_sn"`
	SubjectID string `json:"subject_id"`
	ExpiresAt string `json:"expires_at"`
	RevokedAt string `json:"revoked_at,omitempty"`
	CreatedAt string `json:"created_at"`
}

func newDeviceTokenResponse(token operations.DeviceToken) deviceTokenResponse {
	response := deviceTokenResponse{
		ID: token.ID, BindingID: token.BindingID, DeviceSN: token.DeviceSN, SubjectID: token.SubjectID,
		ExpiresAt: token.ExpiresAt.UTC().Format(timeLayout), CreatedAt: token.CreatedAt.UTC().Format(timeLayout),
	}
	if token.RevokedAt != nil {
		response.RevokedAt = token.RevokedAt.UTC().Format(timeLayout)
	}
	return response
}

type certificateResponse struct {
	DeviceSN       string `json:"device_sn"`
	PID            string `json:"pid"`
	Status         string `json:"status"`
	CertificateID  string `json:"certificate_id,omitempty"`
	CertificateARN string `json:"certificate_arn,omitempty"`
	Endpoint       string `json:"endpoint,omitempty"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

func newCertificateResponse(certificate operations.Certificate) certificateResponse {
	return certificateResponse{
		DeviceSN: certificate.DeviceSN, PID: certificate.PID, Status: certificate.Status,
		CertificateID: certificate.CertificateID, CertificateARN: certificate.CertificateARN,
		Endpoint: certificate.Endpoint, CreatedAt: certificate.CreatedAt.UTC().Format(timeLayout),
		UpdatedAt: certificate.UpdatedAt.UTC().Format(timeLayout),
	}
}

func (server *Server) listDeviceTokens(writer http.ResponseWriter, request *http.Request) {
	if !server.requireOperations(writer) {
		return
	}
	page, ok := parsePage(writer, request)
	if !ok {
		return
	}
	server.withStaff(writer, request, func(ctx context.Context, _ portalaccount.Account) {
		tokens, err := server.operations().ListDeviceTokens(ctx, operations.DeviceTokenFilter{
			DeviceSN: strings.TrimSpace(request.URL.Query().Get("device_sn")), Page: page,
		})
		if err != nil {
			writeOperationsError(writer, err)
			return
		}
		items := make([]deviceTokenResponse, 0, len(tokens))
		for _, token := range tokens {
			items = append(items, newDeviceTokenResponse(token))
		}
		cursor := ""
		if last := len(tokens) - 1; last >= 0 {
			cursor = nextCursor(page.Limit, len(tokens), tokens[last].CreatedAt, tokens[last].ID)
		}
		writePage(writer, items, cursor)
	})
}

func (server *Server) listCertificates(writer http.ResponseWriter, request *http.Request) {
	if !server.requireOperations(writer) {
		return
	}
	page, ok := parsePage(writer, request)
	if !ok {
		return
	}
	server.withStaff(writer, request, func(ctx context.Context, _ portalaccount.Account) {
		certificates, err := server.operations().ListCertificates(ctx, operations.CertificateFilter{
			DeviceSN: strings.TrimSpace(request.URL.Query().Get("device_sn")),
			Status:   strings.TrimSpace(request.URL.Query().Get("status")), Page: page,
		})
		if err != nil {
			writeOperationsError(writer, err)
			return
		}
		items := make([]certificateResponse, 0, len(certificates))
		for _, certificate := range certificates {
			items = append(items, newCertificateResponse(certificate))
		}
		cursor := ""
		if last := len(certificates) - 1; last >= 0 {
			cursor = nextCursor(page.Limit, len(certificates), certificates[last].CreatedAt, certificates[last].DeviceSN)
		}
		writePage(writer, items, cursor)
	})
}

// listDeveloperProjectDevices 只返回该项目当前 active binding 下的设备。
func (server *Server) listDeveloperProjectDevices(writer http.ResponseWriter, request *http.Request, projectID string) {
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
	server.withDeveloperActor(writer, request, func(ctx context.Context, actor project.Actor) {
		if _, err := server.projects().GetProject(ctx, actor, projectID); err != nil {
			writeProjectError(writer, err)
			return
		}
		devices, err := server.operations().ListDevices(ctx, operations.DeviceFilter{
			PID: strings.TrimSpace(request.URL.Query().Get("pid")), Online: online,
			Query: request.URL.Query().Get("q"), SubjectID: projectID, Page: page,
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

// getDeveloperDevice 只允许看到自己项目当前绑定的设备；越权按不存在处理。
func (server *Server) getDeveloperDevice(writer http.ResponseWriter, request *http.Request, deviceSN string) {
	if !server.requireOperations(writer) || !server.requireProjects(writer) {
		return
	}
	server.withDeveloperActor(writer, request, func(ctx context.Context, actor project.Actor) {
		device, err := server.operations().FindDevice(ctx, deviceSN)
		if err != nil {
			writeError(writer, http.StatusNotFound, "not_found", "device not found")
			return
		}
		if device.BindingProjectID == "" {
			// 未绑定设备不属于任何项目，开发者看不到。
			writeError(writer, http.StatusNotFound, "not_found", "device not found")
			return
		}
		if _, err := server.projects().GetProject(ctx, actor, device.BindingProjectID); err != nil {
			writeError(writer, http.StatusNotFound, "not_found", "device not found")
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"device": newDeviceResponse(device)})
	})
}
