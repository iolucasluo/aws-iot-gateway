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
	"testing"
	"time"
)

func TestCreateBindsShortLivedCodeToUserAndMethod(t *testing.T) {
	t.Parallel()

	activation, err := NewService().Create(context.Background(), testCreateInput(MethodBLE))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if activation.ID == "" || activation.Code == "" {
		t.Fatal("Create() must issue an activation id and code")
	}
	if activation.InternalAppID != "livpet-staging" || activation.BindingExternalUserID != "user-42" || activation.Method != MethodBLE {
		t.Fatalf("Create() binding = %#v, want user-42 and ble", activation)
	}
	if time.Until(activation.ExpiresAt) <= 0 {
		t.Fatalf("Create() expiry = %s, want a future time", activation.ExpiresAt)
	}
}

func TestPrepareRequiresTheIssuedCodeAndReturnsOnlyADeviceInstallPayload(t *testing.T) {
	t.Parallel()

	service := NewServiceWith(fakeProvisioner{})
	created, err := service.Create(context.Background(), testCreateInput(MethodQR))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	installation, err := service.Prepare(context.Background(), PrepareInput{
		ActivationID:   created.ID,
		Code:           created.Code,
		CSRPEM:         "-----BEGIN CERTIFICATE REQUEST-----\\nrequest\\n-----END CERTIFICATE REQUEST-----",
		ExpectedMethod: MethodQR,
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

func TestPrepareGeneratesDeviceSNBeforeAWSProvisioning(t *testing.T) {
	t.Parallel()

	provisioner := &deviceSNProvisioner{}
	service := NewServiceWith(provisioner)
	created, err := service.Create(context.Background(), testCreateInput(MethodQR))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	installation, err := service.Prepare(context.Background(), PrepareInput{
		ActivationID: created.ID, Code: created.Code, CSRPEM: "csr", ExpectedMethod: MethodQR,
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if len(provisioner.deviceSN) != 36 || len(installation.DeviceID) != 36 ||
		provisioner.deviceSN[:4] != "fdr-" || installation.DeviceID != provisioner.deviceSN {
		t.Fatalf("generated device SN = %q, installation = %#v", provisioner.deviceSN, installation)
	}
}

func TestGetForUserDoesNotReturnActivationCode(t *testing.T) {
	t.Parallel()

	service := NewService()
	created, err := service.Create(context.Background(), testCreateInput(MethodAP))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	activation, err := service.GetForInternalApp(context.Background(), "livpet-staging", created.ID)
	if err != nil {
		t.Fatalf("GetForUser() error = %v", err)
	}
	if activation.Code != "" || activation.Status != StatusPending {
		t.Fatalf("GetForUser() = %#v, want metadata without code", activation)
	}
}

func TestPrepareRejectsAWrongActivationCode(t *testing.T) {
	t.Parallel()

	service := NewServiceWith(fakeProvisioner{})
	created, err := service.Create(context.Background(), testCreateInput(MethodQR))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	_, err = service.Prepare(context.Background(), PrepareInput{
		ActivationID:   created.ID,
		Code:           "wrong-code",
		CSRPEM:         "csr",
		ExpectedMethod: MethodQR,
	})
	if !errors.Is(err, ErrActivationUnauthorized) {
		t.Fatalf("Prepare() error = %v, want ErrActivationUnauthorized", err)
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
	created, err := service.Create(context.Background(), testCreateInput(MethodAP))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	installation, err := service.Prepare(context.Background(), PrepareInput{
		ActivationID:   created.ID,
		Code:           created.Code,
		CSRPEM:         csrPEM,
		ExpectedMethod: MethodAP,
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
	if confirmed.Status != StatusActivated || confirmed.Code != "" {
		t.Fatalf("Confirm() = %#v, want active metadata without code", confirmed)
	}
	retried, err := service.Confirm(context.Background(), ConfirmInput{
		ActivationID: created.ID,
		Code:         created.Code,
		Proof:        base64.RawURLEncoding.EncodeToString(signature),
	})
	if err != nil {
		t.Fatalf("Confirm() retry error = %v", err)
	}
	if retried.Status != StatusActivated || retried.ID != confirmed.ID || retried.Code != "" {
		t.Fatalf("Confirm() retry = %#v, want the existing activated result", retried)
	}
}

func TestConfirmRetriesAnActivatedInstallationAfterActivationExpiry(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, privateKey)
	if err != nil {
		t.Fatalf("CreateCertificateRequest() error = %v", err)
	}
	csrPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}))

	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	service := NewServiceWith(fakeProvisioner{})
	service.now = func() time.Time { return now }
	created, err := service.Create(context.Background(), testCreateInput(MethodQR))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	installation, err := service.Prepare(context.Background(), PrepareInput{
		ActivationID:   created.ID,
		Code:           created.Code,
		CSRPEM:         csrPEM,
		ExpectedMethod: MethodQR,
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	digest := sha256.Sum256([]byte(installationProofMessage(created.ID, installation.ProofNonce)))
	proof, err := ecdsa.SignASN1(rand.Reader, privateKey, digest[:])
	if err != nil {
		t.Fatalf("SignASN1() error = %v", err)
	}
	input := ConfirmInput{
		ActivationID: created.ID,
		Code:         created.Code,
		Proof:        base64.RawURLEncoding.EncodeToString(proof),
	}
	if _, err := service.Confirm(context.Background(), input); err != nil {
		t.Fatalf("initial Confirm() error = %v", err)
	}

	now = created.ExpiresAt.Add(time.Second)
	retried, err := service.Confirm(context.Background(), input)
	if err != nil {
		t.Fatalf("expired retry Confirm() error = %v, want activated idempotent result", err)
	}
	if retried.Status != StatusActivated || retried.Code != "" {
		t.Fatalf("expired retry Confirm() = %#v, want activated metadata without code", retried)
	}
}

func TestListDevicesForUserShowsOnlyActivatedInstallations(t *testing.T) {
	service := NewServiceWith(fakeProvisioner{})
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, privateKey)
	if err != nil {
		t.Fatalf("CreateCertificateRequest() error = %v", err)
	}
	csrPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}))
	created, err := service.Create(context.Background(), testCreateInput(MethodQR))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	installation, err := service.Prepare(context.Background(), PrepareInput{
		ActivationID:   created.ID,
		Code:           created.Code,
		CSRPEM:         csrPEM,
		ExpectedMethod: MethodQR,
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	digest := sha256.Sum256([]byte(installationProofMessage(created.ID, installation.ProofNonce)))
	proof, err := ecdsa.SignASN1(rand.Reader, privateKey, digest[:])
	if err != nil {
		t.Fatalf("SignASN1() error = %v", err)
	}
	if _, err := service.Confirm(context.Background(), ConfirmInput{
		ActivationID: created.ID,
		Code:         created.Code,
		Proof:        base64.RawURLEncoding.EncodeToString(proof),
	}); err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}

	devices, err := service.ListDevicesForBinding(context.Background(), "livpet-staging", "user-42")
	if err != nil {
		t.Fatalf("ListDevicesForUser() error = %v", err)
	}
	if len(devices) != 1 || devices[0].ID != installation.DeviceID ||
		devices[0].ActivationID != created.ID || devices[0].Endpoint == "" {
		t.Fatalf("ListDevicesForUser() = %#v, want the active installation", devices)
	}
	otherUserDevices, err := service.ListDevicesForBinding(context.Background(), "livpet-staging", "user-43")
	if err != nil {
		t.Fatalf("ListDevicesForUser() for another user error = %v", err)
	}
	if len(otherUserDevices) != 0 {
		t.Fatalf("ListDevicesForUser() leaked devices across users: %#v", otherUserDevices)
	}
}

type deviceListRepositoryStub struct {
	Repository
	devices []Device
}

func (repository *deviceListRepositoryStub) ListDevices(context.Context, string, string) ([]Device, error) {
	return append([]Device(nil), repository.devices...), nil
}

func TestDeviceListExpiresStaleOnlinePresence(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-2 * time.Minute)
	stale := now.Add(-4 * time.Minute)
	lastWill := now.Add(-time.Minute)
	repository := &deviceListRepositoryStub{devices: []Device{
		{ID: "fresh", Online: true, LastSeenAt: &fresh},
		{ID: "stale", Online: true, LastSeenAt: &stale},
		{ID: "lwt", Online: false, LastSeenAt: &lastWill},
	}}
	service := NewServiceWithRepository(nil, repository)
	service.now = func() time.Time { return now }

	devices, err := service.ListDevicesForBinding(context.Background(), "demo-staging", "user-1")
	if err != nil {
		t.Fatalf("ListDevicesForBinding() error = %v", err)
	}
	if len(devices) != 3 || !devices[0].Online || devices[1].Online || devices[2].Online {
		t.Fatalf("devices = %#v", devices)
	}
}

type fakeProvisioner struct{}

func (fakeProvisioner) Provision(_ context.Context, deviceSN, _ string) (PreparedIdentity, error) {
	return PreparedIdentity{
		DeviceID:       deviceSN,
		CertificateID:  "cert-1",
		CertificatePEM: "certificate-pem",
		AWSIoTEndpoint: "example-ats.iot.ap-southeast-1.amazonaws.com",
	}, nil
}

func testCreateInput(method Method) CreateInput {
	return CreateInput{
		InternalAppID: "livpet-staging", BindingExternalUserID: "user-42",
		IdempotencyKey: "operation-1-" + string(method), Method: method,
	}
}

type deviceSNProvisioner struct{ deviceSN string }

func (provisioner *deviceSNProvisioner) Provision(_ context.Context, deviceSN, _ string) (PreparedIdentity, error) {
	provisioner.deviceSN = deviceSN
	return PreparedIdentity{
		DeviceID: deviceSN, CertificateID: "cert-1", CertificatePEM: "certificate-pem",
		AWSIoTEndpoint: "endpoint-ats.iot.example",
	}, nil
}
