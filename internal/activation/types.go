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
	ErrProductUnavailable        = errors.New("product is unavailable for activation")
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
}

type PrepareInput struct {
	ActivationID   string
	Code           string
	CSRPEM         string
	ExpectedMethod Method
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
	InternalAppID         string
	BindingExternalUserID string
	IdempotencyKey        string
	RequestHash           [sha256.Size]byte
	DeviceSN              string
	Status                Status
	ExpiresAt             time.Time
}

type PreparedIdentity struct {
	DeviceID       string
	CertificateID  string
	CertificatePEM string
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
	PID  string `json:"pid"`
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
	Create(context.Context, Record) error
	FindByID(context.Context, string) (Record, error)
	FindByIdempotency(context.Context, string, string) (Record, error)
	ReservePreparation(context.Context, string, string, string, string) (Record, error)
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
