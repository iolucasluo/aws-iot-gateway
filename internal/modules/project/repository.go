package project

import (
	"context"
	"time"
)

// Repository 是项目域的持久化边界。
// 唯一性冲突必须转成稳定错误（如 ErrDuplicateProduct），不让调用方解析数据库错误。
type Repository interface {
	CreateProject(ctx context.Context, project Project) error
	FindProjectByID(ctx context.Context, projectID string) (Project, error)
	ListProjects(ctx context.Context, filter ProjectFilter) ([]Project, error)
	UpdateProject(ctx context.Context, project Project) error

	AddProjectProduct(ctx context.Context, projectID, productID string, at time.Time) error
	RemoveProjectProduct(ctx context.Context, projectID, productID string) error
	ListProjectProductIDs(ctx context.Context, projectID string) ([]string, error)
	ListProjectPIDs(ctx context.Context, projectID string) ([]string, error)

	ManufacturerExists(ctx context.Context, manufacturerID string) (bool, error)

	CreateCredential(ctx context.Context, credential Credential) error
	FindCredentialByID(ctx context.Context, credentialID string) (Credential, error)
	FindCredentialByAPIKey(ctx context.Context, apiKey string) (Credential, error)
	ListCredentials(ctx context.Context, projectID string) ([]Credential, error)
	UpdateCredential(ctx context.Context, credential Credential) error
	TouchCredentialUsed(ctx context.Context, credentialID string, at time.Time) error
}
