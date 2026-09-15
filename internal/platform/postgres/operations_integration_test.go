package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/operations"
)

const (
	opsDeviceID     = "018f0000-0000-7000-8000-0000000000e1"
	opsBindingID    = "018f0000-0000-7000-8000-0000000000e2"
	opsActivationID = "018f0000-0000-7000-8000-0000000000e3"
	opsAuditID      = "018f0000-0000-7000-8000-0000000000e4"
	opsDeviceSN     = "fdr-000000000000000000000000000000e1"
	opsSubjectID    = "50000000-0000-0000-0000-0000000000e1"
)

func seedOperationsFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	applyProductSeeds(t, ctx, pool)
	var dpProfileID string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM dp_profiles WHERE status='active' ORDER BY code LIMIT 1`).Scan(&dpProfileID); err != nil {
		t.Fatalf("read seeded dp profile: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO device_registry (
			id, dp_profile_id, device_sn, thing_name, pid, status, presence_online, last_seen_at, certificate_id
		) VALUES ($1,$2,$3,$3,'feeder-x1','active',true,now(),'cert-ops-1')
	`, opsDeviceID, dpProfileID, opsDeviceSN); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO device_bindings (id, device_registry_id, internal_app_id, binding_external_user_id, status, activated_at)
		VALUES ($1,$2,$3,'user-1','active',now())
	`, opsBindingID, opsDeviceID, opsSubjectID); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO device_activations (
			id, dp_profile_id, internal_app_id, binding_external_user_id, method, pid, status,
			code_hash, idempotency_key, request_hash, device_registry_id, expires_at
		) VALUES ($1,$2,$3,'user-1','ble','feeder-x1','activated',
		          decode(repeat('aa', 32), 'hex'), 'ops-fixture', decode(repeat('bb', 32), 'hex'), $4, now() + interval '1 hour')
	`, opsActivationID, dpProfileID, opsSubjectID, opsDeviceID); err != nil {
		t.Fatalf("seed activation: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO audit_logs (id, actor_kind, actor_account_id, actor_label, operation, resource_type, resource_id, outcome)
		VALUES ($1,'portal_account','10000000-0000-0000-0000-0000000000e1','ops@example.com','POST developer.projects','projects',NULL,'succeeded')
	`, opsAuditID); err != nil {
		t.Fatalf("seed audit log: %v", err)
	}
}

func TestOperationsRepositoryProjectsDeviceStateOwnershipAndAudit(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatalf("ApplyMigrations() error = %v", err)
	}
	seedOperationsFixture(t, ctx, pool)
	repository, err := NewOperationsRepository(pool)
	if err != nil {
		t.Fatalf("NewOperationsRepository() error = %v", err)
	}
	service, err := operations.NewQueryService(repository)
	if err != nil {
		t.Fatalf("NewQueryService() error = %v", err)
	}

	online := true
	devices, err := service.ListDevices(ctx, operations.DeviceFilter{PID: "feeder-x1", Online: &online})
	if err != nil {
		t.Fatalf("ListDevices() error = %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("ListDevices() = %d devices, want 1", len(devices))
	}
	device := devices[0]
	if device.DeviceSN != opsDeviceSN || !device.Online || device.CertificateID != "cert-ops-1" {
		t.Fatalf("device projection = %+v", device)
	}
	if device.BindingProjectID != opsSubjectID || device.BindingExternalUserID != "user-1" || device.BindingActivatedAt == nil {
		t.Fatalf("device binding projection = %+v", device)
	}
	// 过滤条件必须真的生效。
	if offline, err := service.ListDevices(ctx, operations.DeviceFilter{Online: boolPointer(false)}); err != nil || len(offline) != 0 {
		t.Fatalf("ListDevices(online=false) = %d, err = %v; want 0", len(offline), err)
	}
	if matched, err := service.ListDevices(ctx, operations.DeviceFilter{Query: "fdr-000000000000000000000000000000e"}); err != nil || len(matched) != 1 {
		t.Fatalf("ListDevices(prefix) = %d, err = %v; want 1", len(matched), err)
	}
	if missing, err := service.ListDevices(ctx, operations.DeviceFilter{Query: "nope"}); err != nil || len(missing) != 0 {
		t.Fatalf("ListDevices(unknown prefix) = %d, err = %v; want 0", len(missing), err)
	}

	detail, err := service.FindDevice(ctx, opsDeviceSN)
	if err != nil || detail.DeviceSN != opsDeviceSN {
		t.Fatalf("FindDevice() = %+v, err = %v", detail, err)
	}
	if _, err := service.FindDevice(ctx, "fdr-unknown"); err == nil {
		t.Fatal("FindDevice(unknown) succeeded, want a not-found error")
	}

	bindings, err := service.ListBindings(ctx, operations.BindingFilter{ProjectID: opsSubjectID})
	if err != nil || len(bindings) != 1 {
		t.Fatalf("ListBindings() = %d, err = %v; want 1", len(bindings), err)
	}
	if bindings[0].DeviceSN != opsDeviceSN || bindings[0].Status != "active" || bindings[0].PID != "feeder-x1" {
		t.Fatalf("binding projection = %+v", bindings[0])
	}
	if other, err := service.ListBindings(ctx, operations.BindingFilter{ProjectID: "50000000-0000-0000-0000-0000000000ff"}); err != nil || len(other) != 0 {
		t.Fatalf("ListBindings(other project) = %d, err = %v; want 0", len(other), err)
	}

	activations, err := service.ListActivations(ctx, operations.ActivationFilter{Status: "activated"})
	if err != nil || len(activations) != 1 {
		t.Fatalf("ListActivations() = %d, err = %v; want 1", len(activations), err)
	}
	if activations[0].DeviceSN != opsDeviceSN || activations[0].Method != "ble" {
		t.Fatalf("activation projection = %+v", activations[0])
	}

	entries, err := service.ListAuditLogs(ctx, operations.AuditFilter{Operation: "POST developer.projects"})
	if err != nil || len(entries) != 1 {
		t.Fatalf("ListAuditLogs() = %d, err = %v; want 1", len(entries), err)
	}
	if entries[0].ActorLabel != "ops@example.com" || entries[0].Outcome != "succeeded" || entries[0].Metadata == nil {
		t.Fatalf("audit projection = %+v", entries[0])
	}
	// 越界的 operation 前缀不匹配任何行。
	if none, err := service.ListAuditLogs(ctx, operations.AuditFilter{Operation: "DELETE"}); err != nil || len(none) != 0 {
		t.Fatalf("ListAuditLogs(other prefix) = %d, err = %v; want 0", len(none), err)
	}
	future := time.Now().Add(time.Hour)
	if none, err := service.ListAuditLogs(ctx, operations.AuditFilter{From: &future}); err != nil || len(none) != 0 {
		t.Fatalf("ListAuditLogs(future window) = %d, err = %v; want 0", len(none), err)
	}
}

func boolPointer(value bool) *bool { return &value }

func TestOperationsRepositoryProjectsTokensCertificatesAndSubjectFilter(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatalf("ApplyMigrations() error = %v", err)
	}
	seedOperationsFixture(t, ctx, pool)
	if _, err := pool.Exec(ctx, `
		INSERT INTO device_tokens (id, device_binding_id, token_hash, expires_at)
		VALUES ('018f0000-0000-7000-8000-0000000000e5', $1, decode(repeat('cc', 32), 'hex'), now() + interval '1 hour')
	`, opsBindingID); err != nil {
		t.Fatalf("seed device token: %v", err)
	}
	repository, err := NewOperationsRepository(pool)
	if err != nil {
		t.Fatalf("NewOperationsRepository() error = %v", err)
	}
	service, err := operations.NewQueryService(repository)
	if err != nil {
		t.Fatalf("NewQueryService() error = %v", err)
	}

	// 设备列表按 active binding 的主体过滤：平台面主体就是项目 id。
	scoped, err := service.ListDevices(ctx, operations.DeviceFilter{SubjectID: opsSubjectID})
	if err != nil || len(scoped) != 1 || scoped[0].DeviceSN != opsDeviceSN {
		t.Fatalf("ListDevices(subject) = %+v, err = %v", scoped, err)
	}
	if foreign, err := service.ListDevices(ctx, operations.DeviceFilter{
		SubjectID: "50000000-0000-0000-0000-0000000000ff",
	}); err != nil || len(foreign) != 0 {
		t.Fatalf("ListDevices(foreign subject) = %d, err = %v; want 0", len(foreign), err)
	}
	// 未绑定设备不会出现在任何主体过滤下。
	if _, err := pool.Exec(ctx, `
		INSERT INTO device_registry (id, dp_profile_id, device_sn, thing_name, pid, status)
		SELECT '018f0000-0000-7000-8000-0000000000e9', dp_profile_id, 'fdr-unbound-ops', 'fdr-unbound-ops', pid, 'active'
		FROM device_registry WHERE device_sn = $1
	`, opsDeviceSN); err != nil {
		t.Fatalf("seed unbound device: %v", err)
	}
	scoped, err = service.ListDevices(ctx, operations.DeviceFilter{SubjectID: opsSubjectID})
	if err != nil || len(scoped) != 1 {
		t.Fatalf("unbound device leaked into a subject filter: %+v, err = %v", scoped, err)
	}

	tokens, err := service.ListDeviceTokens(ctx, operations.DeviceTokenFilter{DeviceSN: opsDeviceSN})
	if err != nil || len(tokens) != 1 {
		t.Fatalf("ListDeviceTokens() = %d, err = %v; want 1", len(tokens), err)
	}
	if tokens[0].DeviceSN != opsDeviceSN || tokens[0].SubjectID != opsSubjectID || tokens[0].RevokedAt != nil {
		t.Fatalf("token projection = %+v", tokens[0])
	}
	if other, err := service.ListDeviceTokens(ctx, operations.DeviceTokenFilter{DeviceSN: "fdr-unknown"}); err != nil || len(other) != 0 {
		t.Fatalf("ListDeviceTokens(unknown) = %d, err = %v; want 0", len(other), err)
	}

	certificates, err := service.ListCertificates(ctx, operations.CertificateFilter{DeviceSN: opsDeviceSN})
	if err != nil || len(certificates) != 1 {
		t.Fatalf("ListCertificates() = %d, err = %v; want 1", len(certificates), err)
	}
	if certificates[0].CertificateID != "cert-ops-1" || certificates[0].PID != "feeder-x1" {
		t.Fatalf("certificate projection = %+v", certificates[0])
	}
	if revoked, err := service.ListCertificates(ctx, operations.CertificateFilter{Status: "revoked"}); err != nil || len(revoked) != 0 {
		t.Fatalf("ListCertificates(revoked) = %d, err = %v; want 0", len(revoked), err)
	}
}
