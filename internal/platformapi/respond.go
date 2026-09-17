package platformapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/portalaccount"
)

// writeJSON 与设备侧保持同一约定：直接写对象，不包裹 envelope。
func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

// writeError 复用设备侧的响应形状 {"error_code","message","error"}，
// 让门户可以继续用同一套错误归一逻辑。
func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]string{"error_code": code, "message": message, "error": message})
}

// writeAccountError 把账号域错误稳定映射到 HTTP 语义。
// 认证类失败统一 401，不区分"账号不存在"与"口令错误"。
func writeAccountError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, portalaccount.ErrInvalidCredentials),
		errors.Is(err, portalaccount.ErrSessionNotFound),
		errors.Is(err, portalaccount.ErrAccountDisabled):
		writeError(writer, http.StatusUnauthorized, "unauthenticated", "authentication is required")
	case errors.Is(err, portalaccount.ErrForbidden),
		errors.Is(err, portalaccount.ErrAudienceMismatch):
		writeError(writer, http.StatusForbidden, "forbidden", "operation is not allowed")
	case errors.Is(err, portalaccount.ErrAccountNotFound):
		writeError(writer, http.StatusNotFound, "not_found", "account not found")
	case errors.Is(err, portalaccount.ErrManufacturerNotFound):
		writeError(writer, http.StatusNotFound, "manufacturer_not_found", "manufacturer not found")
	case errors.Is(err, portalaccount.ErrEmailTaken):
		writeError(writer, http.StatusConflict, "email_taken", "email address is already registered")
	case errors.Is(err, portalaccount.ErrSetupTokenUsed):
		writeError(writer, http.StatusConflict, "setup_token_used", "setup token was already used")
	case errors.Is(err, portalaccount.ErrSetupTokenNotFound):
		writeError(writer, http.StatusBadRequest, "setup_token_invalid", "setup token is invalid or expired")
	case errors.Is(err, portalaccount.ErrInvalidInput):
		writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid")
	default:
		writeError(writer, http.StatusInternalServerError, "internal_error", "request could not be completed")
	}
}

// accountResponse 是账号的对外投影；password_hash 永远不出现在响应里。
type accountResponse struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	Role           string `json:"role"`
	Email          string `json:"email"`
	DisplayName    string `json:"display_name,omitempty"`
	ManufacturerID string `json:"manufacturer_id,omitempty"`
	Status         string `json:"status"`
	LastLoginAt    string `json:"last_login_at,omitempty"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

func newAccountResponse(account portalaccount.Account) accountResponse {
	response := accountResponse{
		ID: account.ID, Kind: string(account.Kind), Role: account.Role, Email: account.Email,
		DisplayName: account.DisplayName, ManufacturerID: account.ManufacturerID, Status: account.Status,
		CreatedAt: account.CreatedAt.UTC().Format(timeLayout), UpdatedAt: account.UpdatedAt.UTC().Format(timeLayout),
	}
	if account.LastLoginAt != nil {
		response.LastLoginAt = account.LastLoginAt.UTC().Format(timeLayout)
	}
	return response
}

const timeLayout = "2006-01-02T15:04:05Z"
