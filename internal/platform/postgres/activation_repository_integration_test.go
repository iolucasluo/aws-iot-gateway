package postgres

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/activation"
)

type persistentProvisioner struct{}

func (persistentProvisioner) Provision(_ context.Context, deviceSN, _ string) (activation.PreparedIdentity, error) {
	return activation.PreparedIdentity{
		DeviceID: deviceSN, CertificateID: "certificate-persistent", CertificatePEM: "certificate-pem",
		AWSIoTEndpoint: "endpoint-ats.iot.ap-east-1.amazonaws.com",
	}, nil
}

func TestActivationRepositorySurvivesServiceRestartAndCreatesBinding(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatalf("ApplyMigrations() error = %v", err)
	}
	repository, err := NewActivationRepository(pool)
	if err != nil {
		t.Fatalf("NewActivationRepository() error = %v", err)
	}
	service := activation.NewServiceWithRepository(persistentProvisioner{}, repository)
	created, err := service.Create(ctx, activation.CreateInput{
		InternalAppID: "livpet-staging", BindingExternalUserID: "user-persistent",
		IdempotencyKey: "persistent-operation", Method: activation.MethodBLE, PID: "minion-cam-x1",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, privateKey)
	if err != nil {
		t.Fatalf("CreateCertificateRequest() error = %v", err)
	}
	csrPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}))

	restarted := activation.NewServiceWithRepository(persistentProvisioner{}, repository)
	installation, err := restarted.Prepare(ctx, activation.PrepareInput{
		ActivationID: created.ID, Code: created.Code, CSRPEM: csrPEM, ExpectedMethod: activation.MethodBLE,
	})
	if err != nil {
		t.Fatalf("Prepare() after restart error = %v", err)
	}
	if installation.DeviceID == "" || installation.CertificateID != "certificate-persistent" {
		t.Fatalf("installation = %#v", installation)
	}

	restartedAgain := activation.NewServiceWithRepository(persistentProvisioner{}, repository)
	digest := sha256.Sum256([]byte("ExploreX-Activation-Code-Commissioning-v1/install:" + created.ID + ":" + installation.ProofNonce))
	proof, err := ecdsa.SignASN1(rand.Reader, privateKey, digest[:])
	if err != nil {
		t.Fatalf("SignASN1() error = %v", err)
	}
	confirmed, err := restartedAgain.Confirm(ctx, activation.ConfirmInput{
		ActivationID: created.ID, Code: created.Code, Proof: base64.RawURLEncoding.EncodeToString(proof),
	})
	if err != nil {
		t.Fatalf("Confirm() after restart error = %v", err)
	}
	if confirmed.Status != activation.StatusActivated || confirmed.DeviceSN != installation.DeviceID || confirmed.PID != "minion-cam-x1" {
		t.Fatalf("confirmed = %#v", confirmed)
	}
	presenceRepository, err := NewEventRepository(pool)
	if err != nil {
		t.Fatalf("NewEventRepository() error = %v", err)
	}
	presenceAt := time.Now().UTC().Truncate(time.Millisecond)
	if err := presenceRepository.UpdatePresence(ctx, installation.DeviceID, true, presenceAt); err != nil {
		t.Fatalf("UpdatePresence() error = %v", err)
	}
	devices, err := restartedAgain.ListDevicesForBinding(ctx, "livpet-staging", "user-persistent")
	if err != nil {
		t.Fatalf("ListDevicesForBinding() error = %v", err)
	}
	if len(devices) != 1 || devices[0].ID != installation.DeviceID || devices[0].PID != "minion-cam-x1" || !devices[0].Online ||
		devices[0].LastSeenAt == nil || !devices[0].LastSeenAt.Equal(presenceAt) {
		t.Fatalf("devices = %#v", devices)
	}

	var storedCodeHash []byte
	if err := pool.QueryRow(ctx, `SELECT code_hash FROM device_activations WHERE id=$1`, created.ID).Scan(&storedCodeHash); err != nil {
		t.Fatalf("read code hash: %v", err)
	}
	if len(storedCodeHash) != sha256.Size || string(storedCodeHash) == created.Code {
		t.Fatal("PostgreSQL did not retain only a fixed-size activation code hash")
	}
}
