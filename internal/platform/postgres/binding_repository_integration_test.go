package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/binding"
)

func TestBindingRepositoryRevokesAccessAndQueuesCertificateCleanup(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatalf("ApplyMigrations() error = %v", err)
	}
	applyProductSeeds(t, ctx, pool)

	const (
		deviceID  = "028f0000-0000-7000-8000-000000000001"
		bindingID = "028f0000-0000-7000-8000-000000000002"
		deviceSN  = "fdr-10000000000000000000000000000001"
	)
	if _, err := pool.Exec(ctx, `
		INSERT INTO device_registry (dp_profile_id, id, device_sn, thing_name, pid, certificate_id, status)
		VALUES ('40000000-0000-0000-0000-000000000001', $1,$2,$2,'feeder-x1','certificate-binding-test','active')
	`, deviceID, deviceSN); err != nil {
		t.Fatalf("insert device: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO device_bindings (id, device_registry_id, internal_app_id, binding_external_user_id, status)
		VALUES ($1,$2,'livpet-staging','user-binding-test','active')
	`, bindingID, deviceID); err != nil {
		t.Fatalf("insert binding: %v", err)
	}

	repository, err := NewBindingRepository(pool)
	if err != nil {
		t.Fatalf("NewBindingRepository() error = %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	service := binding.NewService(repository, func() (string, error) { return "persistent-device-token", nil }, func() time.Time { return now }, nil)
	issued, err := service.IssueToken(ctx, "livpet-staging", deviceSN)
	if err != nil {
		t.Fatalf("IssueToken() error = %v", err)
	}
	if _, err := service.AuthorizeDevice(ctx, "demo-staging", issued.DeviceToken, deviceSN); !errors.Is(err, binding.ErrForbidden) {
		t.Fatalf("cross-app AuthorizeDevice() error = %v", err)
	}
	if _, err := service.Unbind(ctx, "livpet-staging", issued.DeviceToken); err != nil {
		t.Fatalf("Unbind() error = %v", err)
	}
	if _, err := service.AuthorizeDevice(ctx, "livpet-staging", issued.DeviceToken, deviceSN); !errors.Is(err, binding.ErrForbidden) {
		t.Fatalf("released token AuthorizeDevice() error = %v", err)
	}

	var bindingStatus, deviceStatus string
	var revokedAt *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT b.status, d.status, t.revoked_at
		FROM device_bindings b
		JOIN device_registry d ON d.id=b.device_registry_id
		JOIN device_tokens t ON t.device_binding_id=b.id
		WHERE b.id=$1
	`, bindingID).Scan(&bindingStatus, &deviceStatus, &revokedAt); err != nil {
		t.Fatalf("read released state: %v", err)
	}
	if bindingStatus != "inactive" || deviceStatus != "revoked" || revokedAt == nil {
		t.Fatalf("released state = binding:%s device:%s revoked_at:%v", bindingStatus, deviceStatus, revokedAt)
	}

	var operation, status, certificateID string
	if err := pool.QueryRow(ctx, `
		SELECT operation, status, payload->>'certificate_id'
		FROM device_lifecycle_outbox WHERE device_registry_id=$1
	`, deviceID).Scan(&operation, &status, &certificateID); err != nil {
		t.Fatalf("read lifecycle outbox: %v", err)
	}
	if operation != "revoke_certificate" || status != "pending" || certificateID != "certificate-binding-test" {
		t.Fatalf("outbox = operation:%s status:%s certificate:%s", operation, status, certificateID)
	}
}
