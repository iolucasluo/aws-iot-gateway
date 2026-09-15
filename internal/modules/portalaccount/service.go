package portalaccount

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// SessionTTL 是会话绝对有效期；v1 不做滑动续期，过期必须重新登录。
	SessionTTL = 12 * time.Hour
	// SetupTokenTTL 是一次性邀请/重置 token 的有效期。
	SetupTokenTTL = 48 * time.Hour
	// MinPasswordLength 是 v1 口令策略；上限防止超长输入放大 Argon2 成本。
	MinPasswordLength = 12
	MaxPasswordLength = 256

	tokenBytes        = 32
	sessionTouchEvery = 5 * time.Minute
	dummyPassword     = "portal-account-timing-guard"
)

// LoginResult 是登录成功后的返回；Token 明文只在此出现一次。
type LoginResult struct {
	Token     string
	ExpiresAt time.Time
	Account   Account
}

// Service 承载门户账号的登录、会话与口令生命周期。
// 所有失败路径都返回同一组稳定错误，不区分"账号不存在"与"口令错误"。
type Service struct {
	repository Repository
	now        func() time.Time
	dummyHash  string
}

func NewService(repository Repository, now func() time.Time) (*Service, error) {
	if repository == nil {
		return nil, errors.New("portal account repository is required")
	}
	if now == nil {
		now = time.Now
	}
	guard, err := HashPassword(dummyPassword)
	if err != nil {
		return nil, fmt.Errorf("initialise login timing guard: %w", err)
	}
	return &Service{repository: repository, now: now, dummyHash: guard}, nil
}

// Login 校验口令并签发会话。audience 必须与账号 kind 对应，跨门户登录一律拒绝。
func (service *Service) Login(ctx context.Context, audience Audience, email, password string) (LoginResult, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if audience == "" || email == "" || password == "" {
		return LoginResult{}, ErrInvalidCredentials
	}
	account, err := service.repository.FindAccountByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, ErrAccountNotFound) {
			// 账号不存在时也执行一次真实代价的校验，避免用响应时间枚举账号。
			VerifyPassword(service.dummyHash, password)
			return LoginResult{}, ErrInvalidCredentials
		}
		return LoginResult{}, err
	}
	passwordOK := account.CanLogin() && VerifyPassword(account.PasswordHash, password)
	if !passwordOK || account.Audience() != audience {
		return LoginResult{}, ErrInvalidCredentials
	}
	plaintext, err := randomToken()
	if err != nil {
		return LoginResult{}, fmt.Errorf("generate portal session token: %w", err)
	}
	sessionID, err := randomUUID()
	if err != nil {
		return LoginResult{}, fmt.Errorf("generate portal session id: %w", err)
	}
	now := service.now().UTC()
	session := Session{
		ID: sessionID, AccountID: account.ID, TokenHash: HashToken(plaintext), Audience: audience,
		ExpiresAt: now.Add(SessionTTL), LastSeenAt: now, CreatedAt: now,
	}
	if err := service.repository.CreateSession(ctx, session); err != nil {
		return LoginResult{}, err
	}
	if err := service.repository.MarkAccountLogin(ctx, account.ID, now); err != nil {
		return LoginResult{}, err
	}
	account.LastLoginAt = &now
	return LoginResult{Token: plaintext, ExpiresAt: session.ExpiresAt, Account: account}, nil
}

// Authenticate 解析 Bearer token，返回账号与会话。
// 会话不存在、已吊销、已过期、audience 不匹配都返回 ErrSessionNotFound；
// 账号被禁用返回 ErrAccountDisabled，两者在 API 层都映射为 401。
func (service *Service) Authenticate(ctx context.Context, audience Audience, token string) (Account, Session, error) {
	token = strings.TrimSpace(token)
	if audience == "" || token == "" {
		return Account{}, Session{}, ErrSessionNotFound
	}
	session, err := service.repository.FindSessionByTokenHash(ctx, HashToken(token))
	if err != nil {
		if errors.Is(err, ErrSessionNotFound) {
			return Account{}, Session{}, ErrSessionNotFound
		}
		return Account{}, Session{}, err
	}
	now := service.now().UTC()
	if !session.Active(now) || session.Audience != audience {
		return Account{}, Session{}, ErrSessionNotFound
	}
	account, err := service.repository.FindAccountByID(ctx, session.AccountID)
	if err != nil {
		if errors.Is(err, ErrAccountNotFound) {
			return Account{}, Session{}, ErrSessionNotFound
		}
		return Account{}, Session{}, err
	}
	if !account.CanLogin() {
		return Account{}, Session{}, ErrAccountDisabled
	}
	if account.Audience() != audience {
		return Account{}, Session{}, ErrSessionNotFound
	}
	// last_seen 采样写：失败不影响请求，避免每个请求一次写放大。
	if now.Sub(session.LastSeenAt) >= sessionTouchEvery {
		if err := service.repository.TouchSession(ctx, session.ID, now); err == nil {
			session.LastSeenAt = now
		}
	}
	return account, session, nil
}

// Logout 吊销当前会话；重复登出是幂等的。
func (service *Service) Logout(ctx context.Context, session Session) error {
	if strings.TrimSpace(session.ID) == "" {
		return ErrSessionNotFound
	}
	return service.repository.RevokeSession(ctx, session.ID, service.now().UTC())
}

// ChangePassword 校验当前口令后写入新口令，并吊销该账号的全部会话（含当前会话），
// 调用方必须重新登录。这样口令变更不会留下旧会话。
func (service *Service) ChangePassword(ctx context.Context, accountID, currentPassword, newPassword string) error {
	if strings.TrimSpace(accountID) == "" {
		return ErrInvalidInput
	}
	if err := validateNewPassword(newPassword); err != nil {
		return err
	}
	account, err := service.repository.FindAccountByID(ctx, accountID)
	if err != nil {
		return err
	}
	if !account.CanLogin() || !VerifyPassword(account.PasswordHash, currentPassword) {
		return ErrInvalidCredentials
	}
	if VerifyPassword(account.PasswordHash, newPassword) {
		return fmt.Errorf("%w: new password must differ from the current password", ErrInvalidInput)
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	now := service.now().UTC()
	account.PasswordHash = hash
	account.UpdatedAt = now
	if err := service.repository.UpdateAccount(ctx, account); err != nil {
		return err
	}
	return service.repository.RevokeAccountSessions(ctx, account.ID, now)
}

func validateNewPassword(password string) error {
	if len([]rune(password)) < MinPasswordLength {
		return fmt.Errorf("%w: password must be at least %d characters", ErrInvalidInput, MinPasswordLength)
	}
	if len(password) > MaxPasswordLength {
		return fmt.Errorf("%w: password must be at most %d bytes", ErrInvalidInput, MaxPasswordLength)
	}
	if strings.TrimSpace(password) == "" {
		return fmt.Errorf("%w: password must not be blank", ErrInvalidInput)
	}
	return nil
}

// HashToken 与设备 token 使用同一约定：只持久化 SHA-256。
func HashToken(token string) [sha256.Size]byte { return sha256.Sum256([]byte(token)) }

func randomToken() (string, error) {
	value := make([]byte, tokenBytes)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func randomUUID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	hexValue := fmt.Sprintf("%x", value)
	return hexValue[:8] + "-" + hexValue[8:12] + "-" + hexValue[12:16] + "-" + hexValue[16:20] + "-" + hexValue[20:], nil
}
