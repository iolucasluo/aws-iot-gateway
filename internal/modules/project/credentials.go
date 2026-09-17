package project

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/internalauth"
)

// credentialLastUsedSample 控制 last_used_at 的写放大：同一凭证最多每分钟写一次。
const credentialLastUsedSample = time.Minute

// CreateCredentialInput 是签发项目凭证的请求。
type CreateCredentialInput struct {
	Name        string
	Permissions []string
	ExpiresAt   *time.Time
}

// CreateCredential 签发新凭证并返回一次性明文 secret。
func (service *Service) CreateCredential(ctx context.Context, actor Actor, projectID string, input CreateCredentialInput) (IssueResult, error) {
	target, err := service.repository.FindProjectByID(ctx, projectID)
	if err != nil {
		return IssueResult{}, err
	}
	if !canWriteProject(actor, target) {
		return IssueResult{}, ErrForbidden
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return IssueResult{}, fmt.Errorf("%w: credential name is required", ErrInvalidInput)
	}
	permissions, err := normalizePermissions(input.Permissions)
	if err != nil {
		return IssueResult{}, err
	}
	return service.issueCredential(ctx, target.ID, input.Name, permissions, input.ExpiresAt)
}

// ProjectAPISecret 返回项目当前 active 凭证的 api key 与明文 secret，
// 供平台进程"以项目自身身份"调用 gateway 内部 API。
//
// 它刻意要求权限：平台进程不会凭空获得比项目更大的授权，
// 项目必须自己持有一条具备该权限的凭证，否则返回 ErrCredentialNotFound。
func (service *Service) ProjectAPISecret(ctx context.Context, projectID, permission string) (string, string, error) {
	permission = strings.TrimSpace(permission)
	if projectID == "" || !internalauth.ValidPermission(permission) {
		return "", "", fmt.Errorf("%w: project and a valid permission are required", ErrInvalidInput)
	}
	credential, err := service.repository.FindActiveCredentialForPermission(ctx, projectID, permission)
	if err != nil {
		return "", "", err
	}
	secret, err := service.cipher.Open(credential.SecretCiphertext)
	if err != nil {
		return "", "", fmt.Errorf("open credential secret: %w", err)
	}
	return credential.APIKey, secret, nil
}

// ListCredentials 返回项目凭证；SecretCiphertext 不允许出现在 API 响应里，
// 由调用方（transport）负责投影。
//
// 不可见的项目按 ErrProjectNotFound 处理（不泄露存在性）；可见但不可写
// （例如厂商对项目只有只读权限）才返回 ErrForbidden。
func (service *Service) ListCredentials(ctx context.Context, actor Actor, projectID string) ([]Credential, error) {
	target, err := service.repository.FindProjectByID(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if !canReadProject(actor, target) {
		return nil, ErrProjectNotFound
	}
	if !canWriteProject(actor, target) {
		return nil, ErrForbidden
	}
	return service.repository.ListCredentials(ctx, projectID)
}

// RotateCredential 轮换凭证：旧凭证立即失效，主体仍是同一个 project。
func (service *Service) RotateCredential(ctx context.Context, actor Actor, credentialID string) (IssueResult, error) {
	credential, err := service.repository.FindCredentialByID(ctx, credentialID)
	if err != nil {
		return IssueResult{}, err
	}
	target, err := service.repository.FindProjectByID(ctx, credential.ProjectID)
	if err != nil {
		return IssueResult{}, err
	}
	if !canWriteProject(actor, target) {
		return IssueResult{}, ErrForbidden
	}
	now := service.now().UTC()
	credential.Status = StatusDisabled
	credential.UpdatedAt = now
	if err := service.repository.UpdateCredential(ctx, credential); err != nil {
		return IssueResult{}, err
	}
	return service.issueCredential(ctx, target.ID, credential.Name, credential.Permissions, credential.ExpiresAt)
}

// DisableCredential 停用凭证；重复停用是幂等的。
func (service *Service) DisableCredential(ctx context.Context, actor Actor, credentialID string) (Credential, error) {
	credential, err := service.repository.FindCredentialByID(ctx, credentialID)
	if err != nil {
		return Credential{}, err
	}
	target, err := service.repository.FindProjectByID(ctx, credential.ProjectID)
	if err != nil {
		return Credential{}, err
	}
	if !canWriteProject(actor, target) {
		return Credential{}, ErrForbidden
	}
	credential.Status = StatusDisabled
	credential.UpdatedAt = service.now().UTC()
	if err := service.repository.UpdateCredential(ctx, credential); err != nil {
		return Credential{}, err
	}
	return credential, nil
}

// FindCredentialByAPIKey 实现 internalauth.CredentialStore。
//
// 主体 id 取 project_id，而不是凭证行 id：这样轮换凭证不会丢失设备归属。
// 项目被停用时其全部凭证立即失效；无法解密的密文按来源不可用 fail closed。
//
// 三种结果被刻意区分：不存在 → ErrUnknownAPIKey（允许回退静态来源）；
// 存在但不可用 → ErrCredentialRevoked（不回退）；其它错误 → 传播（fail closed）。
func (service *Service) FindCredentialByAPIKey(ctx context.Context, apiKey string) (internalauth.Credential, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return internalauth.Credential{}, internalauth.ErrUnknownAPIKey
	}
	credential, err := service.repository.FindCredentialByAPIKey(ctx, apiKey)
	if err != nil {
		if errors.Is(err, ErrCredentialNotFound) {
			return internalauth.Credential{}, internalauth.ErrUnknownAPIKey
		}
		return internalauth.Credential{}, err
	}
	now := service.now().UTC()
	if !credential.Usable(now) {
		return internalauth.Credential{}, internalauth.ErrCredentialRevoked
	}
	target, err := service.repository.FindProjectByID(ctx, credential.ProjectID)
	if err != nil {
		if errors.Is(err, ErrProjectNotFound) {
			return internalauth.Credential{}, internalauth.ErrCredentialRevoked
		}
		return internalauth.Credential{}, err
	}
	if target.Status != StatusActive {
		return internalauth.Credential{}, internalauth.ErrCredentialRevoked
	}
	secret, err := service.cipher.Open(credential.SecretCiphertext)
	if err != nil {
		return internalauth.Credential{}, fmt.Errorf("open credential secret: %w", err)
	}
	if credential.LastUsedAt == nil || now.Sub(*credential.LastUsedAt) >= credentialLastUsedSample {
		// 采样写失败不影响验签结果。
		_ = service.repository.TouchCredentialUsed(ctx, credential.ID, now)
	}
	return internalauth.Credential{
		InternalAppID: target.ID,
		ProjectID:     target.ID,
		APIKey:        credential.APIKey,
		APISecret:     secret,
		Permissions:   append([]string(nil), credential.Permissions...),
	}, nil
}

func (service *Service) issueCredential(ctx context.Context, projectID, name string, permissions []string, expiresAt *time.Time) (IssueResult, error) {
	credentialID, err := randomUUID()
	if err != nil {
		return IssueResult{}, fmt.Errorf("generate credential id: %w", err)
	}
	secret, err := randomToken(apiSecretBytes)
	if err != nil {
		return IssueResult{}, fmt.Errorf("generate credential secret: %w", err)
	}
	keySuffix, err := randomToken(apiKeyBytes)
	if err != nil {
		return IssueResult{}, fmt.Errorf("generate credential key: %w", err)
	}
	sealed, err := service.cipher.Seal(secret)
	if err != nil {
		return IssueResult{}, fmt.Errorf("seal credential secret: %w", err)
	}
	now := service.now().UTC()
	credential := Credential{
		ID: credentialID, ProjectID: projectID, Name: name, APIKey: "ak_" + keySuffix,
		SecretCiphertext: sealed, KEKID: service.cipher.KEKID(), Permissions: permissions,
		Status: StatusActive, ExpiresAt: expiresAt, CreatedAt: now, UpdatedAt: now,
	}
	if err := service.repository.CreateCredential(ctx, credential); err != nil {
		return IssueResult{}, err
	}
	return IssueResult{Credential: credential, APISecret: secret}, nil
}

// normalizePermissions 去重、排序并拒绝白名单外的权限。
func normalizePermissions(permissions []string) ([]string, error) {
	normalized := make([]string, 0, len(permissions))
	seen := make(map[string]struct{}, len(permissions))
	for _, permission := range permissions {
		permission = strings.TrimSpace(permission)
		if !internalauth.ValidPermission(permission) {
			return nil, fmt.Errorf("%w: unknown permission %q", ErrInvalidInput, permission)
		}
		if _, duplicate := seen[permission]; duplicate {
			continue
		}
		seen[permission] = struct{}{}
		normalized = append(normalized, permission)
	}
	return normalized, nil
}
