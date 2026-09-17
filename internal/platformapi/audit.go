package platformapi

import (
	"context"
	"net/http"
	"regexp"
	"strings"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/audit"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/portalaccount"
)

var uuidSegmentPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// statusRecorder 捕获响应状态，供审计判定结果。
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (recorder *statusRecorder) WriteHeader(status int) {
	if !recorder.wrote {
		recorder.status = status
		recorder.wrote = true
	}
	recorder.ResponseWriter.WriteHeader(status)
}

func (recorder *statusRecorder) Write(payload []byte) (int, error) {
	recorder.wrote = true
	return recorder.ResponseWriter.Write(payload)
}

// actorHolder 让审计在请求结束后拿到已鉴权账号。
// authenticated 拿到的 request 就是这里注入的，因此不需要改动任何 handler 签名。
type actorHolder struct {
	account portalaccount.Account
	set     bool
}

type actorHolderKey struct{}

func isWriteMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete:
		return true
	default:
		return false
	}
}

// describeOperation 把路径归一成稳定 operation，并抽出资源标识。
// operation 带方法前缀，避免创建与更新落到同一个名字上（审计需要能区分动作）。
// 例如 POST /api/v1/developer/projects/<uuid>/credentials
//
//	→ operation "POST developer.projects.credentials"，resource_type projects，resource_id <uuid>
func describeOperation(audience portalaccount.Audience, path, method string) (string, string, string) {
	segments := make([]string, 0, 4)
	resourceType := ""
	resourceID := ""
	for _, segment := range strings.Split(path, "/") {
		if segment == "" {
			continue
		}
		if uuidSegmentPattern.MatchString(segment) {
			if resourceID == "" {
				resourceID = segment
			}
			continue
		}
		if resourceType == "" && resourceID == "" && segment != "auth" {
			resourceType = segment
		}
		segments = append(segments, segment)
	}
	operation := strings.ToUpper(method) + " " + strings.Join(append([]string{string(audience)}, segments...), ".")
	if len(operation) > 96 {
		operation = operation[:96]
	}
	return operation, resourceType, resourceID
}

// outcomeForStatus 把 HTTP 结果映射成审计结果；调用方只关心"是否被拒绝"。
func outcomeForStatus(status int) string {
	switch {
	case status >= 500:
		return audit.OutcomeFailed
	case status >= 400:
		return audit.OutcomeRejected
	default:
		return audit.OutcomeSucceeded
	}
}

// recordPortalWrite 记录一次门户写操作。审计失败不改变响应，但必须交给
// AuditErrorHandler 记录，不能静默丢弃。
func (server *Server) recordPortalWrite(request *http.Request, audience portalaccount.Audience, path string, holder *actorHolder, status int) {
	if server.audit() == nil {
		return
	}
	operation, resourceType, resourceID := describeOperation(audience, path, request.Method)
	entry := audit.Entry{
		ActorKind:    audit.ActorPortalAccount,
		Operation:    operation,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		RequestID:    requestIDFromContext(request.Context()),
		Outcome:      outcomeForStatus(status),
		Metadata: map[string]any{
			"method": request.Method,
			"status": status,
			"path":   request.URL.Path,
		},
	}
	if holder != nil && holder.set {
		entry.ActorAccountID = holder.account.ID
		entry.ActorLabel = holder.account.Email
	} else {
		// 鉴权失败或未鉴权的写请求：没有账号主体，但仍记录这次尝试。
		entry.ActorKind = audit.ActorSystem
	}
	server.recordAudit(request.Context(), entry)
}

func (server *Server) recordAudit(ctx context.Context, entry audit.Entry) {
	if server.audit() == nil {
		return
	}
	if err := server.audit().Record(ctx, entry); err != nil && server.options.AuditErrorHandler != nil {
		server.options.AuditErrorHandler(err)
	}
}

// recordLogin 记录登录成功/失败。成功时主体是账号，失败时只留尝试的 email 标签。
func (server *Server) recordLogin(ctx context.Context, request *http.Request, audience portalaccount.Audience, email string, account portalaccount.Account, ok bool) {
	entry := audit.Entry{
		Operation:  "POST auth.login",
		RequestID:  requestIDFromContext(ctx),
		Outcome:    audit.OutcomeSucceeded,
		ActorKind:  audit.ActorPortalAccount,
		ActorLabel: email,
		Metadata:   map[string]any{"audience": string(audience), "status": http.StatusOK},
	}
	if !ok {
		entry.Outcome = audit.OutcomeRejected
		entry.ActorKind = audit.ActorSystem
		entry.Metadata["status"] = http.StatusUnauthorized
	} else {
		entry.ActorAccountID = account.ID
		entry.ActorLabel = account.Email
	}
	server.recordAudit(ctx, entry)
}
