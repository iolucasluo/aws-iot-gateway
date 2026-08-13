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
	"sort"
	"sync"
	"time"
)

var (
	ErrInvalidInput              = errors.New("invalid activation input")
	ErrActivationNotFound        = errors.New("activation not found")
	ErrActivationUnauthorized    = errors.New("activation code is invalid")
	ErrActivationExpired         = errors.New("activation has expired")
	ErrProvisionerNotConfigured  = errors.New("provisioner is not configured")
	ErrActivationAlreadyPrepared = errors.New("activation was prepared with another csr")
	ErrInstallationProofInvalid  = errors.New("installation proof is invalid")
)

type Method string

const (
	MethodQR  Method = "qr"
	MethodAP  Method = "ap"
	MethodBLE Method = "ble"
)

type CreateInput struct {
	UserSubject string
	Method      Method
}

type PrepareInput struct {
	ActivationID string
	Code         string
	CSRPEM       string
}

type ConfirmInput struct {
	ActivationID string
	Code         string
	Proof        string
}

type Activation struct {
	ID        string
	Code      string
	Method    Method
	User      string
	Status    Status
	ExpiresAt time.Time
}

type Status string

const (
	StatusCreated    Status = "created"
	StatusInstalling Status = "installing"
	StatusActive     Status = "active"
)

type PreparedIdentity struct {
	DeviceID       string
	CertificateID  string
	CertificatePEM string
	AWSIoTEndpoint string
}

type Installation struct {
	ActivationID  string
	DeviceID      string
	CertificateID string
	Certificate   string
	Endpoint      string
	ProofNonce    string
}

type Provisioner interface {
	Provision(context.Context, string, string) (PreparedIdentity, error)
}

type Activator interface {
	Activate(context.Context, string) error
}

type Service struct {
	provisioner Provisioner
	store       RecordStore
	mu          sync.Mutex
	records     map[string]record
	now         func() time.Time
}

type record struct {
	activation   Activation
	codeHash     [sha256.Size]byte
	csrPEM       string
	installation Installation
}

func NewService() *Service {
	return newService(nil)
}

func NewServiceWith(provisioner Provisioner) *Service {
	return newService(provisioner)
}

func NewServiceWithStore(provisioner Provisioner, store RecordStore) (*Service, error) {
	service := newService(provisioner)
	service.store = store
	if store == nil {
		return service, nil
	}
	records, err := store.Load(context.Background())
	if err != nil {
		return nil, fmt.Errorf("load activation state: %w", err)
	}
	for _, stored := range records {
		codeHash, err := hex.DecodeString(stored.CodeHash)
		if err != nil || len(codeHash) != sha256.Size || stored.Activation.ID == "" {
			return nil, fmt.Errorf("load activation state: invalid persisted record")
		}
		var hash [sha256.Size]byte
		copy(hash[:], codeHash)
		stored.Activation.Code = ""
		service.records[stored.Activation.ID] = record{
			activation:   stored.Activation,
			codeHash:     hash,
			csrPEM:       stored.CSRPEM,
			installation: stored.Installation,
		}
	}
	return service, nil
}

func newService(provisioner Provisioner) *Service {
	return &Service{
		provisioner: provisioner,
		records:     make(map[string]record),
		now:         func() time.Time { return time.Now().UTC() },
	}
}

func (s *Service) Create(ctx context.Context, input CreateInput) (Activation, error) {
	if input.UserSubject == "" || !input.Method.valid() {
		return Activation{}, ErrInvalidInput
	}

	id, err := randomToken(16)
	if err != nil {
		return Activation{}, fmt.Errorf("generate activation id: %w", err)
	}
	code, err := randomToken(32)
	if err != nil {
		return Activation{}, fmt.Errorf("generate activation code: %w", err)
	}
	activation := Activation{
		ID:        id,
		Code:      code,
		Method:    input.Method,
		User:      input.UserSubject,
		Status:    StatusCreated,
		ExpiresAt: s.now().Add(5 * time.Minute),
	}
	s.mu.Lock()
	s.records[activation.ID] = record{
		activation: activation,
		codeHash:   sha256.Sum256([]byte(code)),
	}
	if err := s.persistLocked(ctx); err != nil {
		delete(s.records, activation.ID)
		s.mu.Unlock()
		return Activation{}, err
	}
	s.mu.Unlock()
	return activation, nil
}

func (s *Service) Prepare(ctx context.Context, input PrepareInput) (Installation, error) {
	if input.ActivationID == "" || input.Code == "" || input.CSRPEM == "" {
		return Installation{}, ErrInvalidInput
	}
	if s.provisioner == nil {
		return Installation{}, ErrProvisionerNotConfigured
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.authorizeLocked(input.ActivationID, input.Code)
	if err != nil {
		return Installation{}, err
	}
	if record.installation.ActivationID != "" {
		if record.csrPEM != input.CSRPEM {
			return Installation{}, ErrActivationAlreadyPrepared
		}
		return record.installation, nil
	}

	identity, err := s.provisioner.Provision(ctx, input.ActivationID, input.CSRPEM)
	if err != nil {
		return Installation{}, fmt.Errorf("provision operational identity: %w", err)
	}
	if identity.DeviceID == "" || identity.CertificateID == "" ||
		identity.CertificatePEM == "" || identity.AWSIoTEndpoint == "" {
		return Installation{}, fmt.Errorf("provision operational identity: incomplete result")
	}
	nonce, err := randomToken(32)
	if err != nil {
		return Installation{}, fmt.Errorf("generate installation proof nonce: %w", err)
	}
	record.csrPEM = input.CSRPEM
	record.installation = Installation{
		ActivationID:  input.ActivationID,
		DeviceID:      identity.DeviceID,
		CertificateID: identity.CertificateID,
		Certificate:   identity.CertificatePEM,
		Endpoint:      identity.AWSIoTEndpoint,
		ProofNonce:    nonce,
	}
	record.activation.Status = StatusInstalling
	s.records[input.ActivationID] = record
	if err := s.persistLocked(ctx); err != nil {
		return Installation{}, err
	}
	return record.installation, nil
}

func (s *Service) Confirm(ctx context.Context, input ConfirmInput) (Activation, error) {
	if input.ActivationID == "" || input.Code == "" || input.Proof == "" {
		return Activation{}, ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.authorizeLocked(input.ActivationID, input.Code)
	if err != nil {
		return Activation{}, err
	}
	if record.installation.ActivationID == "" || record.csrPEM == "" {
		return Activation{}, ErrActivationAlreadyPrepared
	}
	if err := verifyInstallationProof(record.csrPEM, record.installation, input.Proof); err != nil {
		return Activation{}, err
	}
	if activator, ok := s.provisioner.(Activator); ok {
		if err := activator.Activate(ctx, record.installation.CertificateID); err != nil {
			return Activation{}, fmt.Errorf("activate operational identity: %w", err)
		}
	}
	record.activation.Status = StatusActive
	record.activation.Code = ""
	s.records[input.ActivationID] = record
	if err := s.persistLocked(ctx); err != nil {
		return Activation{}, err
	}
	return record.activation, nil
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

func (s *Service) GetForUser(_ context.Context, userSubject, activationID string) (Activation, error) {
	if userSubject == "" || activationID == "" {
		return Activation{}, ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[activationID]
	if !ok || record.activation.User != userSubject {
		return Activation{}, ErrActivationNotFound
	}
	if !s.now().Before(record.activation.ExpiresAt) {
		return Activation{}, ErrActivationExpired
	}
	activation := record.activation
	activation.Code = ""
	return activation, nil
}

func (s *Service) authorizeLocked(activationID, code string) (record, error) {
	record, ok := s.records[activationID]
	if !ok {
		return record, ErrActivationNotFound
	}
	if !s.now().Before(record.activation.ExpiresAt) {
		return record, ErrActivationExpired
	}
	if sha256.Sum256([]byte(code)) != record.codeHash {
		return record, ErrActivationUnauthorized
	}
	return record, nil
}

func (s *Service) persistLocked(ctx context.Context) error {
	if s.store == nil {
		return nil
	}
	ids := make([]string, 0, len(s.records))
	for id := range s.records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	stored := make([]StoredRecord, 0, len(ids))
	for _, id := range ids {
		record := s.records[id]
		activation := record.activation
		activation.Code = ""
		stored = append(stored, StoredRecord{
			Activation:   activation,
			CodeHash:     hex.EncodeToString(record.codeHash[:]),
			CSRPEM:       record.csrPEM,
			Installation: record.installation,
		})
	}
	if err := s.store.Save(ctx, stored); err != nil {
		return fmt.Errorf("persist activation state: %w", err)
	}
	return nil
}

func (m Method) valid() bool {
	return m == MethodQR || m == MethodAP || m == MethodBLE
}

func randomToken(size int) (string, error) {
	bytes := make([]byte, size)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}
