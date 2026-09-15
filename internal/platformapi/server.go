package platformapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/audit"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/operations"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/portalaccount"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/project"
)

const (
	adminPrefix         = "/api/v1/admin/"
	developerPrefix     = "/api/v1/developer/"
	maxRequestBodyBytes = 8 << 10
)

// LoginLimiter 是登录限流的最小依赖，生产实现复用 Redis 固定窗口。
type LoginLimiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error)
}

// Options 是平台 API 的运行参数。
type Options struct {
	AllowedOrigins     []string
	LoginRateLimit     int
	LoginRateWindow    time.Duration
	TrustedProxyHeader string
	Now                func() time.Time
	// AuditErrorHandler 接收审计写入失败。审计不可用不应让业务请求失败，
	// 但必须有人记录它，默认丢弃是明确不接受的。
	AuditErrorHandler func(error)
}

// AuditRecorder 是审计写入边界。
type AuditRecorder interface {
	Record(ctx context.Context, entry audit.Entry) error
}

// Dependencies 是平台 API 的协作者。集中成一个结构体，
// 避免每加一项能力就改一次构造函数签名。
type Dependencies struct {
	Accounts   *portalaccount.Service
	Projects   *project.Service
	Audit      AuditRecorder
	Operations *operations.QueryService
}

// Server 是平台面 HTTP 入口。它只服务门户前缀，不注册任何设备侧路由。
type Server struct {
	dependencies Dependencies
	limiter      LoginLimiter
	options      Options
	cors         corsPolicy
}

func (server *Server) accounts() *portalaccount.Service { return server.dependencies.Accounts }
func (server *Server) projects() *project.Service       { return server.dependencies.Projects }
func (server *Server) audit() AuditRecorder             { return server.dependencies.Audit }
func (server *Server) operations() *operations.QueryService {
	return server.dependencies.Operations
}

// New 组装平台 API。缺少的必要协作者不会静默降级：
// Accounts 必填；Projects 缺失时 developer/admin 的项目类路由返回 503；
// Audit 缺失时不写审计（仅限测试与本地开发）；Operations 缺失时运维视图返回 503。
func New(dependencies Dependencies, limiter LoginLimiter, options Options) (*Server, error) {
	if dependencies.Accounts == nil {
		return nil, errors.New("platform api requires an account service")
	}
	if limiter == nil {
		return nil, errors.New("platform api requires a login limiter")
	}
	if options.LoginRateLimit <= 0 {
		return nil, errors.New("platform api requires a positive login rate limit")
	}
	if options.LoginRateWindow <= 0 {
		return nil, errors.New("platform api requires a positive login rate window")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Server{
		dependencies: dependencies, limiter: limiter, options: options,
		cors: newCORSPolicy(options.AllowedOrigins),
	}, nil
}

func (server *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	requestID := requestID(request)
	writer.Header().Set(requestIDHeader, requestID)
	request = request.WithContext(withRequestID(request.Context(), requestID))
	if server.cors.apply(writer, request) {
		return
	}
	switch {
	case request.Method == http.MethodGet && request.URL.Path == "/healthz":
		writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
	case request.Method == http.MethodGet && request.URL.Path == "/readyz":
		// 平台进程的依赖就绪由 main 注入的探针决定，这里只表示进程在服务。
		writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
	case strings.HasPrefix(request.URL.Path, adminPrefix):
		server.servePortal(writer, request, portalaccount.AudienceAdmin, adminPrefix)
	case strings.HasPrefix(request.URL.Path, developerPrefix):
		server.servePortal(writer, request, portalaccount.AudienceDeveloper, developerPrefix)
	default:
		writeError(writer, http.StatusNotFound, "not_found", "endpoint not found")
	}
}

// servePortal 处理三个门户共用的会话面，再按 audience 分派各自资源。
// audience 直接决定 token 可命中的前缀，跨门户拿到的会话在这里被拒绝。
func (server *Server) servePortal(writer http.ResponseWriter, request *http.Request, audience portalaccount.Audience, prefix string) {
	path := strings.TrimPrefix(request.URL.Path, prefix)
	switch {
	case path == "auth/login" && request.Method == http.MethodPost:
		server.login(writer, request, audience)
	case path == "auth/set-password" && request.Method == http.MethodPost:
		server.setPassword(writer, request, audience)
	case path == "auth/logout" && request.Method == http.MethodPost:
		server.authenticated(writer, request, audience, func(_ context.Context, _ portalaccount.Account, session portalaccount.Session) {
			if err := server.accounts().Logout(request.Context(), session); err != nil {
				writeAccountError(writer, err)
				return
			}
			writeJSON(writer, http.StatusOK, map[string]string{"status": "logged_out"})
		})
	case path == "auth/me" && request.Method == http.MethodGet:
		server.authenticated(writer, request, audience, func(_ context.Context, account portalaccount.Account, _ portalaccount.Session) {
			writeJSON(writer, http.StatusOK, map[string]any{"account": newAccountResponse(account)})
		})
	case path == "auth/password" && request.Method == http.MethodPost:
		server.authenticated(writer, request, audience, func(_ context.Context, account portalaccount.Account, _ portalaccount.Session) {
			var body struct {
				CurrentPassword string `json:"current_password"`
				NewPassword     string `json:"new_password"`
			}
			if !decodeBody(writer, request, &body) {
				return
			}
			if err := server.accounts().ChangePassword(request.Context(), account.ID, body.CurrentPassword, body.NewPassword); err != nil {
				writeAccountError(writer, err)
				return
			}
			writeJSON(writer, http.StatusOK, map[string]string{"status": "password_changed"})
		})
	default:
		server.serveResources(writer, request, audience, path)
	}
}

func (server *Server) serveResources(writer http.ResponseWriter, request *http.Request, audience portalaccount.Audience, path string) {
	// 只有依赖项目域的门户才需要它；staff 的账号治理不因此降级。
	if audience == portalaccount.AudienceDeveloper && server.projects() == nil {
		writeError(writer, http.StatusServiceUnavailable, "projects_unavailable", "project services are unavailable")
		return
	}
	dispatch := func(target http.ResponseWriter, incoming *http.Request) {
		if audience == portalaccount.AudienceDeveloper {
			server.serveDeveloper(target, incoming, path)
			return
		}
		server.serveAdminResources(target, incoming, path)
	}
	if server.audit() == nil || !isWriteMethod(request.Method) {
		dispatch(writer, request)
		return
	}
	// 写操作才需要捕获状态与主体；holder 让已鉴权账号在响应结束后仍然可见。
	holder := &actorHolder{}
	captured := &statusRecorder{ResponseWriter: writer, status: http.StatusOK}
	audited := request.WithContext(context.WithValue(request.Context(), actorHolderKey{}, holder))
	dispatch(captured, audited)
	server.recordPortalWrite(audited, audience, path, holder, captured.status)
}

// serveAdminResources 处理 staff 专属资源。会话面已在 servePortal 处理。
func (server *Server) serveAdminResources(writer http.ResponseWriter, request *http.Request, path string) {
	switch {
	case path == "accounts" && request.Method == http.MethodGet:
		server.authenticated(writer, request, portalaccount.AudienceAdmin, func(_ context.Context, account portalaccount.Account, _ portalaccount.Session) {
			filter := portalaccount.AccountFilter{
				Kind:   portalaccount.AccountKind(strings.TrimSpace(request.URL.Query().Get("kind"))),
				Status: strings.TrimSpace(request.URL.Query().Get("status")),
			}
			accounts, err := server.accounts().ListAccounts(request.Context(), portalaccount.ActorFromAccount(account), filter)
			if err != nil {
				writeAccountError(writer, err)
				return
			}
			items := make([]accountResponse, 0, len(accounts))
			for _, item := range accounts {
				items = append(items, newAccountResponse(item))
			}
			writeJSON(writer, http.StatusOK, map[string]any{"items": items})
		})
	case path == "accounts" && request.Method == http.MethodPost:
		server.authenticated(writer, request, portalaccount.AudienceAdmin, func(_ context.Context, account portalaccount.Account, _ portalaccount.Session) {
			var body struct {
				Kind           string `json:"kind"`
				Role           string `json:"role"`
				Email          string `json:"email"`
				DisplayName    string `json:"display_name"`
				ManufacturerID string `json:"manufacturer_id"`
			}
			if !decodeBody(writer, request, &body) {
				return
			}
			result, err := server.accounts().CreateAccount(request.Context(), portalaccount.ActorFromAccount(account), portalaccount.CreateAccountInput{
				Kind: portalaccount.AccountKind(strings.TrimSpace(body.Kind)), Role: strings.TrimSpace(body.Role),
				Email: body.Email, DisplayName: body.DisplayName, ManufacturerID: body.ManufacturerID,
			})
			if err != nil {
				writeAccountError(writer, err)
				return
			}
			writeJSON(writer, http.StatusCreated, map[string]any{
				"account":     newAccountResponse(result.Account),
				"setup_token": result.SetupToken,
			})
		})
	default:
		server.serveAccountResource(writer, request, path)
	}
}

func (server *Server) serveAccountResource(writer http.ResponseWriter, request *http.Request, path string) {
	accountID, found := strings.CutPrefix(path, "accounts/")
	if !found || accountID == "" {
		writeError(writer, http.StatusNotFound, "not_found", "endpoint not found")
		return
	}
	if resetID, isReset := strings.CutSuffix(accountID, "/password-reset"); isReset {
		if request.Method != http.MethodPost || resetID == "" || strings.Contains(resetID, "/") {
			writeError(writer, http.StatusNotFound, "not_found", "endpoint not found")
			return
		}
		server.authenticated(writer, request, portalaccount.AudienceAdmin, func(_ context.Context, account portalaccount.Account, _ portalaccount.Session) {
			token, err := server.accounts().ResetPassword(request.Context(), portalaccount.ActorFromAccount(account), resetID)
			if err != nil {
				writeAccountError(writer, err)
				return
			}
			writeJSON(writer, http.StatusOK, map[string]string{"setup_token": token})
		})
		return
	}
	if strings.Contains(accountID, "/") {
		writeError(writer, http.StatusNotFound, "not_found", "endpoint not found")
		return
	}
	switch request.Method {
	case http.MethodGet:
		server.authenticated(writer, request, portalaccount.AudienceAdmin, func(_ context.Context, account portalaccount.Account, _ portalaccount.Session) {
			target, err := server.accounts().GetAccount(request.Context(), portalaccount.ActorFromAccount(account), accountID)
			if err != nil {
				writeAccountError(writer, err)
				return
			}
			writeJSON(writer, http.StatusOK, map[string]any{"account": newAccountResponse(target)})
		})
	case http.MethodPatch:
		server.authenticated(writer, request, portalaccount.AudienceAdmin, func(_ context.Context, account portalaccount.Account, _ portalaccount.Session) {
			var body struct {
				DisplayName *string `json:"display_name"`
				Role        *string `json:"role"`
				Status      *string `json:"status"`
			}
			if !decodeBody(writer, request, &body) {
				return
			}
			updated, err := server.accounts().UpdateAccount(request.Context(), portalaccount.ActorFromAccount(account), accountID,
				portalaccount.AccountPatch{DisplayName: body.DisplayName, Role: body.Role, Status: body.Status})
			if err != nil {
				writeAccountError(writer, err)
				return
			}
			writeJSON(writer, http.StatusOK, map[string]any{"account": newAccountResponse(updated)})
		})
	default:
		writeError(writer, http.StatusNotFound, "not_found", "endpoint not found")
	}
}

func (server *Server) login(writer http.ResponseWriter, request *http.Request, audience portalaccount.Audience) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	if email != "" {
		allowed, err := server.allow(audience, "email:"+email, request)
		if err != nil {
			writeError(writer, http.StatusServiceUnavailable, "rate_limit_unavailable", "login throttling is unavailable")
			return
		}
		if !allowed {
			writeError(writer, http.StatusTooManyRequests, "too_many_requests", "too many login attempts")
			return
		}
	}
	if allowed, err := server.allow(audience, "ip:"+clientIP(request, server.options.TrustedProxyHeader), request); err != nil {
		writeError(writer, http.StatusServiceUnavailable, "rate_limit_unavailable", "login throttling is unavailable")
		return
	} else if !allowed {
		writeError(writer, http.StatusTooManyRequests, "too_many_requests", "too many login attempts")
		return
	}
	result, err := server.accounts().Login(request.Context(), audience, email, body.Password)
	if err != nil {
		server.recordLogin(request.Context(), request, audience, email, portalaccount.Account{}, false)
		writeAccountError(writer, err)
		return
	}
	server.recordLogin(request.Context(), request, audience, email, result.Account, true)
	writeJSON(writer, http.StatusOK, map[string]any{
		"token":      result.Token,
		"expires_at": result.ExpiresAt.UTC().Format(timeLayout),
		"account":    newAccountResponse(result.Account),
	})
}

// setPassword 用一次性 setup token 设置首次口令或重置口令。
// token 本身就是凭据，因此不需要会话，但仍按 IP 限流以防暴力尝试。
// token 所属账号必须属于当前门户前缀，否则返回 401，不允许跨门户兑换。
func (server *Server) setPassword(writer http.ResponseWriter, request *http.Request, audience portalaccount.Audience) {
	var body struct {
		Token       string `json:"token"`
		NewPassword string `json:"new_password"`
	}
	if !decodeBody(writer, request, &body) {
		return
	}
	allowed, err := server.allow(audience, "set-password:ip:"+clientIP(request, server.options.TrustedProxyHeader), request)
	if err != nil {
		writeError(writer, http.StatusServiceUnavailable, "rate_limit_unavailable", "password setup throttling is unavailable")
		return
	}
	if !allowed {
		writeError(writer, http.StatusTooManyRequests, "too_many_requests", "too many password setup attempts")
		return
	}
	account, err := server.accounts().SetPassword(request.Context(), body.Token, body.NewPassword)
	if err != nil {
		server.recordAudit(request.Context(), audit.Entry{
			ActorKind: audit.ActorSystem, Operation: "POST auth.set_password",
			RequestID: requestIDFromContext(request.Context()), Outcome: audit.OutcomeRejected,
			Metadata: map[string]any{"audience": string(audience)},
		})
		writeAccountError(writer, err)
		return
	}
	if account.Audience() != audience {
		writeError(writer, http.StatusUnauthorized, "unauthenticated", "setup token does not belong to this portal")
		return
	}
	server.recordAudit(request.Context(), audit.Entry{
		ActorKind: audit.ActorPortalAccount, ActorAccountID: account.ID, ActorLabel: account.Email,
		Operation: "POST auth.set_password", RequestID: requestIDFromContext(request.Context()),
		Outcome: audit.OutcomeSucceeded, Metadata: map[string]any{"audience": string(audience)},
	})
	writeJSON(writer, http.StatusOK, map[string]any{"status": "password_set", "account": newAccountResponse(account)})
}

func (server *Server) allow(audience portalaccount.Audience, scope string, request *http.Request) (bool, error) {
	key := "platform:" + string(audience) + ":" + scope
	allowed, err := server.limiter.Allow(request.Context(), key, server.options.LoginRateLimit, server.options.LoginRateWindow)
	if err != nil {
		return false, fmt.Errorf("login rate limit: %w", err)
	}
	return allowed, nil
}

func (server *Server) authenticated(
	writer http.ResponseWriter, request *http.Request, audience portalaccount.Audience,
	handle func(ctx context.Context, account portalaccount.Account, session portalaccount.Session),
) {
	account, session, err := server.accounts().Authenticate(request.Context(), audience, bearerToken(request))
	if err != nil {
		writeAccountError(writer, err)
		return
	}
	if holder, ok := request.Context().Value(actorHolderKey{}).(*actorHolder); ok {
		holder.account = account
		holder.set = true
	}
	handle(request.Context(), account, session)
}

func decodeBody(writer http.ResponseWriter, request *http.Request, target any) bool {
	if request.Body == nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", "request body is required")
		return false
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, maxRequestBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		if errors.Is(err, io.EOF) {
			writeError(writer, http.StatusBadRequest, "invalid_request", "request body is required")
			return false
		}
		writeError(writer, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return false
	}
	return true
}
