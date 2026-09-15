package portalaccount

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Actor 是已通过会话鉴权的操作者投影，由 API 层从 Authenticate 结果构造。
type Actor struct {
	AccountID      string
	Kind           AccountKind
	Role           string
	ManufacturerID string
}

// ActorFromAccount 把账号投影成操作者。
func ActorFromAccount(account Account) Actor {
	return Actor{AccountID: account.ID, Kind: account.Kind, Role: account.Role, ManufacturerID: account.ManufacturerID}
}

func (actor Actor) isStaff() bool {
	return actor.Kind == KindStaff && actor.Role == RoleSuperAdmin
}

func (actor Actor) isManufacturerAdmin() bool {
	return actor.Kind == KindManufacturer && actor.Role == RoleManufacturerAdmin
}

// canManage 判定操作者能否管理目标账号：staff 可管理全部，厂商管理员只能管理本厂商账号。
func (actor Actor) canManage(target Account) bool {
	if actor.isStaff() {
		return true
	}
	if actor.isManufacturerAdmin() {
		return target.Kind == KindManufacturer && target.ManufacturerID == actor.ManufacturerID
	}
	return false
}

func (actor Actor) canCreate(input CreateAccountInput) error {
	if actor.isStaff() {
		return validateAccountScope(input.Kind, input.Role, input.ManufacturerID)
	}
	if actor.isManufacturerAdmin() {
		if input.Kind != KindManufacturer || input.Role != RoleManufacturerOperator || input.ManufacturerID != actor.ManufacturerID {
			return ErrForbidden
		}
		return nil
	}
	return ErrForbidden
}

// validateAccountScope 与 003 迁移的 portal_accounts_kind_role_check 保持一致，
// 让非法组合在进入数据库前就返回稳定的输入错误。
func validateAccountScope(kind AccountKind, role, manufacturerID string) error {
	manufacturerID = strings.TrimSpace(manufacturerID)
	switch kind {
	case KindStaff:
		if role != RoleSuperAdmin || manufacturerID != "" {
			return fmt.Errorf("%w: staff accounts require role %s and no manufacturer", ErrInvalidInput, RoleSuperAdmin)
		}
	case KindDeveloper:
		if role != RoleDeveloper || manufacturerID != "" {
			return fmt.Errorf("%w: developer accounts require role %s and no manufacturer", ErrInvalidInput, RoleDeveloper)
		}
	case KindManufacturer:
		if role != RoleManufacturerAdmin && role != RoleManufacturerOperator {
			return fmt.Errorf("%w: manufacturer accounts require role admin or operator", ErrInvalidInput)
		}
		if manufacturerID == "" {
			return fmt.Errorf("%w: manufacturer accounts require a manufacturer", ErrInvalidInput)
		}
	default:
		return fmt.Errorf("%w: unknown account kind %q", ErrInvalidInput, kind)
	}
	return nil
}

// CreateAccountInput 是创建账号的请求。v1 不设置初始口令：账号先建立，
// 由 CreateAccount 返回的一次性 setup token 完成首次设密。
type CreateAccountInput struct {
	Kind           AccountKind
	Role           string
	Email          string
	DisplayName    string
	ManufacturerID string
}

// CreateAccountResult 只在创建响应中出现一次 SetupToken 明文。
type CreateAccountResult struct {
	Account    Account
	SetupToken string
}

// AccountPatch 是账号更新请求；nil 字段表示不改动。
type AccountPatch struct {
	DisplayName *string
	Role        *string
	Status      *string
}

// CreateAccount 建立账号并签发一次性邀请 token。
func (service *Service) CreateAccount(ctx context.Context, actor Actor, input CreateAccountInput) (CreateAccountResult, error) {
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.ManufacturerID = strings.TrimSpace(input.ManufacturerID)
	if !validEmail(input.Email) {
		return CreateAccountResult{}, fmt.Errorf("%w: invalid email address", ErrInvalidInput)
	}
	if err := actor.canCreate(input); err != nil {
		return CreateAccountResult{}, err
	}
	if input.Kind == KindManufacturer {
		exists, err := service.repository.ManufacturerExists(ctx, input.ManufacturerID)
		if err != nil {
			return CreateAccountResult{}, err
		}
		if !exists {
			return CreateAccountResult{}, ErrManufacturerNotFound
		}
	}
	accountID, err := randomUUID()
	if err != nil {
		return CreateAccountResult{}, fmt.Errorf("generate portal account id: %w", err)
	}
	plaintext, token, err := service.newSetupToken()
	if err != nil {
		return CreateAccountResult{}, err
	}
	now := service.now().UTC()
	account := Account{
		ID: accountID, Kind: input.Kind, Role: input.Role, Email: input.Email,
		DisplayName: input.DisplayName, ManufacturerID: input.ManufacturerID,
		Status: StatusActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := service.repository.CreateAccount(ctx, account); err != nil {
		return CreateAccountResult{}, err
	}
	token.AccountID = account.ID
	if err := service.repository.CreateSetupToken(ctx, token); err != nil {
		return CreateAccountResult{}, err
	}
	return CreateAccountResult{Account: account, SetupToken: plaintext}, nil
}

// ListAccounts 按操作者 scope 返回账号：staff 透传过滤条件，厂商管理员被强制收窄到本厂商。
func (service *Service) ListAccounts(ctx context.Context, actor Actor, filter AccountFilter) ([]Account, error) {
	if actor.isStaff() {
		return service.repository.ListAccounts(ctx, filter)
	}
	if actor.isManufacturerAdmin() {
		filter.Kind = KindManufacturer
		filter.ManufacturerID = actor.ManufacturerID
		return service.repository.ListAccounts(ctx, filter)
	}
	return nil, ErrForbidden
}

// UpdateAccount 更新显示名、角色或启停状态，并施加自我保护和 scope 约束。
func (service *Service) UpdateAccount(ctx context.Context, actor Actor, accountID string, patch AccountPatch) (Account, error) {
	target, err := service.repository.FindAccountByID(ctx, accountID)
	if err != nil {
		return Account{}, err
	}
	if !actor.canManage(target) {
		return Account{}, ErrForbidden
	}
	if actor.AccountID == target.ID {
		if patch.Role != nil && *patch.Role != target.Role {
			return Account{}, fmt.Errorf("%w: an operator cannot change its own role", ErrForbidden)
		}
		if patch.Status != nil && *patch.Status != StatusActive {
			return Account{}, fmt.Errorf("%w: an operator cannot disable itself", ErrForbidden)
		}
	}
	role := target.Role
	if patch.Role != nil {
		role = strings.TrimSpace(*patch.Role)
	}
	status := target.Status
	if patch.Status != nil {
		status = strings.TrimSpace(*patch.Status)
	}
	if status != StatusActive && status != StatusDisabled {
		return Account{}, fmt.Errorf("%w: unknown account status %q", ErrInvalidInput, status)
	}
	if err := validateAccountScope(target.Kind, role, target.ManufacturerID); err != nil {
		return Account{}, err
	}
	if actor.isManufacturerAdmin() && role != RoleManufacturerOperator {
		return Account{}, fmt.Errorf("%w: a manufacturer admin can only manage operators", ErrForbidden)
	}
	if patch.DisplayName != nil {
		target.DisplayName = strings.TrimSpace(*patch.DisplayName)
	}
	target.Role = role
	target.Status = status
	target.UpdatedAt = service.now().UTC()
	if err := service.repository.UpdateAccount(ctx, target); err != nil {
		return Account{}, err
	}
	return target, nil
}

// ResetPassword 为账号签发一次性重置 token，并吊销其现有会话。
func (service *Service) ResetPassword(ctx context.Context, actor Actor, accountID string) (string, error) {
	target, err := service.repository.FindAccountByID(ctx, accountID)
	if err != nil {
		return "", err
	}
	if !actor.canManage(target) {
		return "", ErrForbidden
	}
	plaintext, token, err := service.newSetupToken()
	if err != nil {
		return "", err
	}
	token.AccountID = target.ID
	token.Purpose = PurposePasswordReset
	now := service.now().UTC()
	if err := service.repository.CreateSetupToken(ctx, token); err != nil {
		return "", err
	}
	if err := service.repository.RevokeAccountSessions(ctx, target.ID, now); err != nil {
		return "", err
	}
	return plaintext, nil
}

// SetPassword 使用一次性 token 设置新口令。token 先被原子消费，再写入新口令，
// 因此并发重复使用只可能有一次成功；成功后该账号的旧会话全部失效。
func (service *Service) SetPassword(ctx context.Context, token, newPassword string) (Account, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Account{}, ErrSetupTokenNotFound
	}
	if err := validateNewPassword(newPassword); err != nil {
		return Account{}, err
	}
	stored, err := service.repository.FindSetupTokenByHash(ctx, HashToken(token))
	if err != nil {
		if errors.Is(err, ErrSetupTokenNotFound) {
			return Account{}, ErrSetupTokenNotFound
		}
		return Account{}, err
	}
	now := service.now().UTC()
	if stored.UsedAt != nil {
		return Account{}, ErrSetupTokenUsed
	}
	if !stored.Usable(now) {
		return Account{}, ErrSetupTokenNotFound
	}
	account, err := service.repository.FindAccountByID(ctx, stored.AccountID)
	if err != nil {
		return Account{}, err
	}
	if account.Status != StatusActive {
		return Account{}, ErrAccountDisabled
	}
	if err := service.repository.ConsumeSetupToken(ctx, stored.ID, now); err != nil {
		return Account{}, err
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return Account{}, err
	}
	account.PasswordHash = hash
	account.UpdatedAt = now
	if err := service.repository.UpdateAccount(ctx, account); err != nil {
		return Account{}, err
	}
	if err := service.repository.RevokeAccountSessions(ctx, account.ID, now); err != nil {
		return Account{}, err
	}
	return account, nil
}

func (service *Service) newSetupToken() (string, SetupToken, error) {
	plaintext, err := randomToken()
	if err != nil {
		return "", SetupToken{}, fmt.Errorf("generate setup token: %w", err)
	}
	tokenID, err := randomUUID()
	if err != nil {
		return "", SetupToken{}, fmt.Errorf("generate setup token id: %w", err)
	}
	now := service.now().UTC()
	return plaintext, SetupToken{
		ID: tokenID, TokenHash: HashToken(plaintext), Purpose: PurposeInvite,
		ExpiresAt: now.Add(SetupTokenTTL), CreatedAt: now,
	}, nil
}

func validEmail(email string) bool {
	if email == "" || len(email) > 320 || strings.ContainsAny(email, " \t\r\n") {
		return false
	}
	local, domain, found := strings.Cut(email, "@")
	if !found || local == "" || domain == "" || strings.Contains(domain, "@") {
		return false
	}
	return strings.Contains(domain, ".")
}
