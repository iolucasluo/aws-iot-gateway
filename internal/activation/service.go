package activation

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Service struct {
	provisioner Provisioner
	repository  Repository
	now         func() time.Time
}

// The device publishes status every 60 seconds. Three missed heartbeats fail closed to offline
// even if an MQTT LWT or its SQS delivery is lost.
const deviceOnlineFreshness = 3 * time.Minute

func NewService() *Service {
	return NewServiceWithRepository(nil, NewMemoryRepository())
}

func NewServiceWith(provisioner Provisioner) *Service {
	return NewServiceWithRepository(provisioner, NewMemoryRepository())
}

func NewServiceWithRepository(provisioner Provisioner, repository Repository) *Service {
	return &Service{provisioner: provisioner, repository: repository, now: func() time.Time { return time.Now().UTC() }}
}

func (service *Service) Create(ctx context.Context, input CreateInput) (Activation, error) {
	if service.repository == nil || strings.TrimSpace(input.InternalAppID) == "" ||
		strings.TrimSpace(input.BindingExternalUserID) == "" || strings.TrimSpace(input.IdempotencyKey) == "" ||
		len(input.IdempotencyKey) > 128 || !input.Method.valid() || strings.TrimSpace(input.PID) == "" {
		return Activation{}, ErrInvalidInput
	}
	product, err := service.findActiveProduct(ctx, input.PID)
	if err != nil {
		return Activation{}, err
	}
	requestHash := sha256.Sum256([]byte(strings.Join([]string{
		input.InternalAppID, input.BindingExternalUserID, string(input.Method), product.PID,
	}, "\n")))
	if existing, err := service.repository.FindByIdempotency(ctx, input.InternalAppID, input.IdempotencyKey); err == nil {
		if existing.Activation.RequestHash != requestHash {
			return Activation{}, ErrIdempotencyConflict
		}
		return Activation{}, ErrActivationInProgress
	} else if !errors.Is(err, ErrActivationNotFound) {
		return Activation{}, fmt.Errorf("find activation idempotency key: %w", err)
	}
	id, err := randomUUID()
	if err != nil {
		return Activation{}, fmt.Errorf("generate activation id: %w", err)
	}
	code, err := randomToken(32)
	if err != nil {
		return Activation{}, fmt.Errorf("generate activation code: %w", err)
	}
	value := Activation{
		ID: id, Code: code, Method: input.Method, PID: product.PID, InternalAppID: input.InternalAppID,
		BindingExternalUserID: input.BindingExternalUserID, IdempotencyKey: input.IdempotencyKey,
		RequestHash: requestHash, Status: StatusPending, ExpiresAt: service.now().Add(5 * time.Minute),
	}
	if err := service.repository.Create(ctx, Record{Activation: value, CodeHash: sha256.Sum256([]byte(code))}); err != nil {
		return Activation{}, err
	}
	return value, nil
}

func (service *Service) Prepare(ctx context.Context, input PrepareInput) (Installation, error) {
	if service.repository == nil || input.ActivationID == "" || input.Code == "" || input.CSRPEM == "" || !input.ExpectedMethod.valid() {
		return Installation{}, ErrInvalidInput
	}
	if service.provisioner == nil {
		return Installation{}, ErrProvisionerNotConfigured
	}
	record, err := service.repository.FindByID(ctx, input.ActivationID)
	if err != nil {
		return Installation{}, err
	}
	if err := service.authorize(record, input.Code, input.ExpectedMethod); err != nil {
		return Installation{}, err
	}
	if record.Installation.ActivationID != "" {
		if record.CSRPEM != input.CSRPEM {
			return Installation{}, ErrActivationAlreadyPrepared
		}
		return record.Installation, nil
	}
	if _, err := service.findActiveProduct(ctx, record.Activation.PID); err != nil {
		return Installation{}, err
	}
	deviceInternalID, err := randomUUID()
	if err != nil {
		return Installation{}, fmt.Errorf("generate internal device id: %w", err)
	}
	deviceSN, err := randomDeviceSN()
	if err != nil {
		return Installation{}, fmt.Errorf("generate device sn: %w", err)
	}
	record, err = service.repository.ReservePreparation(ctx, input.ActivationID, deviceInternalID, deviceSN, input.CSRPEM)
	if err != nil {
		return Installation{}, err
	}
	if record.Installation.ActivationID != "" {
		return record.Installation, nil
	}
	identity, err := service.provisioner.Provision(ctx, record.Activation.DeviceSN, input.CSRPEM)
	if err != nil {
		_ = service.repository.FailPreparation(ctx, input.ActivationID, "aws_iot_provision_failed")
		return Installation{}, fmt.Errorf("provision operational identity: %w", err)
	}
	if identity.DeviceID != record.Activation.DeviceSN || identity.CertificateID == "" ||
		identity.CertificatePEM == "" || identity.AWSIoTEndpoint == "" {
		_ = service.repository.FailPreparation(ctx, input.ActivationID, "aws_iot_incomplete_result")
		return Installation{}, fmt.Errorf("provision operational identity: incomplete result")
	}
	nonce, err := randomToken(32)
	if err != nil {
		_ = service.repository.FailPreparation(ctx, input.ActivationID, "nonce_generation_failed")
		return Installation{}, fmt.Errorf("generate installation proof nonce: %w", err)
	}
	installation := Installation{
		ActivationID: input.ActivationID, DeviceID: identity.DeviceID, CertificateID: identity.CertificateID,
		PID: record.Activation.PID, Certificate: identity.CertificatePEM, Endpoint: identity.AWSIoTEndpoint, ProofNonce: nonce,
	}
	completed, err := service.repository.CompletePreparation(ctx, input.ActivationID, installation)
	if err != nil {
		return Installation{}, fmt.Errorf("persist prepared operational identity: %w", err)
	}
	return completed.Installation, nil
}

func (service *Service) Confirm(ctx context.Context, input ConfirmInput) (Activation, error) {
	if service.repository == nil || input.ActivationID == "" || input.Code == "" || input.Proof == "" {
		return Activation{}, ErrInvalidInput
	}
	record, err := service.repository.FindByID(ctx, input.ActivationID)
	if err != nil {
		return Activation{}, err
	}
	if err := service.authorizeConfirmation(record, input.Code); err != nil {
		return Activation{}, err
	}
	if record.Installation.ActivationID == "" || record.CSRPEM == "" {
		return Activation{}, ErrActivationAlreadyPrepared
	}
	if err := verifyInstallationProof(record.CSRPEM, record.Installation, input.Proof); err != nil {
		return Activation{}, err
	}
	if record.Activation.Status == StatusActivated {
		value := record.Activation
		value.Code = ""
		return value, nil
	}
	if activator, ok := service.provisioner.(Activator); ok {
		if err := activator.Activate(ctx, record.Installation.CertificateID); err != nil {
			return Activation{}, fmt.Errorf("activate operational identity: %w", err)
		}
	}
	bindingID, err := randomUUID()
	if err != nil {
		return Activation{}, fmt.Errorf("generate binding id: %w", err)
	}
	record, err = service.repository.CompleteActivation(ctx, input.ActivationID, bindingID)
	if err != nil {
		return Activation{}, err
	}
	value := record.Activation
	value.Code = ""
	return value, nil
}

func (service *Service) GetForInternalApp(ctx context.Context, internalAppID, activationID string) (Activation, error) {
	if service.repository == nil || internalAppID == "" || activationID == "" {
		return Activation{}, ErrInvalidInput
	}
	record, err := service.repository.FindByID(ctx, activationID)
	if err != nil || record.Activation.InternalAppID != internalAppID {
		return Activation{}, ErrActivationNotFound
	}
	if !service.now().Before(record.Activation.ExpiresAt) && record.Activation.Status != StatusActivated {
		return Activation{}, ErrActivationExpired
	}
	value := record.Activation
	value.Code = ""
	return value, nil
}

func (service *Service) ListDevicesForBinding(ctx context.Context, internalAppID, externalUserID string) ([]Device, error) {
	if service.repository == nil || internalAppID == "" {
		return nil, ErrInvalidInput
	}
	devices, err := service.repository.ListDevices(ctx, internalAppID, externalUserID)
	if err != nil {
		return nil, err
	}
	oldestFreshPresence := service.now().Add(-deviceOnlineFreshness)
	for index := range devices {
		if devices[index].LastSeenAt == nil || devices[index].LastSeenAt.Before(oldestFreshPresence) {
			devices[index].Online = false
		}
	}
	return devices, nil
}

func (service *Service) ListActiveProducts(ctx context.Context) ([]Product, error) {
	if service.repository == nil {
		return nil, ErrProductUnavailable
	}
	return service.repository.ListActiveProducts(ctx)
}

func (service *Service) findActiveProduct(ctx context.Context, pid string) (Product, error) {
	if service.repository == nil {
		return Product{}, ErrProductUnavailable
	}
	return service.repository.FindActiveProduct(ctx, strings.TrimSpace(pid))
}

func (service *Service) authorize(record Record, code string, expectedMethod Method) error {
	if record.Activation.Method != expectedMethod || sha256.Sum256([]byte(code)) != record.CodeHash {
		return ErrActivationUnauthorized
	}
	if !service.now().Before(record.Activation.ExpiresAt) {
		return ErrActivationExpired
	}
	return nil
}

func (service *Service) authorizeConfirmation(record Record, code string) error {
	if record.Activation.Method == "" || sha256.Sum256([]byte(code)) != record.CodeHash {
		return ErrActivationUnauthorized
	}
	if record.Activation.Status != StatusActivated && !service.now().Before(record.Activation.ExpiresAt) {
		return ErrActivationExpired
	}
	return nil
}

func installationProofMessage(activationID, nonce string) string {
	return "ExploreX-Activation-Code-Commissioning-v1/install:" + activationID + ":" + nonce
}

func verifyInstallationProof(csrPEM string, installation Installation, encodedProof string) error {
	block, _ := pem.Decode([]byte(csrPEM))
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return ErrInstallationProofInvalid
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || csr.CheckSignature() != nil {
		return ErrInstallationProofInvalid
	}
	proof, err := base64.RawURLEncoding.DecodeString(encodedProof)
	if err != nil {
		return ErrInstallationProofInvalid
	}
	digest := sha256.Sum256([]byte(installationProofMessage(installation.ActivationID, installation.ProofNonce)))
	switch publicKey := csr.PublicKey.(type) {
	case *ecdsa.PublicKey:
		if !ecdsa.VerifyASN1(publicKey, digest[:], proof) {
			return ErrInstallationProofInvalid
		}
	case *rsa.PublicKey:
		if err := rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, digest[:], proof); err != nil {
			return ErrInstallationProofInvalid
		}
	default:
		return ErrInstallationProofInvalid
	}
	return nil
}

func randomToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func randomDeviceSN() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "fdr-" + hex.EncodeToString(value), nil
}

func randomUUID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}
