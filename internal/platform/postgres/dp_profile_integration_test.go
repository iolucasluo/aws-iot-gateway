package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/activation"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/device"
)

func TestDPProfilesArePublishedWithoutDevelopmentSeeds(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var initialProfiles int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dp_profiles`).Scan(&initialProfiles); err != nil {
		t.Fatal(err)
	}
	if initialProfiles != 0 {
		t.Fatal("migrations inserted initial DP profiles")
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := ApplyContractSeeds(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	var profiles, members, definitions, products int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dp_profiles`).Scan(&profiles); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dp_profile_members`).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dp_definitions`).Scan(&definitions); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM products`).Scan(&products); err != nil {
		t.Fatal(err)
	}
	if profiles != 3 || members != 95 || definitions >= members || products != 0 {
		t.Fatalf("published profiles/members/definitions/products = %d/%d/%d/%d", profiles, members, definitions, products)
	}
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
}

func TestDPProfileSelectionIsIndependentOfProduct(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	applyProductSeeds(t, ctx, pool)
	insertProfileDevice(t, pool, "30000000-0000-0000-0000-000000000001", "lamp-a", "lamp-x1", "feeder-v1")
	insertProfileDevice(t, pool, "30000000-0000-0000-0000-000000000002", "camera-a", "minion-cam-x1", "feeder-v1")
	repository, err := NewDPProfileRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	left, err := repository.LoadDPDefinitions(ctx, "lamp-a")
	if err != nil {
		t.Fatal(err)
	}
	right, err := repository.LoadDPDefinitions(ctx, "camera-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 39 || !reflect.DeepEqual(left, right) {
		t.Fatal("same profile must yield the same contract across products")
	}
	if err := device.ValidateCommands(left, []device.CommandInput{{Code: "speaker_vol", Value: json.RawMessage(`{"volume":200}`)}}); !errors.Is(err, device.ErrDPInvalidValue) {
		t.Fatalf("out of range volume = %v", err)
	}
	// A profile assignment, not a PID fallback, selects a different contract.
	if _, err := pool.Exec(ctx, `UPDATE device_registry SET dp_profile_id=(SELECT id FROM dp_profiles WHERE code='camera-v1') WHERE device_sn='lamp-a'`); err != nil {
		t.Fatal(err)
	}
	changed, err := repository.LoadDPDefinitions(ctx, "lamp-a")
	if err != nil || len(changed) != 28 {
		t.Fatalf("changed profile count=%d error=%v", len(changed), err)
	}
	if _, err := pool.Exec(ctx, `UPDATE dp_profiles SET status='disabled' WHERE code='camera-v1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.LoadDPDefinitions(ctx, "lamp-a"); !errors.Is(err, device.ErrDPUnavailable) {
		t.Fatalf("disabled profile error=%v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE device_registry SET dp_profile_id=NULL WHERE device_sn='camera-a'`); err == nil {
		t.Fatal("registered device lost its required profile")
	}
	if _, err := repository.LoadDPDefinitions(ctx, "unknown-device"); !errors.Is(err, device.ErrDPUnavailable) {
		t.Fatalf("unknown device error=%v", err)
	}
}

func insertProfileDevice(t *testing.T, pool *pgxpool.Pool, id, sn, pid, profile string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO device_registry (id, device_sn, thing_name, pid, dp_profile_id, status)
		VALUES ($1,$2,$2,$3,(SELECT id FROM dp_profiles WHERE code=$4),'active')
	`, id, sn, pid, profile); err != nil {
		t.Fatal(err)
	}
}

// The old catalog may contain reviewed deployment-specific changes. Preserve the
// database snapshot, not today's source seed, when upgrading existing devices.
func TestActivationPersistsProfileAndRechecksDisabledSelection(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	applyProductSeeds(t, ctx, pool)
	repository, _ := NewActivationRepository(pool)
	service := activation.NewServiceWithRepository(persistentProvisioner{}, repository)
	input := activation.CreateInput{InternalAppID: "test-app", BindingExternalUserID: "test-user", Method: activation.MethodQR, PID: "feeder-x1", IdempotencyKey: "profile-selection"}
	created, err := service.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	input.PID = "minion-cam-x1"
	if _, err := service.Create(ctx, input); !errors.Is(err, activation.ErrIdempotencyConflict) {
		t.Fatalf("profile changed under same key: %v", err)
	}
	input.PID = "feeder-x1"
	if _, err := service.Create(ctx, input); !errors.Is(err, activation.ErrActivationInProgress) {
		t.Fatalf("same profile retry: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE dp_profiles SET status='disabled' WHERE code='feeder-v1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ReservePreparation(ctx, activation.Reservation{
		ActivationID:     created.ID,
		DeviceInternalID: "30000000-0000-0000-0000-000000000001",
		DeviceSN:         "profile-device",
		CSRPEM:           "fixture-csr",
	}); !errors.Is(err, activation.ErrDPProfileUnavailable) {
		t.Fatalf("prepare disabled profile: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM device_registry`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("disabled preparation created device: count=%d error=%v", count, err)
	}
	input.IdempotencyKey = "disabled-new"
	if _, err := service.Create(ctx, input); !errors.Is(err, activation.ErrDPProfileUnavailable) {
		t.Fatalf("create disabled profile: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE dp_profiles SET status='active' WHERE code='feeder-v1'`); err != nil {
		t.Fatal(err)
	}
	prepared, err := service.Prepare(ctx, activation.PrepareInput{ActivationID: created.ID, Code: created.Code, CSRPEM: "fixture-csr", ExpectedMethod: activation.MethodQR})
	if err != nil {
		t.Fatal(err)
	}
	var profile string
	if err := pool.QueryRow(ctx, `SELECT profile.code FROM device_registry d JOIN dp_profiles profile ON profile.id=d.dp_profile_id WHERE d.device_sn=$1`, prepared.DeviceID).Scan(&profile); err != nil || profile != "feeder-v1" {
		t.Fatalf("registered profile=%s error=%v", profile, err)
	}
}

func TestDPContractSeedUpdatesDeclaredData(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := ApplyContractSeeds(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE dp_profiles SET status='disabled' WHERE code='feeder-v1';
		UPDATE dp_profile_members SET status='disabled' WHERE code='speaker_vol';`); err != nil {
		t.Fatal(err)
	}
	if err := ApplyContractSeeds(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM dp_profiles WHERE code='feeder-v1'`).Scan(&status); err != nil || status != "active" {
		t.Fatalf("profile state=%s error=%v", status, err)
	}
	var enabled int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dp_profile_members WHERE code='speaker_vol' AND status='active'`).Scan(&enabled); err != nil || enabled != 3 {
		t.Fatalf("seed active members=%d error=%v", enabled, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE dp_definitions SET spec='{}' WHERE code='speaker_vol'`); err != nil {
		t.Fatal(err)
	}
	if err := ApplyContractSeeds(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var empty int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dp_definitions WHERE code='speaker_vol' AND spec='{}'::jsonb`).Scan(&empty); err != nil || empty != 0 {
		t.Fatalf("seed did not restore definitions: empty=%d error=%v", empty, err)
	}
}
