package project

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/portalaccount"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/credentialcipher"
)

// CreateProjectInput 是创建项目的请求。OwnerAccountID 为空时归当前开发者所有。
type CreateProjectInput struct {
	OwnerAccountID string
	Type           ProjectType
	Name           string
	Description    string
	ManufacturerID string
}

// ProjectPatch 是项目更新请求；nil 字段表示不改动。
type ProjectPatch struct {
	Name        *string
	Description *string
	Status      *string
}

// Service 承载项目、项目产品白名单与项目凭证。
// project 是作用域维度而非 tenant：同一设备仍然只有一个全局 active binding。
type Service struct {
	repository Repository
	cipher     *credentialcipher.Cipher
	now        func() time.Time
}

func NewService(repository Repository, cipher *credentialcipher.Cipher, now func() time.Time) (*Service, error) {
	if repository == nil {
		return nil, fmt.Errorf("project repository is required")
	}
	if cipher == nil {
		return nil, fmt.Errorf("credential cipher is required")
	}
	if now == nil {
		now = time.Now
	}
	return &Service{repository: repository, cipher: cipher, now: now}, nil
}

func isStaff(actor Actor) bool {
	return actor.Kind == portalaccount.KindStaff && actor.Role == portalaccount.RoleSuperAdmin
}

func isDeveloper(actor Actor) bool {
	return actor.Kind == portalaccount.KindDeveloper
}

func isManufacturer(actor Actor) bool {
	return actor.Kind == portalaccount.KindManufacturer
}

// canReadProject 决定操作者能否看到项目（staff 全部、开发者自己的、厂商名下的）。
func canReadProject(actor Actor, target Project) bool {
	switch {
	case isStaff(actor):
		return true
	case isDeveloper(actor):
		return target.OwnerAccountID == actor.AccountID
	case isManufacturer(actor):
		return target.ManufacturerID != "" && target.ManufacturerID == actor.ManufacturerID
	default:
		return false
	}
}

// canWriteProject 决定操作者能否改动项目：厂商对项目只有只读权限。
func canWriteProject(actor Actor, target Project) bool {
	if isStaff(actor) {
		return true
	}
	return isDeveloper(actor) && target.OwnerAccountID == actor.AccountID
}

// CreateProject 建立项目。开发者只能建自己的项目，staff 可以代任意账号建。
func (service *Service) CreateProject(ctx context.Context, actor Actor, input CreateProjectInput) (Project, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	input.ManufacturerID = strings.TrimSpace(input.ManufacturerID)
	input.OwnerAccountID = strings.TrimSpace(input.OwnerAccountID)
	if input.Name == "" || (input.Type != TypeCloud && input.Type != TypeOEM) {
		return Project{}, fmt.Errorf("%w: name and project type are required", ErrInvalidInput)
	}
	if input.OwnerAccountID == "" {
		input.OwnerAccountID = actor.AccountID
	}
	if !isStaff(actor) {
		if !isDeveloper(actor) || input.OwnerAccountID != actor.AccountID {
			return Project{}, ErrForbidden
		}
	}
	switch input.Type {
	case TypeOEM:
		if input.ManufacturerID == "" {
			return Project{}, fmt.Errorf("%w: an oem project requires a manufacturer", ErrInvalidInput)
		}
		exists, err := service.repository.ManufacturerExists(ctx, input.ManufacturerID)
		if err != nil {
			return Project{}, err
		}
		if !exists {
			return Project{}, ErrManufacturerNotFound
		}
	case TypeCloud:
		if input.ManufacturerID != "" {
			return Project{}, fmt.Errorf("%w: a cloud project must not reference a manufacturer", ErrInvalidInput)
		}
	}
	projectID, err := randomUUID()
	if err != nil {
		return Project{}, fmt.Errorf("generate project id: %w", err)
	}
	now := service.now().UTC()
	project := Project{
		ID: projectID, OwnerAccountID: input.OwnerAccountID, Type: input.Type, Name: input.Name,
		Description: input.Description, ManufacturerID: input.ManufacturerID, Status: StatusActive,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := service.repository.CreateProject(ctx, project); err != nil {
		return Project{}, err
	}
	return project, nil
}

// ListProjects 按操作者 scope 返回项目，调用方传入的 scope 字段会被覆盖。
func (service *Service) ListProjects(ctx context.Context, actor Actor, filter ProjectFilter) ([]Project, error) {
	switch {
	case isStaff(actor):
	case isDeveloper(actor):
		filter.OwnerAccountID = actor.AccountID
	case isManufacturer(actor):
		if actor.ManufacturerID == "" {
			return nil, ErrForbidden
		}
		filter.ManufacturerID = actor.ManufacturerID
	default:
		return nil, ErrForbidden
	}
	return service.repository.ListProjects(ctx, filter)
}

// GetProject 返回 scope 内的项目；越权与不存在都返回 ErrProjectNotFound。
func (service *Service) GetProject(ctx context.Context, actor Actor, projectID string) (Project, error) {
	target, err := service.repository.FindProjectByID(ctx, projectID)
	if err != nil {
		return Project{}, err
	}
	if !canReadProject(actor, target) {
		return Project{}, ErrProjectNotFound
	}
	return target, nil
}

// UpdateProject 更新名称、描述或启停状态；停用项目会同时让它的凭证失效。
func (service *Service) UpdateProject(ctx context.Context, actor Actor, projectID string, patch ProjectPatch) (Project, error) {
	target, err := service.repository.FindProjectByID(ctx, projectID)
	if err != nil {
		return Project{}, err
	}
	if !canWriteProject(actor, target) {
		return Project{}, ErrForbidden
	}
	if patch.Name != nil {
		name := strings.TrimSpace(*patch.Name)
		if name == "" {
			return Project{}, fmt.Errorf("%w: name must not be blank", ErrInvalidInput)
		}
		target.Name = name
	}
	if patch.Description != nil {
		target.Description = strings.TrimSpace(*patch.Description)
	}
	if patch.Status != nil {
		status := strings.TrimSpace(*patch.Status)
		if status != StatusActive && status != StatusDisabled {
			return Project{}, fmt.Errorf("%w: unknown project status %q", ErrInvalidInput, status)
		}
		target.Status = status
	}
	target.UpdatedAt = service.now().UTC()
	if err := service.repository.UpdateProject(ctx, target); err != nil {
		return Project{}, err
	}
	return target, nil
}

// AttachProduct 把产品加入项目白名单。
func (service *Service) AttachProduct(ctx context.Context, actor Actor, projectID, productID string) (Project, error) {
	target, err := service.repository.FindProjectByID(ctx, projectID)
	if err != nil {
		return Project{}, err
	}
	if !canWriteProject(actor, target) {
		return Project{}, ErrForbidden
	}
	productID = strings.TrimSpace(productID)
	if productID == "" {
		return Project{}, fmt.Errorf("%w: product id is required", ErrInvalidInput)
	}
	if err := service.repository.AddProjectProduct(ctx, projectID, productID, service.now().UTC()); err != nil {
		return Project{}, err
	}
	return target, nil
}

// DetachProduct 把产品移出项目白名单。
func (service *Service) DetachProduct(ctx context.Context, actor Actor, projectID, productID string) (Project, error) {
	target, err := service.repository.FindProjectByID(ctx, projectID)
	if err != nil {
		return Project{}, err
	}
	if !canWriteProject(actor, target) {
		return Project{}, ErrForbidden
	}
	if err := service.repository.RemoveProjectProduct(ctx, projectID, strings.TrimSpace(productID)); err != nil {
		return Project{}, err
	}
	return target, nil
}

// ListProjectProducts 返回白名单内的产品 id 与 PID。
func (service *Service) ListProjectProducts(ctx context.Context, actor Actor, projectID string) ([]string, []string, error) {
	if _, err := service.GetProject(ctx, actor, projectID); err != nil {
		return nil, nil, err
	}
	productIDs, err := service.repository.ListProjectProductIDs(ctx, projectID)
	if err != nil {
		return nil, nil, err
	}
	pids, err := service.repository.ListProjectPIDs(ctx, projectID)
	if err != nil {
		return nil, nil, err
	}
	return productIDs, pids, nil
}

// ScopeFor 返回项目作用域投影，供设备列表与产品可见性过滤使用。
func (service *Service) ScopeFor(ctx context.Context, actor Actor, projectID string) (Scope, error) {
	target, err := service.GetProject(ctx, actor, projectID)
	if err != nil {
		return Scope{}, err
	}
	pids, err := service.repository.ListProjectPIDs(ctx, projectID)
	if err != nil {
		return Scope{}, err
	}
	return Scope{
		ProjectID: target.ID, OwnerAccountID: target.OwnerAccountID,
		ManufacturerID: target.ManufacturerID, AllowedPIDs: pids,
	}, nil
}

// ScopeForPrincipal 供 DB 凭证主体使用：主体 id 就是 project id，因此不需要账号 actor。
func (service *Service) ScopeForPrincipal(ctx context.Context, projectID string) (Scope, error) {
	target, err := service.repository.FindProjectByID(ctx, projectID)
	if err != nil {
		return Scope{}, err
	}
	if target.Status != StatusActive {
		return Scope{}, ErrProjectNotFound
	}
	pids, err := service.repository.ListProjectPIDs(ctx, projectID)
	if err != nil {
		return Scope{}, err
	}
	return Scope{
		ProjectID: target.ID, OwnerAccountID: target.OwnerAccountID,
		ManufacturerID: target.ManufacturerID, AllowedPIDs: pids,
	}, nil
}
