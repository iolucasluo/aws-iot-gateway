package postgres

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/activation"
)

// identityTestProvisioner 让集成测试聚焦 activation/registry 归并，不触碰 AWS IoT。
type identityTestProvisioner struct{}

func (identityTestProvisioner) Provision(_ context.Context, deviceSN, _ string) (activation.PreparedIdentity, error) {
	return activation.PreparedIdentity{
		DeviceID:       deviceSN,
		CertificateID:  "certificate-" + deviceSN,
		CertificatePEM: "-----BEGIN CERTIFICATE-----\nfixture\n-----END CERTIFICATE-----",
		Endpoint:       "endpoint-ats.iot.example",
	}, nil
}

func createIdentityActivation(t *testing.T, ctx context.Context, service *activation.Service, pid, idempotencyKey, chipUID string) activation.Activation {
	t.Helper()
	created, err := service.Create(ctx, activation.CreateInput{
		InternalAppID: "livpet-staging", BindingExternalUserID: "user-42",
		IdempotencyKey: idempotencyKey, Method: activation.MethodBLE, PID: pid, ChipUID: chipUID,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	return created
}

func prepareIdentityActivation(t *testing.T, ctx context.Context, service *activation.Service, created activation.Activation, csr string) activation.Installation {
	t.Helper()
	installation, err := service.Prepare(ctx, activation.PrepareInput{
		ActivationID: created.ID, Code: created.Code, CSRPEM: csr, ExpectedMethod: activation.MethodBLE,
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	return installation
}

// 同一 chip UID 的第二次激活必须复用同一 device_registry 行与同一 device_sn。
func TestActivationReusesDeviceRegistryRowForSameChipUID(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := ApplySeeds(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repository, err := NewActivationRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	service := activation.NewServiceWithRepository(identityTestProvisioner{}, repository)

	const chipUIDHex = "b5eb3f00adc1"
	first := createIdentityActivation(t, ctx, service, "feeder-x1", "identity-first", chipUIDHex)
	firstInstallation := prepareIdentityActivation(t, ctx, service, first, "csr-first")
	second := createIdentityActivation(t, ctx, service, "feeder-x1", "identity-second", chipUIDHex)
	secondInstallation := prepareIdentityActivation(t, ctx, service, second, "csr-second")

	if firstInstallation.DeviceID != secondInstallation.DeviceID {
		t.Fatalf("device sn changed across activation: %q -> %q", firstInstallation.DeviceID, secondInstallation.DeviceID)
	}
	chipUID, err := hex.DecodeString(chipUIDHex)
	if err != nil {
		t.Fatal(err)
	}
	var rows int
	var rowSN string
	if err := pool.QueryRow(ctx, `
		SELECT count(*), COALESCE(max(device_sn), '')
		FROM device_registry WHERE chip_uid=$1
	`, chipUID).Scan(&rows, &rowSN); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("device_registry rows for chip uid = %d, want 1", rows)
	}
	if rowSN != firstInstallation.DeviceID {
		t.Fatalf("registry device sn = %q, want %q", rowSN, firstInstallation.DeviceID)
	}
}

// 同一 chip UID 出现在不同产品上必须拒绝，而不是复用或新建第二行。
func TestActivationRejectsChipUIDBoundToAnotherProduct(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := ApplySeeds(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repository, err := NewActivationRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	service := activation.NewServiceWithRepository(identityTestProvisioner{}, repository)

	const chipUIDHex = "b5eb3f00adc1"
	first := createIdentityActivation(t, ctx, service, "feeder-x1", "identity-feeder", chipUIDHex)
	prepareIdentityActivation(t, ctx, service, first, "csr-feeder")

	second := createIdentityActivation(t, ctx, service, "lamp-x1", "identity-lamp", chipUIDHex)
	if _, err := service.Prepare(ctx, activation.PrepareInput{
		ActivationID: second.ID, Code: second.Code, CSRPEM: "csr-lamp", ExpectedMethod: activation.MethodBLE,
	}); !errors.Is(err, activation.ErrDeviceIdentityConflict) {
		t.Fatalf("Prepare() error = %v, want ErrDeviceIdentityConflict", err)
	}
}
