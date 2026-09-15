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

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/portalaccount"
)

const (
	adminPrefix         = "/api/v1/admin/"
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
}

// Server 是平台面 HTTP 入口。它只服务门户前缀，不注册任何设备侧路由。
type Server struct {
	accounts *portalaccount.Service
	limiter  LoginLimiter
	options  Options
	cors     corsPolicy
}

func New(accounts *portalaccount.Service, limiter LoginLimiter, options Options) (*Server, error) {
	if accounts == nil {
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
		accounts: accounts, limiter: limiter, options: options,
		cors: newCORSPolicy(options.AllowedOrigins),
	}, nil
}

func (server *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set(requestIDHeader, requestID(request))
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
		server.serveAdmin(writer, request)
	default:
		writeError(writer, http.StatusNotFound, "not_found", "endpoint not found")
	}
}

func (server *Server) serveAdmin(writer http.ResponseWriter, request *http.Request) {
	path := strings.TrimPrefix(request.URL.Path, adminPrefix)
	switch {
	case path == "auth/login" && request.Method == http.MethodPost:
		server.login(writer, request, portalaccount.AudienceAdmin)
	case path == "auth/set-password" && request.Method == http.MethodPost:
		server.setPassword(writer, request, portalaccount.AudienceAdmin)
	case path == "auth/logout" && request.Method == http.MethodPost:
		server.authenticated(writer, request, func(_ context.Context, _ portalaccount.Account, session portalaccount.Session) {
			if err := server.accounts.Logout(request.Context(), session); err != nil {
				writeAccountError(writer, err)
				return
			}
			writeJSON(writer, http.StatusOK, map[string]string{"status": "logged_out"})
		})
	case path == "auth/me" && request.Method == http.MethodGet:
		server.authenticated(writer, request, func(_ context.Context, account portalaccount.Account, _ portalaccount.Session) {
			writeJSON(writer, http.StatusOK, map[string]any{"account": newAccountResponse(account)})
		})
	case path == "auth/password" && request.Method == http.MethodPost:
		server.authenticated(writer, request, func(_ context.Context, account portalaccount.Account, _ portalaccount.Session) {
			var body struct {
				CurrentPassword string `json:"current_password"`
				NewPassword     string `json:"new_password"`
			}
			if !decodeBody(writer, request, &body) {
				return
			}
			if err := server.accounts.ChangePassword(request.Context(), account.ID, body.CurrentPassword, body.NewPassword); err != nil {
				writeAccountError(writer, err)
				return
			}
			writeJSON(writer, http.StatusOK, map[string]string{"status": "password_changed"})
		})
	case path == "accounts" && request.Method == http.MethodGet:
		server.authenticated(writer, request, func(_ context.Context, account portalaccount.Account, _ portalaccount.Session) {
			filter := portalaccount.AccountFilter{
				Kind:   portalaccount.AccountKind(strings.TrimSpace(request.URL.Query().Get("kind"))),
				Status: strings.TrimSpace(request.URL.Query().Get("status")),
			}
			accounts, err := server.accounts.ListAccounts(request.Context(), portalaccount.ActorFromAccount(account), filter)
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
		server.authenticated(writer, request, func(_ context.Context, account portalaccount.Account, _ portalaccount.Session) {
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
			result, err := server.accounts.CreateAccount(request.Context(), portalaccount.ActorFromAccount(account), portalaccount.CreateAccountInput{
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
		server.authenticated(writer, request, func(_ context.Context, account portalaccount.Account, _ portalaccount.Session) {
			token, err := server.accounts.ResetPassword(request.Context(), portalaccount.ActorFromAccount(account), resetID)
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
		server.authenticated(writer, request, func(_ context.Context, account portalaccount.Account, _ portalaccount.Session) {
			target, err := server.accounts.GetAccount(request.Context(), portalaccount.ActorFromAccount(account), accountID)
			if err != nil {
				writeAccountError(writer, err)
				return
			}
			writeJSON(writer, http.StatusOK, map[string]any{"account": newAccountResponse(target)})
		})
	case http.MethodPatch:
		server.authenticated(writer, request, func(_ context.Context, account portalaccount.Account, _ portalaccount.Session) {
			var body struct {
				DisplayName *string `json:"display_name"`
				Role        *string `json:"role"`
				Status      *string `json:"status"`
			}
			if !decodeBody(writer, request, &body) {
				return
			}
			updated, err := server.accounts.UpdateAccount(request.Context(), portalaccount.ActorFromAccount(account), accountID,
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
	result, err := server.accounts.Login(request.Context(), audience, email, body.Password)
	if err != nil {
		writeAccountError(writer, err)
		return
	}
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
	account, err := server.accounts.SetPassword(request.Context(), body.Token, body.NewPassword)
	if err != nil {
		writeAccountError(writer, err)
		return
	}
	if account.Audience() != audience {
		writeError(writer, http.StatusUnauthorized, "unauthenticated", "setup token does not belong to this portal")
		return
	}
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
	writer http.ResponseWriter, request *http.Request,
	handle func(ctx context.Context, account portalaccount.Account, session portalaccount.Session),
) {
	account, session, err := server.accounts.Authenticate(request.Context(), portalaccount.AudienceAdmin, bearerToken(request))
	if err != nil {
		writeAccountError(writer, err)
		return
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
