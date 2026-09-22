package project

import (
	"context"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/portalaccount"
)

// Repository 是项目域的持久化边界。
// 唯一性冲突必须转成稳定错误（如 ErrDuplicateProduct），不让调用方解析数据库错误。
type Repository interface {
	CreateProject(ctx context.Context, project Project) error
	FindProjectByID(ctx context.Context, projectID string) (Project, error)
	ListProjects(ctx context.Context, filter ProjectFilter) ([]Project, error)
	UpdateProject(ctx context.Context, project Project) error
	DeleteProject(ctx context.Context, projectID string) error

	AddProjectProduct(ctx context.Context, id, projectID, productID string, at time.Time) error
	RemoveProjectProduct(ctx context.Context, projectID, productID string) error
	ListProjectProductIDs(ctx context.Context, projectID string) ([]string, error)
	ListProjectPIDs(ctx context.Context, projectID string) ([]string, error)

	ManufacturerExists(ctx context.Context, manufacturerID string) (bool, error)
	FindPortalAccount(ctx context.Context, accountID string) (portalaccount.Account, error)

	CreateProduct(ctx context.Context, product Product) error
	FindProductByPID(ctx context.Context, pid string) (Product, error)
	ListProducts(ctx context.Context, filter ProductFilter) ([]Product, error)
	UpdateProduct(ctx context.Context, product Product) error
	ActiveDPProfileExists(ctx context.Context, code string) (bool, error)
	ListActiveDPProfiles(ctx context.Context) ([]DPProfile, error)

	CreateWebhook(ctx context.Context, webhook Webhook) error
	FindWebhookByID(ctx context.Context, webhookID string) (Webhook, error)
	FindActiveWebhookByProject(ctx context.Context, projectID string) (Webhook, error)
	ListWebhooks(ctx context.Context, projectID string) ([]Webhook, error)
	UpdateWebhook(ctx context.Context, webhook Webhook) error
	DeleteWebhook(ctx context.Context, webhookID string) error

	CreateCredential(ctx context.Context, credential Credential) error
	FindCredentialByID(ctx context.Context, credentialID string) (Credential, error)
	FindActiveCredentialForPermission(ctx context.Context, projectID, permission string) (Credential, error)
	FindCredentialByAPIKey(ctx context.Context, apiKey string) (Credential, error)
	ListCredentials(ctx context.Context, projectID string) ([]Credential, error)
	UpdateCredential(ctx context.Context, credential Credential) error
	TouchCredentialUsed(ctx context.Context, credentialID string, at time.Time) error
}
