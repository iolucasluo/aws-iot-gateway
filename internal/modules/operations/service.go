package operations

import (
	"context"
	"fmt"
	"strings"
)

// Repository 是只读投影的查询边界。实现只允许 SELECT。
type Repository interface {
	ListDevices(ctx context.Context, filter DeviceFilter) ([]Device, error)
	FindDevice(ctx context.Context, deviceSN string) (Device, error)
	ListBindings(ctx context.Context, filter BindingFilter) ([]Binding, error)
	ListActivations(ctx context.Context, filter ActivationFilter) ([]Activation, error)
	ListAuditLogs(ctx context.Context, filter AuditFilter) ([]AuditEntry, error)
	ListDeviceTokens(ctx context.Context, filter DeviceTokenFilter) ([]DeviceToken, error)
	ListCertificates(ctx context.Context, filter CertificateFilter) ([]Certificate, error)
}

// QueryService 归一化查询参数并委托仓库。它不写任何状态。
type QueryService struct{ repository Repository }

func NewQueryService(repository Repository) (*QueryService, error) {
	if repository == nil {
		return nil, fmt.Errorf("operations repository is required")
	}
	return &QueryService{repository: repository}, nil
}

func (service *QueryService) ListDevices(ctx context.Context, filter DeviceFilter) ([]Device, error) {
	filter.PID = strings.TrimSpace(filter.PID)
	filter.Status = strings.TrimSpace(filter.Status)
	filter.Query = strings.TrimSpace(filter.Query)
	filter.SubjectID = strings.TrimSpace(filter.SubjectID)
	filter.Page.Limit = normalizeLimit(filter.Page.Limit)
	return service.repository.ListDevices(ctx, filter)
}

func (service *QueryService) FindDevice(ctx context.Context, deviceSN string) (Device, error) {
	deviceSN = strings.TrimSpace(deviceSN)
	if deviceSN == "" {
		return Device{}, fmt.Errorf("%w: device sn is required", ErrInvalidQuery)
	}
	return service.repository.FindDevice(ctx, deviceSN)
}

func (service *QueryService) ListBindings(ctx context.Context, filter BindingFilter) ([]Binding, error) {
	filter.DeviceSN = strings.TrimSpace(filter.DeviceSN)
	filter.ProjectID = strings.TrimSpace(filter.ProjectID)
	filter.Status = strings.TrimSpace(filter.Status)
	filter.Page.Limit = normalizeLimit(filter.Page.Limit)
	return service.repository.ListBindings(ctx, filter)
}

func (service *QueryService) ListActivations(ctx context.Context, filter ActivationFilter) ([]Activation, error) {
	filter.PID = strings.TrimSpace(filter.PID)
	filter.Status = strings.TrimSpace(filter.Status)
	filter.Page.Limit = normalizeLimit(filter.Page.Limit)
	return service.repository.ListActivations(ctx, filter)
}

func (service *QueryService) ListDeviceTokens(ctx context.Context, filter DeviceTokenFilter) ([]DeviceToken, error) {
	filter.DeviceSN = strings.TrimSpace(filter.DeviceSN)
	filter.Page.Limit = normalizeLimit(filter.Page.Limit)
	return service.repository.ListDeviceTokens(ctx, filter)
}

func (service *QueryService) ListCertificates(ctx context.Context, filter CertificateFilter) ([]Certificate, error) {
	filter.DeviceSN = strings.TrimSpace(filter.DeviceSN)
	filter.Status = strings.TrimSpace(filter.Status)
	filter.Page.Limit = normalizeLimit(filter.Page.Limit)
	return service.repository.ListCertificates(ctx, filter)
}

func (service *QueryService) ListAuditLogs(ctx context.Context, filter AuditFilter) ([]AuditEntry, error) {
	filter.ActorKind = strings.TrimSpace(filter.ActorKind)
	filter.ActorAccountID = strings.TrimSpace(filter.ActorAccountID)
	filter.Operation = strings.TrimSpace(filter.Operation)
	filter.Outcome = strings.TrimSpace(filter.Outcome)
	filter.Page.Limit = normalizeLimit(filter.Page.Limit)
	return service.repository.ListAuditLogs(ctx, filter)
}
