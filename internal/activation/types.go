package activation

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"
)

var (
	ErrInvalidInput              = errors.New("invalid activation input")
	ErrActivationNotFound        = errors.New("activation not found")
	ErrActivationUnauthorized    = errors.New("activation code is invalid")
	ErrActivationExpired         = errors.New("activation has expired")
	ErrProvisionerNotConfigured  = errors.New("provisioner is not configured")
	ErrActivationAlreadyPrepared = errors.New("activation was prepared with another csr")
	ErrPreparationInProgress     = errors.New("activation preparation is in progress")
	ErrInstallationProofInvalid  = errors.New("installation proof is invalid")
	ErrIdempotencyConflict       = errors.New("idempotency key conflicts with another request")
	ErrActivationInProgress      = errors.New("activation already exists for idempotency key")
	ErrDeviceBindingConflict     = errors.New("device already has an active binding")
	ErrDPProfileUnavailable      = errors.New("DP profile is unavailable for activation")
	ErrProductUnavailable        = errors.New("product is unavailable for activation")
	ErrInvalidDeviceSN           = errors.New("device sn does not match the numbering standard")
	ErrInvalidChipUID            = errors.New("chip uid is not valid hex")
	ErrDeviceIdentityConflict    = errors.New("device identity conflicts with an existing registration")
)

type Method string

const (
	MethodQR  Method = "qr"
	MethodAP  Method = "ap"
	MethodBLE Method = "ble"
)

type Status string

const (
	StatusPending    Status = "pending"
	StatusActivating Status = "activating"
	StatusInstalling Status = "installing"
	StatusActivated  Status = "activated"
	StatusFailed     Status = "failed"
	StatusExpired    Status = "expired"
	StatusCancelled  Status = "cancelled"
)

type CreateInput struct {
	InternalAppID         string
	BindingExternalUserID string
	IdempotencyKey        string
	Method                Method
	PID                   string
	// DeviceSN 是产线发放的 device_sn，ChipUID 是 eFuse 芯片 UID 的 hex 形式。
	// 两者均可选；缺省时回落为设备上报或兜底生成。
	DeviceSN string
	ChipUID  string
}

type PrepareInput struct {
	ActivationID   string
	Code           string
	CSRPEM         string
	ExpectedMethod Method
	// DeviceSN / ChipUID 由设备上报，优先级低于创建 activation 时调用方提供的值。
	DeviceSN string
	ChipUID  string
}

type ConfirmInput struct {
	ActivationID string
	Code         string
	Proof        string
}

type Activation struct {
	ID                    string
	Code                  string
	Method                Method
	PID                   string
	DPProfile             string
	InternalAppID         string
	BindingExternalUserID string
	IdempotencyKey        string
	RequestHash           [sha256.Size]byte
	DeviceSN              string
	Status                Status
	ExpiresAt             time.Time
	// RequestedDeviceSN / RequestedChipUID 是创建 activation 时调用方提供的身份事实，
	// 在 prepare 阶段作为最高优先级来源参与归并。
	RequestedDeviceSN string
	RequestedChipUID  []byte
}

type PreparedIdentity struct {
	DeviceID       string
	CertificateID  string
	CertificatePEM string
	Endpoint       string
	// AWSIoTEndpoint is retained for legacy AWS callers. New providers must
	// populate Endpoint instead.
	AWSIoTEndpoint string
}

type Installation struct {
	ActivationID  string
	DeviceID      string
	PID           string
	CertificateID string
	Certificate   string
	Endpoint      string
	ProofNonce    string
}

type Device struct {
	ID                    string
	ActivationID          string
	PID                   string
	Endpoint              string
	BindingExternalUserID string
	Status                string
	Online                bool
	LastSeenAt            *time.Time
}

type Product struct {
	DPProfile    string   `json:"dp_profile"`
	PID          string   `json:"pid"`
	Name         string   `json:"name"`
	Capabilities []string `json:"capabilities"`
}

// DPProfile identifies an explicitly published, versioned command contract.
type DPProfile struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type Record struct {
	Activation       Activation
	CodeHash         [sha256.Size]byte
	CSRPEM           string
	Installation     Installation
	DeviceInternalID string
	FailureCode      string
}

type ProductCatalog interface {
	FindActiveProduct(context.Context, string) (Product, error)
	ListActiveProducts(context.Context) ([]Product, error)
}

type Repository interface {
	ProductCatalog
	FindActiveDPProfile(context.Context, string) (DPProfile, error)
	ListActiveDPProfiles(context.Context) ([]DPProfile, error)
	Create(context.Context, Record) error
	FindByID(context.Context, string) (Record, error)
	FindByIdempotency(context.Context, string, string) (Record, error)
	ReservePreparation(context.Context, Reservation) (Record, error)
	CompletePreparation(context.Context, string, Installation) (Record, error)
	FailPreparation(context.Context, string, string) error
	CompleteActivation(context.Context, string, string) (Record, error)
	ListDevices(context.Context, string, string) ([]Device, error)
}

type Provisioner interface {
	Provision(context.Context, string, string) (PreparedIdentity, error)
}

type Activator interface {
	Activate(context.Context, string) error
}

func (method Method) valid() bool {
	return method == MethodQR || method == MethodAP || method == MethodBLE
}
