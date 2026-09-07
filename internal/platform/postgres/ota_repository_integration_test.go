package postgres

import (
	"context"
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/events"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/ota"
)

func TestSingleDeviceOTARepositoryPersistsEarlyProgressAndBootProof(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatalf("ApplyMigrations() error = %v", err)
	}
	applyProductSeeds(t, ctx, pool)

	const (
		deviceID     = "048f0000-0000-7000-8000-000000000001"
		deviceSN     = "fdr-40000000000000000000000000000001"
		versionID    = "11111111-1111-4111-8111-111111111111"
		deploymentID = "22222222-2222-4222-8222-222222222222"
	)
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `
		INSERT INTO device_registry (
			id, device_sn, thing_name, pid, status, presence_online, presence_updated_at
		) VALUES ($1,$2,$2,'feeder-x1','active',true,$3)
	`, deviceID, deviceSN, now); err != nil {
		t.Fatalf("insert OTA device: %v", err)
	}

	otaRepository, err := NewOTARepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	availableAt := now
	version, err := otaRepository.CreateFirmwareVersion(ctx, ota.FirmwareVersion{
		ID: versionID, CreatedByAppID: "publisher", IdempotencyKey: "publish-v42",
		PID: "feeder-x1", Version: "v1.2.3", FirmwareBuild: 42,
		SHA256:         "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		ManifestSHA256: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		FileSize:       1234, ReleaseStatement: "signed-release", Signature: "signature",
		SourceURL: "http://192.168.1.10:18080/ota.tar.gz", Status: ota.FirmwareStatusAvailable,
		CreatedAt: now, AvailableAt: &availableAt,
	})
	if err != nil {
		t.Fatalf("CreateFirmwareVersion() error = %v", err)
	}
	if version.ID != versionID || version.Status != ota.FirmwareStatusAvailable {
		t.Fatalf("firmware version = %#v", version)
	}

	deployment, _, err := otaRepository.CreateDeployment(ctx, ota.Deployment{
		ID: deploymentID, InternalAppID: "demo-app", DeviceID: deviceID, DeviceSN: deviceSN,
		FirmwareVersionID: versionID, IdempotencyKey: "one-device-one-click",
		State: ota.DeploymentStatePending, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("CreateDeployment() error = %v", err)
	}
	if deployment.State != ota.DeploymentStatePending || deployment.TargetBuild != 42 {
		t.Fatalf("deployment = %#v", deployment)
	}

	eventRepository, err := NewEventRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	storeProgress := func(eventID string, sequence int64, state string) {
		t.Helper()
		if err := eventRepository.Store(ctx, events.Event{
			ID: eventID, DeviceSN: deviceSN, EventType: "ota_progress",
			BootID: "33333333-3333-4333-8333-333333333333", EventSeq: sequence,
			ReceivedAt: time.Now().UTC(),
			Data:       map[string]any{"deployment_id": deploymentID, "state": state},
		}); err != nil {
			t.Fatalf("Store(%s) error = %v", state, err)
		}
	}
	storeProgress("44444444-4444-4444-8444-444444444441", 1, "downloading")
	if err := otaRepository.UpdateDeployment(ctx, deploymentID, ota.DeploymentStateAccepted, ""); err != nil {
		t.Fatalf("late ACK after early progress error = %v", err)
	}
	deployment, err = otaRepository.GetDeployment(ctx, "demo-app", deviceID, deploymentID)
	if err != nil || deployment.State != "downloading" {
		t.Fatalf("state after late ACK = %#v, %v", deployment, err)
	}

	storeProgress("44444444-4444-4444-8444-444444444442", 2, "verifying")
	storeProgress("44444444-4444-4444-8444-444444444443", 3, "installing")
	if err := eventRepository.Store(ctx, events.Event{
		ID: "44444444-4444-4444-8444-444444444444", DeviceSN: deviceSN, EventType: "boot",
		BootID: "55555555-5555-4555-8555-555555555555", EventSeq: 1,
		ReceivedAt: time.Now().UTC(), Data: map[string]any{"version": "v1.2.3", "build": float64(42)},
	}); err != nil {
		t.Fatalf("Store(boot) error = %v", err)
	}
	deployment, err = otaRepository.GetDeployment(ctx, "demo-app", deviceID, deploymentID)
	if err != nil || deployment.State != ota.DeploymentStateSucceeded || deployment.CompletedAt == nil {
		t.Fatalf("state after boot proof = %#v, %v", deployment, err)
	}
}
