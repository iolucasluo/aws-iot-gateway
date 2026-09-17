package project

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	maxWebhookEventTypes = 32
	// WebhookSecretBytes 是回调签名 secret 的随机长度。
	WebhookSecretBytes = 32
)

var webhookEventTypePattern = regexp.MustCompile("^[a-z0-9][a-z0-9._-]{0,63}$")

// Webhook 是项目级回调配置的持久化投影。secret 只存密文。
type Webhook struct {
	ID               string
	ProjectID        string
	CallbackURL      string
	SecretCiphertext []byte
	KEKID            string
	EventTypes       []string
	Status           string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// WebhookIssue 只在创建/轮换响应中出现一次明文 secret。
type WebhookIssue struct {
	Webhook Webhook
	Secret  string
}

// CreateWebhookInput 是创建回调的请求。
type CreateWebhookInput struct {
	CallbackURL string
	EventTypes  []string
}

// WebhookPatch 是回调更新请求；nil 字段表示不改动。
type WebhookPatch struct {
	CallbackURL *string
	EventTypes  *[]string
	Status      *string
}

// CreateWebhook 建立项目回调并返回一次性明文 secret。
// 回调地址必须是 https 且不带凭据、查询串或片段：投递器不会跟随重定向，
// 也不应该把签名发到带参数的地址上。
func (service *Service) CreateWebhook(ctx context.Context, actor Actor, projectID string, input CreateWebhookInput) (WebhookIssue, error) {
	target, err := service.repository.FindProjectByID(ctx, projectID)
	if err != nil {
		return WebhookIssue{}, err
	}
	if !canWriteProject(actor, target) {
		return WebhookIssue{}, ErrForbidden
	}
	callbackURL, err := validateCallbackURL(input.CallbackURL)
	if err != nil {
		return WebhookIssue{}, err
	}
	eventTypes, err := normalizeEventTypes(input.EventTypes)
	if err != nil {
		return WebhookIssue{}, err
	}
	secret, err := randomToken(WebhookSecretBytes)
	if err != nil {
		return WebhookIssue{}, fmt.Errorf("generate webhook secret: %w", err)
	}
	sealed, err := service.cipher.Seal(secret)
	if err != nil {
		return WebhookIssue{}, fmt.Errorf("seal webhook secret: %w", err)
	}
	webhookID, err := randomUUID()
	if err != nil {
		return WebhookIssue{}, fmt.Errorf("generate webhook id: %w", err)
	}
	now := service.now().UTC()
	webhook := Webhook{
		ID: webhookID, ProjectID: projectID, CallbackURL: callbackURL, SecretCiphertext: sealed,
		KEKID: service.cipher.KEKID(), EventTypes: eventTypes, Status: StatusActive,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := service.repository.CreateWebhook(ctx, webhook); err != nil {
		return WebhookIssue{}, err
	}
	return WebhookIssue{Webhook: webhook, Secret: secret}, nil
}

// ListWebhooks 返回项目回调；可见性与凭证一致（不可见 404、只读 403）。
func (service *Service) ListWebhooks(ctx context.Context, actor Actor, projectID string) ([]Webhook, error) {
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
	return service.repository.ListWebhooks(ctx, projectID)
}

// UpdateWebhook 更新回调地址、事件类型或启停状态。
func (service *Service) UpdateWebhook(ctx context.Context, actor Actor, webhookID string, patch WebhookPatch) (Webhook, error) {
	webhook, err := service.repository.FindWebhookByID(ctx, webhookID)
	if err != nil {
		return Webhook{}, err
	}
	target, err := service.repository.FindProjectByID(ctx, webhook.ProjectID)
	if err != nil {
		return Webhook{}, err
	}
	if !canWriteProject(actor, target) {
		return Webhook{}, ErrForbidden
	}
	if patch.CallbackURL != nil {
		callbackURL, err := validateCallbackURL(*patch.CallbackURL)
		if err != nil {
			return Webhook{}, err
		}
		webhook.CallbackURL = callbackURL
	}
	if patch.EventTypes != nil {
		eventTypes, err := normalizeEventTypes(*patch.EventTypes)
		if err != nil {
			return Webhook{}, err
		}
		webhook.EventTypes = eventTypes
	}
	if patch.Status != nil {
		status := strings.TrimSpace(*patch.Status)
		if status != StatusActive && status != StatusDisabled {
			return Webhook{}, fmt.Errorf("%w: unknown webhook status %q", ErrInvalidInput, status)
		}
		webhook.Status = status
	}
	webhook.UpdatedAt = service.now().UTC()
	if err := service.repository.UpdateWebhook(ctx, webhook); err != nil {
		return Webhook{}, err
	}
	return webhook, nil
}

// DeleteWebhook 删除回调；重复删除返回 ErrWebhookNotFound。
func (service *Service) DeleteWebhook(ctx context.Context, actor Actor, webhookID string) error {
	webhook, err := service.repository.FindWebhookByID(ctx, webhookID)
	if err != nil {
		return err
	}
	target, err := service.repository.FindProjectByID(ctx, webhook.ProjectID)
	if err != nil {
		return err
	}
	if !canWriteProject(actor, target) {
		return ErrForbidden
	}
	return service.repository.DeleteWebhook(ctx, webhookID)
}

// ResolveActiveWebhook 供投递器按项目解析目标：返回该项目唯一的 active 回调。
//
// "没有可用动态目标"统一返回 ErrWebhookNotFound（包括项目不存在、项目已停用、
// 回调不存在或已停用）：对投递器而言这四种情况的行为相同——回退静态配置。
func (service *Service) ResolveActiveWebhook(ctx context.Context, projectID string) (Webhook, error) {
	webhook, err := service.repository.FindActiveWebhookByProject(ctx, projectID)
	if err != nil {
		return Webhook{}, err
	}
	target, err := service.repository.FindProjectByID(ctx, projectID)
	if err != nil {
		return Webhook{}, err
	}
	if target.Status != StatusActive {
		return Webhook{}, ErrWebhookNotFound
	}
	return webhook, nil
}

// OpenWebhookSecret 解出回调签名 secret；解密失败 fail closed。
func (service *Service) OpenWebhookSecret(webhook Webhook) (string, error) {
	secret, err := service.cipher.Open(webhook.SecretCiphertext)
	if err != nil {
		return "", fmt.Errorf("open webhook secret: %w", err)
	}
	return secret, nil
}

func validateCallbackURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("%w: callback url is required", ErrInvalidInput)
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("%w: callback url is invalid", ErrInvalidInput)
	}
	if parsed.Scheme != "https" || parsed.Host == "" {
		return "", fmt.Errorf("%w: callback url must be https", ErrInvalidInput)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%w: callback url must not carry credentials, query, or fragment", ErrInvalidInput)
	}
	return parsed.String(), nil
}

func normalizeEventTypes(eventTypes []string) ([]string, error) {
	if len(eventTypes) == 0 {
		return nil, fmt.Errorf("%w: at least one webhook event type is required", ErrInvalidInput)
	}
	if len(eventTypes) > maxWebhookEventTypes {
		return nil, fmt.Errorf("%w: too many webhook event types", ErrInvalidInput)
	}
	normalized := make([]string, 0, len(eventTypes))
	seen := make(map[string]struct{}, len(eventTypes))
	for _, eventType := range eventTypes {
		eventType = strings.TrimSpace(eventType)
		if !webhookEventTypePattern.MatchString(eventType) {
			return nil, fmt.Errorf("%w: invalid webhook event type %q", ErrInvalidInput, eventType)
		}
		if _, duplicate := seen[eventType]; duplicate {
			continue
		}
		seen[eventType] = struct{}{}
		normalized = append(normalized, eventType)
	}
	return normalized, nil
}
