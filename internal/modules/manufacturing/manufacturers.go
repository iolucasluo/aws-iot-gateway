package manufacturing

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var manufacturerCodePattern = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9._-]{1,31}$")

// Manufacturer 是代工厂主数据。它由制造面拥有（portalaccount 只做存在性读）。
type Manufacturer struct {
	ID           string
	Code         string
	Name         string
	ContactName  string
	ContactEmail string
	Status       string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ManufacturerFilter 是代工厂列表的过滤条件。
type ManufacturerFilter struct {
	Status string
	Limit  int
}

// CreateManufacturerInput 是创建代工厂的请求（仅 staff）。
type CreateManufacturerInput struct {
	Code         string
	Name         string
	ContactName  string
	ContactEmail string
}

// ManufacturerPatch 是代工厂更新请求；nil 字段表示不改动。
type ManufacturerPatch struct {
	Name         *string
	ContactName  *string
	ContactEmail *string
	Status       *string
}

// CreateManufacturer 只允许 staff 建立代工厂主数据。
func (service *Service) CreateManufacturer(ctx context.Context, actor Actor, input CreateManufacturerInput) (Manufacturer, error) {
	if !isStaff(actor) {
		return Manufacturer{}, ErrForbidden
	}
	input.Code = strings.TrimSpace(input.Code)
	input.Name = strings.TrimSpace(input.Name)
	input.ContactName = strings.TrimSpace(input.ContactName)
	input.ContactEmail = strings.ToLower(strings.TrimSpace(input.ContactEmail))
	if !manufacturerCodePattern.MatchString(input.Code) || input.Name == "" {
		return Manufacturer{}, fmt.Errorf("%w: manufacturer code and name are required", ErrInvalidInput)
	}
	if input.ContactEmail != "" && !strings.Contains(input.ContactEmail, "@") {
		return Manufacturer{}, fmt.Errorf("%w: contact email is invalid", ErrInvalidInput)
	}
	manufacturerID, err := randomUUID()
	if err != nil {
		return Manufacturer{}, fmt.Errorf("generate manufacturer id: %w", err)
	}
	now := service.now().UTC()
	manufacturer := Manufacturer{
		ID: manufacturerID, Code: input.Code, Name: input.Name, ContactName: input.ContactName,
		ContactEmail: input.ContactEmail, Status: StatusActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := service.repository.CreateManufacturer(ctx, manufacturer); err != nil {
		return Manufacturer{}, err
	}
	return manufacturer, nil
}

// ListManufacturers 对 staff 返回全部；厂商只看到自己那一条。
func (service *Service) ListManufacturers(ctx context.Context, actor Actor, filter ManufacturerFilter) ([]Manufacturer, error) {
	filter.Status = strings.TrimSpace(filter.Status)
	switch {
	case isStaff(actor):
		return service.repository.ListManufacturers(ctx, filter)
	case isManufacturer(actor):
		manufacturer, err := service.repository.FindManufacturerByID(ctx, actor.ManufacturerID)
		if err != nil {
			return nil, err
		}
		if filter.Status != "" && manufacturer.Status != filter.Status {
			return []Manufacturer{}, nil
		}
		return []Manufacturer{manufacturer}, nil
	default:
		return nil, ErrForbidden
	}
}

// UpdateManufacturer 只允许 staff 改动；跨主体一律拒绝。
func (service *Service) UpdateManufacturer(ctx context.Context, actor Actor, manufacturerID string, patch ManufacturerPatch) (Manufacturer, error) {
	if !isStaff(actor) {
		return Manufacturer{}, ErrForbidden
	}
	manufacturer, err := service.repository.FindManufacturerByID(ctx, manufacturerID)
	if err != nil {
		return Manufacturer{}, err
	}
	if patch.Name != nil {
		name := strings.TrimSpace(*patch.Name)
		if name == "" {
			return Manufacturer{}, fmt.Errorf("%w: manufacturer name must not be blank", ErrInvalidInput)
		}
		manufacturer.Name = name
	}
	if patch.ContactName != nil {
		manufacturer.ContactName = strings.TrimSpace(*patch.ContactName)
	}
	if patch.ContactEmail != nil {
		email := strings.ToLower(strings.TrimSpace(*patch.ContactEmail))
		if email != "" && !strings.Contains(email, "@") {
			return Manufacturer{}, fmt.Errorf("%w: contact email is invalid", ErrInvalidInput)
		}
		manufacturer.ContactEmail = email
	}
	if patch.Status != nil {
		status := strings.TrimSpace(*patch.Status)
		if status != StatusActive && status != StatusDisabled {
			return Manufacturer{}, fmt.Errorf("%w: unknown manufacturer status %q", ErrInvalidInput, status)
		}
		manufacturer.Status = status
	}
	manufacturer.UpdatedAt = service.now().UTC()
	if err := service.repository.UpdateManufacturer(ctx, manufacturer); err != nil {
		return Manufacturer{}, err
	}
	return manufacturer, nil
}
