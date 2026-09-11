package postgres

import (
	"context"
	"errors"
	"testing"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/activation"
)

func TestProductProfileDefaultsPreserveActivationAndDeviceSnapshots(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	applyProductSeeds(t, ctx, pool)
	repository, _ := NewActivationRepository(pool)
	service := activation.NewServiceWithRepository(persistentProvisioner{}, repository)
	input := activation.CreateInput{InternalAppID: "test-app", BindingExternalUserID: "user", Method: activation.MethodQR, PID: "lamp-x1", IdempotencyKey: "original"}
	old, err := service.Create(ctx, input)
	if err != nil || old.DPProfile != "lamp-v1" {
		t.Fatalf("initial profile=%s error=%v", old.DPProfile, err)
	}
	// A deliberately different default demonstrates snapshot isolation; this is
	// a test fixture, not a declaration that Lamp hardware implements feeder-v1.
	if _, err := pool.Exec(ctx, `UPDATE products SET dp_profile_code='feeder-v1' WHERE pid='lamp-x1'; UPDATE dp_profiles SET status='retired' WHERE code='lamp-v1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(ctx, input); !errors.Is(err, activation.ErrActivationInProgress) {
		t.Fatalf("retry after default change: %v", err)
	}
	input.IdempotencyKey = "new-default"
	latest, err := service.Create(ctx, input)
	if err != nil || latest.DPProfile != "feeder-v1" {
		t.Fatalf("new profile=%s error=%v", latest.DPProfile, err)
	}
	input.PID = "feeder-x1"
	input.IdempotencyKey = "shared-profile"
	shared, err := service.Create(ctx, input)
	if err != nil || shared.DPProfile != latest.DPProfile {
		t.Fatalf("shared profile=%s error=%v", shared.DPProfile, err)
	}
	prepared, err := service.Prepare(ctx, activation.PrepareInput{ActivationID: old.ID, Code: old.Code, CSRPEM: "fixture-csr", ExpectedMethod: activation.MethodQR})
	if err != nil {
		t.Fatal(err)
	}
	var code string
	if err := pool.QueryRow(ctx, `SELECT p.code FROM device_registry d JOIN dp_profiles p ON p.id=d.dp_profile_id WHERE d.device_sn=$1`, prepared.DeviceID).Scan(&code); err != nil || code != "lamp-v1" {
		t.Fatalf("device snapshot=%s error=%v", code, err)
	}
	// Old fingerprints included profile; retry compares the original caller fields.
	if _, err := pool.Exec(ctx, `UPDATE device_activations SET request_hash=decode(repeat('ab',32),'hex') WHERE id=$1`, old.ID); err != nil {
		t.Fatal(err)
	}
	input.PID, input.IdempotencyKey = "lamp-x1", "original"
	if _, err := service.Create(ctx, input); !errors.Is(err, activation.ErrActivationInProgress) {
		t.Fatalf("legacy retry: %v", err)
	}
	input.BindingExternalUserID = "another-user"
	if _, err := service.Create(ctx, input); !errors.Is(err, activation.ErrIdempotencyConflict) {
		t.Fatalf("changed user: %v", err)
	}
}

func TestProductProfileSelectionFailsClosedAndRepositoryRejectsStaleMapping(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	applyProductSeeds(t, ctx, pool)
	repository, _ := NewActivationRepository(pool)
	service := activation.NewServiceWithRepository(nil, repository)
	input := activation.CreateInput{InternalAppID: "test-app", BindingExternalUserID: "user", Method: activation.MethodQR, PID: "lamp-x1", IdempotencyKey: "missing"}
	for _, statement := range []string{
		`UPDATE products SET dp_profile_code=NULL WHERE pid='lamp-x1'`,
		`UPDATE products SET dp_profile_code='lamp-v1' WHERE pid='lamp-x1'; UPDATE dp_profiles SET status='retired' WHERE code='lamp-v1'`,
		`UPDATE dp_profiles SET status='disabled' WHERE code='lamp-v1'`,
	} {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
		if _, err := service.Create(ctx, input); !errors.Is(err, activation.ErrDPProfileUnavailable) {
			t.Fatalf("unavailable mapping: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE dp_profiles SET status='active' WHERE code='lamp-v1'`); err != nil {
		t.Fatal(err)
	}
	value, err := service.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	value.ID, value.IdempotencyKey, value.DPProfile = "30000000-0000-0000-0000-000000000099", "stale", "feeder-v1"
	if err := repository.Create(ctx, activation.Record{Activation: value}); !errors.Is(err, activation.ErrDPProfileUnavailable) {
		t.Fatalf("repository accepted mismatched profile: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM device_activations`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("partial insert count=%d err=%v", count, err)
	}
	for _, statement := range []string{
		`UPDATE products SET dp_profile_code='unknown-v1' WHERE pid='lamp-x1'`,
		`UPDATE dp_profile_members SET dp_profile_code='unknown-v1' WHERE dp_profile_code='lamp-v1'`,
		`UPDATE dp_profiles SET code='renamed-v1' WHERE code='lamp-v1'`,
		`DELETE FROM dp_profiles WHERE code='lamp-v1'`,
	} {
		if _, err := pool.Exec(ctx, statement); err == nil {
			t.Fatalf("missing FK constraint: %s", statement)
		}
	}
}
