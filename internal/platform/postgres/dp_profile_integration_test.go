package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/db/migrations"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/db/seeds"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/activation"
	"io/fs"
	"reflect"
	"strings"
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
	if profiles != 3 || members != 53 || definitions >= members || products != 0 {
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
	if len(left) != 25 || !reflect.DeepEqual(left, right) {
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
func TestDPProfilesMigrateLegacyContractsAndRoundTripSafely(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	applyBeforeDPProfiles(t, pool)
	oldProducts, err := seeds.Files.ReadFile("001_products.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(oldProducts)); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO product_dps (id,product_id,code,type,direction,spec,status) VALUES
		('20000000-0000-0000-0000-000000000001','10000000-0000-0000-0000-000000000001','brightness','int','rw','{"min":0,"max":100}','active'),
		('20000000-0000-0000-0000-000000000002','10000000-0000-0000-0000-000000000002','brightness','int','rw','{"min":0,"max":255}','disabled'),
		('20000000-0000-0000-0000-000000000003','10000000-0000-0000-0000-000000000003','brightness','int','rw','{"min":0,"max":100}','active');
		INSERT INTO device_registry (id,device_sn,thing_name,pid,status) VALUES
		('30000000-0000-0000-0000-000000000001','old-lamp','old-lamp','lamp-x1','active');
		INSERT INTO device_activations (id,internal_app_id,binding_external_user_id,method,pid,status,code_hash,idempotency_key,request_hash,expires_at)
		VALUES ('30000000-0000-0000-0000-000000000002','test-app','test-user','qr','lamp-x1','pending',decode(repeat('aa',32),'hex'),'old-activation',decode(repeat('bb',32),'hex'),now()+interval '1 hour');
	`)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var shared, distinct bool
	if err := pool.QueryRow(ctx, `
		SELECT a.dp_definition_id=c.dp_definition_id, a.dp_definition_id<>b.dp_definition_id
		FROM dp_profile_members a, dp_profile_members b, dp_profile_members c
		WHERE a.id='20000000-0000-0000-0000-000000000001' AND b.id='20000000-0000-0000-0000-000000000002' AND c.id='20000000-0000-0000-0000-000000000003'
	`).Scan(&shared, &distinct); err != nil || !shared || !distinct {
		t.Fatalf("full contract reuse=%t distinct variants=%t error=%v", shared, distinct, err)
	}
	repository, _ := NewDPProfileRepository(pool)
	definitions, err := repository.LoadDPDefinitions(ctx, "old-lamp")
	if err != nil || len(definitions) != 1 || definitions[0].Code != "brightness" {
		t.Fatalf("legacy contract=%v error=%v", definitions, err)
	}
	activationRepository, _ := NewActivationRepository(pool)
	record, err := activationRepository.FindByID(ctx, "30000000-0000-0000-0000-000000000002")
	if err != nil || record.Activation.DPProfile != "legacy-10000000-0000-0000-0000-000000000003-v1" {
		t.Fatalf("legacy activation profile=%s error=%v", record.Activation.DPProfile, err)
	}
	if _, err := activationRepository.FindActiveDPProfile(ctx, record.Activation.DPProfile); !errors.Is(err, activation.ErrDPProfileUnavailable) {
		t.Fatalf("retired profile is selectable: %v", err)
	}
	// Retired snapshots can complete an already issued activation.
	if _, err := activationRepository.ReservePreparation(ctx, record.Activation.ID, "30000000-0000-0000-0000-000000000004", "old-pending", "fixture-csr"); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	applyDownProfileMigration(t, pool, "010_dp_profiles.down.sql")
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM product_dps`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("restored rows=%d error=%v", count, err)
	}
	var status, max string
	if err := pool.QueryRow(ctx, `SELECT status,spec->>'max' FROM product_dps WHERE id='20000000-0000-0000-0000-000000000002'`).Scan(&status, &max); err != nil || status != "disabled" || max != "255" {
		t.Fatalf("restored disabled variant=%s/%s error=%v", status, max, err)
	}
}

func TestDPProfileRollbackRejectsInUsePublishedContractsAtomically(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	applyProductSeeds(t, ctx, pool)
	insertProfileDevice(t, pool, "30000000-0000-0000-0000-000000000001", "new-lamp", "lamp-x1", "feeder-v1")
	down, err := migrations.Files.ReadFile("010_dp_profiles.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(down)); err == nil {
		t.Fatal("rollback accepted in-use published profile")
	}
	repository, _ := NewDPProfileRepository(pool)
	definitions, err := repository.LoadDPDefinitions(ctx, "new-lamp")
	if err != nil || len(definitions) != 25 {
		t.Fatalf("failed rollback changed contracts: count=%d error=%v", len(definitions), err)
	}
}

func TestActivationPersistsProfileAndRechecksDisabledSelection(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	applyProductSeeds(t, ctx, pool)
	repository, _ := NewActivationRepository(pool)
	service := activation.NewServiceWithRepository(persistentProvisioner{}, repository)
	input := activation.CreateInput{InternalAppID: "test-app", BindingExternalUserID: "test-user", Method: activation.MethodQR, PID: "lamp-x1", DPProfile: "feeder-v1", IdempotencyKey: "profile-selection"}
	created, err := service.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	input.DPProfile = "camera-v1"
	if _, err := service.Create(ctx, input); !errors.Is(err, activation.ErrIdempotencyConflict) {
		t.Fatalf("profile changed under same key: %v", err)
	}
	input.DPProfile = "feeder-v1"
	if _, err := service.Create(ctx, input); !errors.Is(err, activation.ErrActivationInProgress) {
		t.Fatalf("same profile retry: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE dp_profiles SET status='disabled' WHERE code='feeder-v1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ReservePreparation(ctx, created.ID, "30000000-0000-0000-0000-000000000001", "profile-device", "fixture-csr"); !errors.Is(err, activation.ErrDPProfileUnavailable) {
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

func applyBeforeDPProfiles(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE TABLE gateway_schema_migrations (name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if name >= "010" || !strings.HasSuffix(name, ".sql") || strings.HasSuffix(name, ".down.sql") {
			continue
		}
		content, err := migrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(content)); err != nil {
			t.Fatalf("pre-profile migration %s: %v", name, err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO gateway_schema_migrations(name) VALUES ($1)`, name); err != nil {
			t.Fatal(err)
		}
	}
}

func applyDownProfileMigration(t *testing.T, pool *pgxpool.Pool, name string) {
	t.Helper()
	ctx := context.Background()
	content, err := migrations.Files.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	if _, err := transaction.Exec(ctx, string(content)); err != nil {
		t.Fatalf("rollback %s: %v", name, err)
	}
	if _, err := transaction.Exec(ctx, `DELETE FROM gateway_schema_migrations WHERE name=$1`, strings.Replace(name, ".down.sql", ".up.sql", 1)); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(ctx); err != nil {
		t.Fatal(err)
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
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dp_profile_members WHERE code='speaker_vol' AND status='active'`).Scan(&enabled); err != nil || enabled != 2 {
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
