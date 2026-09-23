// Package operations 提供平台面只读投影，包括 staff 运维视图与项目 Webhook 投递历史。
//
// 它不拥有任何设备域状态：所有投影都直接读 device_registry / device_bindings /
// device_activations / audit_logs，不复制、不缓存、不写回。
package operations

import (
	"encoding/base64"
	"errors"
	"strings"
	"time"
)

var ErrInvalidQuery = errors.New("invalid operations query")

// Cursor 是高基数列表的键集游标：按 (created_at, id) 倒序翻页。
type Cursor struct {
	CreatedAt time.Time
	ID        string
}

const cursorSeparator = "|"

// Encode 返回不透明游标串。
func (cursor Cursor) Encode() string {
	value := cursor.CreatedAt.UTC().Format(time.RFC3339Nano) + cursorSeparator + cursor.ID
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

// DecodeCursor 解析游标；空串表示从头开始，格式错误返回 ok=false。
func DecodeCursor(value string) (Cursor, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return Cursor{}, true
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return Cursor{}, false
	}
	created, id, found := strings.Cut(string(decoded), cursorSeparator)
	if !found || id == "" {
		return Cursor{}, false
	}
	moment, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Cursor{}, false
	}
	return Cursor{CreatedAt: moment.UTC(), ID: id}, true
}

// Page 是列表查询的公共分页参数。
type Page struct {
	Limit  int
	Before *Cursor
}

// normalizeLimit 缺省 50，上限 200：平台面列表不允许无限拉取。
func normalizeLimit(limit int) int {
	switch {
	case limit <= 0:
		return 50
	case limit > 200:
		return 200
	default:
		return limit
	}
}

// DeviceFilter 是设备列表的过滤条件。
// SubjectID 按当前 active binding 的主体过滤（平台面即项目 id）。
type DeviceFilter struct {
	PID       string
	Status    string
	Online    *bool
	Query     string
	SubjectID string
	Page      Page
}

// Device 是设备运行身份的只读投影。
type Device struct {
	// ID 只用于键集游标，不对外暴露；设备对外的稳定标识是 device_sn。
	ID                    string
	DeviceSN              string
	PID                   string
	Status                string
	Online                bool
	LastSeenAt            *time.Time
	CertificateID         string
	CreatedAt             time.Time
	BindingProjectID      string
	BindingExternalUserID string
	BindingActivatedAt    *time.Time
}

// BindingFilter 是绑定列表的过滤条件。
type BindingFilter struct {
	DeviceSN  string
	ProjectID string
	Status    string
	Page      Page
}

// Binding 是设备归属的只读投影。它对应 device_bindings 一行。
type Binding struct {
	ID                    string
	DeviceSN              string
	PID                   string
	SubjectID             string
	BindingExternalUserID string
	Status                string
	ActivatedAt           time.Time
	ReleasedAt            *time.Time
	ReleaseReason         string
	CreatedAt             time.Time
}

// ActivationFilter 是激活记录列表的过滤条件。
type ActivationFilter struct {
	PID    string
	Status string
	Page   Page
}

// Activation 是 activation saga 的只读投影。
type Activation struct {
	ID                    string
	PID                   string
	DeviceSN              string
	SubjectID             string
	BindingExternalUserID string
	Method                string
	Status                string
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// AuditFilter 是审计列表的过滤条件。
type AuditFilter struct {
	ActorKind      string
	ActorAccountID string
	Operation      string
	Outcome        string
	From           *time.Time
	To             *time.Time
	Page           Page
}

// AuditEntry 是审计事实的只读投影。
type AuditEntry struct {
	ID             string
	ActorKind      string
	ActorAccountID string
	ActorLabel     string
	InternalAppID  string
	Operation      string
	ResourceType   string
	ResourceID     string
	RequestID      string
	Outcome        string
	ErrorCode      string
	Metadata       map[string]any
	CreatedAt      time.Time
}

// WebhookDeliveryFilter 是项目 Webhook 投递记录的查询条件。
// Status 为空表示全部；可选值为 delivered / undelivered。
type WebhookDeliveryFilter struct {
	ProjectID string
	Status    string
	Page      Page
}

// WebhookDelivery 是一条 Webhook 事件的最终投递投影。
// DeliveryStatus 将 outbox 的内部状态收敛为门户可理解的 delivered / undelivered。
type WebhookDelivery struct {
	ID             string
	DeliveryID     string
	EventType      string
	DeliveryStatus string
	OutboxStatus   string
	Attempts       int
	StatusCode     *int
	ErrorCode      string
	CreatedAt      time.Time
	LastAttemptAt  *time.Time
}

// DeviceTokenFilter 是设备令牌列表的过滤条件。
type DeviceTokenFilter struct {
	DeviceSN string
	Page     Page
}

// DeviceToken 是 binding-scoped token 的只读投影。
// 它刻意不包含 token hash：那是凭据材料，门户只需要元数据。
type DeviceToken struct {
	ID        string
	BindingID string
	DeviceSN  string
	SubjectID string
	ExpiresAt time.Time
	RevokedAt *time.Time
	CreatedAt time.Time
}

// CertificateFilter 是设备证书列表的过滤条件。
type CertificateFilter struct {
	DeviceSN string
	Status   string
	Page     Page
}

// Certificate 是设备运行证书的只读投影（来自 device_registry 的身份锚列）。
type Certificate struct {
	DeviceSN       string
	PID            string
	Status         string
	CertificateID  string
	CertificateARN string
	Endpoint       string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// FirmwareVersionFilter 是 OTA 固件版本列表的过滤条件。
type FirmwareVersionFilter struct {
	PID    string
	Status string
	Page   Page
}

// FirmwareVersion 是 OTA 固件版本的只读投影。
//
// 它刻意不包含 artifact_key / source_url：那是下载凭据材料，
// 门户只需要元数据；需要下载时由 gateway 的 presigned 流程单独签发。
type FirmwareVersion struct {
	ID               string
	PID              string
	PublishedBy      string
	Version          string
	FirmwareBuild    int64
	SHA256           string
	FileSize         int64
	ReleaseStatement string
	Status           string
	AvailableAt      *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// OTADeploymentFilter 是单设备 OTA 下发记录列表的过滤条件。
type OTADeploymentFilter struct {
	DeviceSN  string
	PID       string
	ProjectID string
	State     string
	Page      Page
}

// OTADeployment 是 OTA 下发记录的只读投影。它对应 device_ota_deployments 一行。
type OTADeployment struct {
	ID            string
	DeviceSN      string
	PID           string
	ProjectID     string
	TargetVersion string
	TargetBuild   int64
	State         string
	FailureReason string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	CompletedAt   *time.Time
}
