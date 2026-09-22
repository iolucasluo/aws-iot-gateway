package project

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/portalaccount"
)

// 与 activation 目录保持同一 pid / capability 形状：门户写入的目录必须能被 App 直接消费。
var (
	productPIDPattern        = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)
	productCapabilityPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,63}$`)
	productCategoryPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	uuidPattern              = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

const maxProductCapabilities = 32

// canManageProduct 决定操作者能否改动产品：staff 全部，developer 只能改自己的。
func canManageProduct(actor Actor, product Product) bool {
	if isStaff(actor) {
		return true
	}
	return isDeveloper(actor) && product.OwnerAccountID != "" && product.OwnerAccountID == actor.AccountID
}

// CreateProduct 建立产品目录项。DP profile 必须已经存在且 active：
// DP 定义仍由 reviewed contract seed 发布，门户只做产品归属与准入。
func (service *Service) CreateProduct(ctx context.Context, actor Actor, input CreateProductInput) (Product, error) {
	input.PID = strings.TrimSpace(input.PID)
	input.Name = strings.TrimSpace(input.Name)
	input.DPProfileCode = strings.TrimSpace(input.DPProfileCode)
	input.Category = strings.TrimSpace(input.Category)
	input.OwnerAccountID = strings.TrimSpace(input.OwnerAccountID)
	if !productPIDPattern.MatchString(input.PID) {
		return Product{}, fmt.Errorf("%w: pid must match %s", ErrInvalidInput, productPIDPattern.String())
	}
	if input.Name == "" {
		return Product{}, fmt.Errorf("%w: product name is required", ErrInvalidInput)
	}
	if input.DPProfileCode == "" {
		return Product{}, fmt.Errorf("%w: dp_profile_code is required", ErrInvalidInput)
	}
	if input.Category != "" && !productCategoryPattern.MatchString(input.Category) {
		return Product{}, fmt.Errorf("%w: invalid product category", ErrInvalidInput)
	}
	capabilities, err := normalizeCapabilities(input.Capabilities)
	if err != nil {
		return Product{}, err
	}
	status := strings.TrimSpace(input.Status)
	if status == "" {
		status = StatusActive
	}
	if status != StatusActive && status != StatusDisabled {
		return Product{}, fmt.Errorf("%w: unknown product status %q", ErrInvalidInput, status)
	}
	switch {
	case isStaff(actor):
		// staff 可以建公共目录产品（owner 为空）或指定归属。
	case isDeveloper(actor):
		// 开发者只能建自己的产品，且不能替别人建。
		if input.OwnerAccountID == "" {
			input.OwnerAccountID = actor.AccountID
		}
		if input.OwnerAccountID != actor.AccountID {
			return Product{}, ErrForbidden
		}
	default:
		return Product{}, ErrForbidden
	}
	if input.OwnerAccountID != "" {
		if err := service.EnsureOwnerIsDeveloper(ctx, input.OwnerAccountID); err != nil {
			return Product{}, err
		}
	}
	active, err := service.repository.ActiveDPProfileExists(ctx, input.DPProfileCode)
	if err != nil {
		return Product{}, err
	}
	if !active {
		return Product{}, fmt.Errorf("%w: dp profile %q is not active", ErrInvalidInput, input.DPProfileCode)
	}
	productID, err := randomUUID()
	if err != nil {
		return Product{}, fmt.Errorf("generate product id: %w", err)
	}
	now := service.now().UTC()
	product := Product{
		ID: productID, PID: input.PID, DPProfileCode: input.DPProfileCode, Name: input.Name,
		Status: status, Capabilities: capabilities, OwnerAccountID: input.OwnerAccountID,
		Category: input.Category, CreatedAt: now, UpdatedAt: now,
	}
	if err := service.repository.CreateProduct(ctx, product); err != nil {
		return Product{}, err
	}
	return product, nil
}

// ListProducts 按操作者 scope 返回产品：staff 透传过滤条件，developer 看自己
// 的产品与平台公共目录产品（公共产品用于项目挂载，不开放改动）。
func (service *Service) ListProducts(ctx context.Context, actor Actor, filter ProductFilter) ([]Product, error) {
	switch {
	case isStaff(actor):
	case isDeveloper(actor):
		filter.OwnerAccountID = actor.AccountID
		filter.IncludeGlobal = true
	default:
		return nil, ErrForbidden
	}
	return service.repository.ListProducts(ctx, filter)
}

// GetProduct 返回 scope 内的产品；越权与不存在都返回 ErrProductNotFound。
func (service *Service) GetProduct(ctx context.Context, actor Actor, pid string) (Product, error) {
	product, err := service.repository.FindProductByPID(ctx, strings.TrimSpace(pid))
	if err != nil {
		return Product{}, err
	}
	if !canManageProduct(actor, product) {
		return Product{}, ErrProductNotFound
	}
	return product, nil
}

// UpdateProduct 更新名称、启停、能力、分类与归属。
// 归属只在 staff 显式传入时改变，避免开发者把自己的产品转给别人。
func (service *Service) UpdateProduct(ctx context.Context, actor Actor, pid string, patch ProductPatch) (Product, error) {
	product, err := service.repository.FindProductByPID(ctx, strings.TrimSpace(pid))
	if err != nil {
		return Product{}, err
	}
	if !canManageProduct(actor, product) {
		return Product{}, ErrForbidden
	}
	if patch.Name != nil {
		name := strings.TrimSpace(*patch.Name)
		if name == "" {
			return Product{}, fmt.Errorf("%w: product name must not be blank", ErrInvalidInput)
		}
		product.Name = name
	}
	if patch.Status != nil {
		status := strings.TrimSpace(*patch.Status)
		if status != StatusActive && status != StatusDisabled {
			return Product{}, fmt.Errorf("%w: unknown product status %q", ErrInvalidInput, status)
		}
		product.Status = status
	}
	if patch.Capabilities != nil {
		capabilities, err := normalizeCapabilities(*patch.Capabilities)
		if err != nil {
			return Product{}, err
		}
		product.Capabilities = capabilities
	}
	if patch.Category != nil {
		category := strings.TrimSpace(*patch.Category)
		if category != "" && !productCategoryPattern.MatchString(category) {
			return Product{}, fmt.Errorf("%w: invalid product category", ErrInvalidInput)
		}
		product.Category = category
	}
	if patch.OwnerAccountID != nil {
		if !isStaff(actor) {
			return Product{}, fmt.Errorf("%w: only staff can change product ownership", ErrForbidden)
		}
		owner := strings.TrimSpace(*patch.OwnerAccountID)
		if owner != "" {
			if err := service.EnsureOwnerIsDeveloper(ctx, owner); err != nil {
				return Product{}, err
			}
		}
		product.OwnerAccountID = owner
	}
	product.UpdatedAt = service.now().UTC()
	if err := service.repository.UpdateProduct(ctx, product); err != nil {
		return Product{}, err
	}
	return product, nil
}

// ListActiveDPProfiles 暴露 active 的 DP profile 供门户选择；DP 定义本身不可通过门户修改。
func (service *Service) ListActiveDPProfiles(ctx context.Context) ([]DPProfile, error) {
	return service.repository.ListActiveDPProfiles(ctx)
}

// ListDPDefinitionsForProfile 返回指定 profile 下的公共 DP + 当前开发者的自定义 DP。
func (service *Service) ListDPDefinitionsForProfile(ctx context.Context, actor Actor, profileCode string) ([]DPDefinition, error) {
	if !isDeveloper(actor) && !isStaff(actor) {
		return nil, ErrForbidden
	}
	return service.repository.ListDPDefinitionsForProfile(ctx, strings.TrimSpace(profileCode), actor.AccountID)
}

// CreateDPDefinition 创建开发者自定义 DP 并挂载到指定 profile。
func (service *Service) CreateDPDefinition(ctx context.Context, actor Actor, input CreateDPDefinitionInput) (DPDefinition, error) {
	if !isDeveloper(actor) {
		return DPDefinition{}, ErrForbidden
	}
	input.Code = strings.TrimSpace(input.Code)
	input.Type = strings.TrimSpace(input.Type)
	input.Direction = strings.TrimSpace(input.Direction)
	if input.Code == "" || input.Type == "" || input.Direction == "" {
		return DPDefinition{}, fmt.Errorf("%w: code, type and direction are required", ErrInvalidInput)
	}
	if len(input.DPProfileCodes) == 0 {
		return DPDefinition{}, fmt.Errorf("%w: at least one dp_profile_code is required", ErrInvalidInput)
	}
	for _, code := range input.DPProfileCodes {
		exists, err := service.repository.ActiveDPProfileExists(ctx, strings.TrimSpace(code))
		if err != nil {
			return DPDefinition{}, err
		}
		if !exists {
			return DPDefinition{}, fmt.Errorf("%w: dp profile %q not found or inactive", ErrInvalidInput, code)
		}
	}
	id, err := randomUUID()
	if err != nil {
		return DPDefinition{}, fmt.Errorf("generate dp definition id: %w", err)
	}
	now := service.now().UTC()
	def := DPDefinition{
		ID: id, Code: input.Code, Type: input.Type, Direction: input.Direction,
		Spec: input.Spec, OwnerAccountID: actor.AccountID,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := service.repository.CreateDPDefinition(ctx, def, input.DPProfileCodes); err != nil {
		return DPDefinition{}, err
	}
	return def, nil
}

// DeleteDPDefinition 删除开发者自己的自定义 DP（公共 DP 不可删）。
func (service *Service) DeleteDPDefinition(ctx context.Context, actor Actor, id string) error {
	if !isDeveloper(actor) {
		return ErrForbidden
	}
	id = strings.TrimSpace(id)
	if !uuidPattern.MatchString(id) {
		return ErrDPDefinitionNotFound
	}
	return service.repository.DeleteDPDefinition(ctx, id, actor.AccountID)
}

// DPProfile 是 DP 合同的只读投影。
type DPProfile struct {
	Code string
	Name string
}

// DPDefinition 是 DP 定义的门户投影（含 owner 归属）。
type DPDefinition struct {
	ID             string
	Code           string
	Type           string
	Direction      string
	Spec           json.RawMessage // JSON，可为空
	OwnerAccountID string          // 空 = 公共
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// CreateDPDefinitionInput 是开发者创建自定义 DP 的请求。
type CreateDPDefinitionInput struct {
	Code           string
	Type           string
	Direction      string
	Spec           json.RawMessage
	DPProfileCodes []string // 挂载到哪些 profile
}

// EnsureOwnerIsDeveloper 在把产品归属给账号前确认它是 developer，
// 避免把目录产品错误挂到 staff 或厂商账号上。
func (service *Service) EnsureOwnerIsDeveloper(ctx context.Context, accountID string) error {
	account, err := service.repository.FindPortalAccount(ctx, accountID)
	if err != nil {
		return err
	}
	if account.Kind != portalaccount.KindDeveloper || account.Status != portalaccount.StatusActive {
		return fmt.Errorf("%w: product owner must be an active developer account", ErrInvalidInput)
	}
	return nil
}

func normalizeCapabilities(capabilities []string) ([]string, error) {
	if len(capabilities) > maxProductCapabilities {
		return nil, fmt.Errorf("%w: too many product capabilities", ErrInvalidInput)
	}
	normalized := make([]string, 0, len(capabilities))
	seen := make(map[string]struct{}, len(capabilities))
	for _, capability := range capabilities {
		capability = strings.TrimSpace(capability)
		if !productCapabilityPattern.MatchString(capability) {
			return nil, fmt.Errorf("%w: invalid product capability %q", ErrInvalidInput, capability)
		}
		if _, duplicate := seen[capability]; duplicate {
			continue
		}
		seen[capability] = struct{}{}
		normalized = append(normalized, capability)
	}
	return normalized, nil
}
