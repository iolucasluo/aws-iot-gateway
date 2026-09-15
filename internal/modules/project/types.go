package project

import (
	"errors"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/portalaccount"
)

// ProjectType 决定项目是否需要绑定代工厂。
type ProjectType string

const (
	TypeCloud ProjectType = "cloud"
	TypeOEM   ProjectType = "oem"
)

// 项目与凭证状态。
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
)

var (
	ErrInvalidInput         = errors.New("invalid project input")
	ErrProjectNotFound      = errors.New("project not found")
	ErrCredentialNotFound   = errors.New("credential not found")
	ErrProductNotFound      = errors.New("product not found")
	ErrProductNotInProject  = errors.New("product is not part of the project")
	ErrManufacturerNotFound = errors.New("manufacturer not found")
	ErrForbidden            = errors.New("project operation forbidden")
	ErrCredentialDisabled   = errors.New("credential is disabled or expired")
	ErrDuplicateProduct     = errors.New("product is already attached to the project")
	ErrOwnerNotFound        = errors.New("product owner account not found")
)

// Project 是凭证与设备归属的锚点；它不是 tenant。
type Project struct {
	ID             string
	OwnerAccountID string
	Type           ProjectType
	Name           string
	Description    string
	ManufacturerID string
	Status         string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Credential 是项目级 HMAC 凭证的持久化投影。
// SecretCiphertext 是 AES-256-GCM 密文，明文只在创建/轮换响应中出现一次。
type Credential struct {
	ID               string
	ProjectID        string
	Name             string
	APIKey           string
	SecretCiphertext []byte
	KEKID            string
	Permissions      []string
	Status           string
	ExpiresAt        *time.Time
	LastUsedAt       *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Usable 判断凭证在给定时刻是否可以用于验签。
func (credential Credential) Usable(now time.Time) bool {
	if credential.Status != StatusActive {
		return false
	}
	return credential.ExpiresAt == nil || now.Before(*credential.ExpiresAt)
}

// ProjectFilter 是项目列表的显式过滤条件；scope 字段由调用方按操作者派生。
type ProjectFilter struct {
	OwnerAccountID string
	ManufacturerID string
	Type           ProjectType
	Status         string
	Limit          int
}

// Scope 是项目的作用域投影：它既约束门户查询，也决定 DB 凭证主体
// 在 /api/v1/openapi/device-products 能看到哪些产品。
type Scope struct {
	ProjectID      string
	OwnerAccountID string
	ManufacturerID string
	AllowedPIDs    []string
}

// IssueResult 只在创建/轮换响应中出现一次明文 secret。
type IssueResult struct {
	Credential Credential
	APISecret  string
}

// Actor 复用门户账号的身份投影，避免第二套角色模型。
type Actor = portalaccount.Actor

// Product 是激活产品目录的完整投影（含平台面归属）。
// activation.Product 是它对 App 的只读投影；这里额外承载 owner/status/category。
type Product struct {
	ID             string
	PID            string
	DPProfileCode  string
	Name           string
	Status         string
	Capabilities   []string
	OwnerAccountID string
	Category       string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// ProductFilter 是产品列表的显式过滤条件。
type ProductFilter struct {
	OwnerAccountID string
	Status         string
	PID            string
	Limit          int
}

// CreateProductInput 是创建产品的请求。OwnerAccountID 为空表示公共目录产品（仅 staff 可建）。
type CreateProductInput struct {
	PID            string
	Name           string
	DPProfileCode  string
	Capabilities   []string
	Category       string
	OwnerAccountID string
	Status         string
}

// ProductPatch 是产品更新请求；nil 字段表示不改动。
type ProductPatch struct {
	Name           *string
	Status         *string
	Capabilities   *[]string
	Category       *string
	OwnerAccountID *string
}
