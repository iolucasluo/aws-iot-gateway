package activation

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestCreateBindsShortLivedCodeToUserAndMethod(t *testing.T) {
	t.Parallel()

	activation, err := NewService().Create(context.Background(), CreateInput{
		UserSubject: "user-42",
		Method:      MethodBLE,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if activation.ID == "" || activation.Code == "" {
		t.Fatal("Create() must issue an activation id and code")
	}
	if activation.User != "user-42" || activation.Method != MethodBLE {
		t.Fatalf("Create() binding = %#v, want user-42 and ble", activation)
	}
	if time.Until(activation.ExpiresAt) <= 0 {
		t.Fatalf("Create() expiry = %s, want a future time", activation.ExpiresAt)
	}
}

func TestPrepareRequiresTheIssuedCodeAndReturnsOnlyADeviceInstallPayload(t *testing.T) {
	t.Parallel()

	service := NewServiceWith(fakeProvisioner{})
	created, err := service.Create(context.Background(), CreateInput{
		UserSubject: "user-42",
		Method:      MethodQR,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	installation, err := service.Prepare(context.Background(), PrepareInput{
		ActivationID: created.ID,
		Code:         created.Code,
		CSRPEM:       "-----BEGIN CERTIFICATE REQUEST-----\\nrequest\\n-----END CERTIFICATE REQUEST-----",
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if installation.ActivationID != created.ID || installation.DeviceID == "" ||
		installation.Certificate == "" || installation.Endpoint == "" ||
		installation.ProofNonce == "" {
		t.Fatalf("Prepare() result = %#v, want a complete installation payload", installation)
	}
	if installation.Certificate == created.Code {
		t.Fatal("Prepare() must never echo the activation code as certificate material")
	}
}

func TestGetForUserDoesNotReturnActivationCode(t *testing.T) {
	t.Parallel()

	service := NewService()
	created, err := service.Create(context.Background(), CreateInput{
		UserSubject: "user-42",
		Method:      MethodAP,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	activation, err := service.GetForUser(context.Background(), "user-42", created.ID)
	if err != nil {
		t.Fatalf("GetForUser() error = %v", err)
	}
	if activation.Code != "" || activation.Status != StatusCreated {
		t.Fatalf("GetForUser() = %#v, want metadata without code", activation)
	}
}

func TestPrepareRejectsAWrongActivationCode(t *testing.T) {
	t.Parallel()

	service := NewServiceWith(fakeProvisioner{})
	created, err := service.Create(context.Background(), CreateInput{
		UserSubject: "user-42",
		Method:      MethodQR,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	_, err = service.Prepare(context.Background(), PrepareInput{
		ActivationID: created.ID,
		Code:         "wrong-code",
		CSRPEM:       "csr",
	})
	if !errors.Is(err, ErrActivationUnauthorized) {
		t.Fatalf("Prepare() error = %v, want ErrActivationUnauthorized", err)
	}
}

func TestFileBackedServiceSurvivesRestartWithoutPersistingTheCode(t *testing.T) {
	t.Parallel()

	store := NewFileStore(filepath.Join(t.TempDir(), "activations.json"))
	service, err := NewServiceWithStore(fakeProvisioner{}, store)
	if err != nil {
		t.Fatalf("NewServiceWithStore() error = %v", err)
	}
	created, err := service.Create(context.Background(), CreateInput{
		UserSubject: "user-42",
		Method:      MethodBLE,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	restarted, err := NewServiceWithStore(fakeProvisioner{}, store)
	if err != nil {
		t.Fatalf("NewServiceWithStore() after restart error = %v", err)
	}
	installation, err := restarted.Prepare(context.Background(), PrepareInput{
		ActivationID: created.ID,
		Code:         created.Code,
		CSRPEM:       "csr",
	})
	if err != nil {
		t.Fatalf("Prepare() after restart error = %v", err)
	}
	if installation.DeviceID == "" {
		t.Fatalf("Prepare() after restart = %#v, want installation", installation)
	}
}

func TestConfirmRequiresProofFromTheOperationalPrivateKey(t *testing.T) {
	t.Parallel()

	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, privateKey)
	if err != nil {
		t.Fatalf("CreateCertificateRequest() error = %v", err)
	}
	csrPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}))

	service := NewServiceWith(fakeProvisioner{})
	created, err := service.Create(context.Background(), CreateInput{
		UserSubject: "user-42",
		Method:      MethodAP,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	installation, err := service.Prepare(context.Background(), PrepareInput{
		ActivationID: created.ID,
		Code:         created.Code,
		CSRPEM:       csrPEM,
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	digest := sha256.Sum256([]byte(installationProofMessage(created.ID, installation.ProofNonce)))
	signature, err := ecdsa.SignASN1(rand.Reader, privateKey, digest[:])
	if err != nil {
		t.Fatalf("SignASN1() error = %v", err)
	}

	confirmed, err := service.Confirm(context.Background(), ConfirmInput{
		ActivationID: created.ID,
		Code:         created.Code,
		Proof:        base64.RawURLEncoding.EncodeToString(signature),
	})
	if err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}
	if confirmed.Status != StatusActive || confirmed.Code != "" {
		t.Fatalf("Confirm() = %#v, want active metadata without code", confirmed)
	}
}

type fakeProvisioner struct{}

func (fakeProvisioner) Provision(context.Context, string, string) (PreparedIdentity, error) {
	return PreparedIdentity{
		DeviceID:       "aws-iot-demo-device-1",
		CertificateID:  "cert-1",
		CertificatePEM: "certificate-pem",
		AWSIoTEndpoint: "example-ats.iot.ap-southeast-1.amazonaws.com",
	}, nil
}
