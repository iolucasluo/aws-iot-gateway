package portalaccount

import (
	"errors"
	"time"
)

// AccountKind 是门户归属；它同时决定可用的 role 集合与请求前缀。
type AccountKind string

const (
	KindStaff        AccountKind = "staff"
	KindDeveloper    AccountKind = "developer"
	KindManufacturer AccountKind = "manufacturer"
)

// Role 是 CHECK 约束允许的固定角色集合，v1 不建 RBAC 权限表。
const (
	RoleSuperAdmin           = "super_admin"
	RoleDeveloper            = "developer"
	RoleManufacturerAdmin    = "admin"
	RoleManufacturerOperator = "operator"
)

// Audience 是会话可命中的请求前缀，必须与账号 kind 一一对应。
type Audience string

const (
	AudienceAdmin        Audience = "admin"
	AudienceDeveloper    Audience = "developer"
	AudienceManufacturer Audience = "manufacturer"
)

// Status 是账号与会话的启停状态。
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
)

// SetupTokenPurpose 区分首次邀请与重置口令。
const (
	PurposeInvite        = "invite"
	PurposePasswordReset = "password_reset"
)

var (
	ErrInvalidInput         = errors.New("invalid portal account input")
	ErrAccountNotFound      = errors.New("portal account not found")
	ErrInvalidCredentials   = errors.New("invalid credentials")
	ErrAccountDisabled      = errors.New("portal account is disabled")
	ErrSessionNotFound      = errors.New("portal session not found")
	ErrAudienceMismatch     = errors.New("portal session audience does not match the account")
	ErrSetupTokenNotFound   = errors.New("setup token not found")
	ErrSetupTokenUsed       = errors.New("setup token already used")
	ErrEmailTaken           = errors.New("portal account email already exists")
	ErrManufacturerNotFound = errors.New("manufacturer not found")
	ErrForbidden            = errors.New("portal account operation forbidden")
)

// Account 是门户账号的持久化投影，password_hash 永不出现在 API 响应中。
type Account struct {
	ID             string
	Kind           AccountKind
	Role           string
	Email          string
	PasswordHash   string
	DisplayName    string
	ManufacturerID string
	Status         string
	LastLoginAt    *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// CanLogin 要求账号 active 且已设置口令（邀请后尚未设口令的账号不能登录）。
func (account Account) CanLogin() bool {
	return account.Status == StatusActive && account.PasswordHash != ""
}

// Audience 返回账号 kind 对应的会话 audience。
func (account Account) Audience() Audience {
	switch account.Kind {
	case KindStaff:
		return AudienceAdmin
	case KindDeveloper:
		return AudienceDeveloper
	case KindManufacturer:
		return AudienceManufacturer
	default:
		return ""
	}
}

// Session 是已签发的门户会话；TokenHash 是明文 token 的 SHA-256。
type Session struct {
	ID         string
	AccountID  string
	TokenHash  [32]byte
	Audience   Audience
	ExpiresAt  time.Time
	RevokedAt  *time.Time
	LastSeenAt time.Time
	CreatedAt  time.Time
}

// Active 判断会话在给定时刻是否仍然有效。
func (session Session) Active(now time.Time) bool {
	return session.RevokedAt == nil && now.Before(session.ExpiresAt)
}

// SetupToken 是一次性邀请/重置凭据，只保存 SHA-256。
type SetupToken struct {
	ID        string
	AccountID string
	TokenHash [32]byte
	Purpose   string
	ExpiresAt time.Time
	UsedAt    *time.Time
	CreatedAt time.Time
}

// Usable 判断一次性 token 在给定时刻是否仍可使用。
func (token SetupToken) Usable(now time.Time) bool {
	return token.UsedAt == nil && now.Before(token.ExpiresAt)
}
