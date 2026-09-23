package postgres

import (
	"context"
	"strings"
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
	for _, fixture := range []struct {
		eventID, outboxID, deliveryID, delivery string
		status, errorCode                       string
		statusCode                              *int
		attempts                                int
	}{
		{eventID: "038f0000-0000-0000-0000-0000000000e1", outboxID: "018f0000-0000-0000-0000-0000000000e1", deliveryID: "delivery-ops-ok", delivery: "delivery-ops-ok", status: "delivered", statusCode: intPointer(200), attempts: 1},
		{eventID: "038f0000-0000-0000-0000-0000000000e2", outboxID: "018f0000-0000-0000-0000-0000000000e2", deliveryID: "delivery-ops-dead", delivery: "delivery-ops-dead", status: "dead", statusCode: intPointer(503), errorCode: "http_status", attempts: 8},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO device_events (id, device_registry_id, event_type, boot_id, event_seq, data)
			VALUES ($1,$2,$3,$4,$5,'{}'::jsonb)
		`, fixture.eventID, opsDeviceID, "feeding_done", "boot-ops", fixture.attempts); err != nil {
			t.Fatalf("seed webhook event %s: %v", fixture.eventID, err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO webhook_outbox (id, device_event_id, internal_app_id, delivery_id, event_type, payload, status, attempts)
			VALUES ($1,$2,$3,$4,'feeding_done','{}'::jsonb,$5,$6)
		`, fixture.outboxID, fixture.eventID, opsSubjectID, fixture.deliveryID, fixture.status, fixture.attempts); err != nil {
			t.Fatalf("seed webhook outbox %s: %v", fixture.outboxID, err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO webhook_deliveries (id, webhook_outbox_id, attempt, status_code, error_code)
			VALUES ($1,$2,$3,$4,$5)
		`, fixture.delivery, fixture.outboxID, fixture.attempts, fixture.statusCode, nullableString(fixture.errorCode)); err != nil {
			t.Fatalf("seed webhook delivery %s: %v", fixture.delivery, err)
		}
	}
}

func intPointer(value int) *int { return &value }
func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
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
	deliveries, err := service.ListWebhookDeliveries(ctx, operations.WebhookDeliveryFilter{ProjectID: opsSubjectID})
	if err != nil || len(deliveries) != 2 {
		t.Fatalf("ListWebhookDeliveries() = %d, err = %v; want 2", len(deliveries), err)
	}
	if deliveries[0].DeliveryStatus != "undelivered" || deliveries[0].OutboxStatus != "dead" || deliveries[0].StatusCode == nil || *deliveries[0].StatusCode != 503 {
		t.Fatalf("undelivered projection = %+v", deliveries[0])
	}
	if delivered, err := service.ListWebhookDeliveries(ctx, operations.WebhookDeliveryFilter{ProjectID: opsSubjectID, Status: "delivered"}); err != nil || len(delivered) != 1 || delivered[0].DeliveryID != "delivery-ops-ok" {
		t.Fatalf("ListWebhookDeliveries(delivered) = %+v, err = %v", delivered, err)
	}
	if undelivered, err := service.ListWebhookDeliveries(ctx, operations.WebhookDeliveryFilter{ProjectID: opsSubjectID, Status: "undelivered"}); err != nil || len(undelivered) != 1 || undelivered[0].DeliveryID != "delivery-ops-dead" {
		t.Fatalf("ListWebhookDeliveries(undelivered) = %+v, err = %v", undelivered, err)
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

const (
	opsFirmwareID      = "018f0000-0000-7000-8000-0000000000e5"
	opsFirmwareOldID   = "018f0000-0000-7000-8000-0000000000e6"
	opsOTADeploymentID = "018f0000-0000-7000-8000-0000000000e7"
	opsOtherProjectID  = "50000000-0000-0000-0000-0000000000ef"
)

// seedOTAOperationsFixture 追加一版可发布固件、一版上传中的固件和一次成功的下发记录。
func seedOTAOperationsFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	seedOperationsFixture(t, ctx, pool)
	if _, err := pool.Exec(ctx, `
		INSERT INTO ota_firmware_versions (
			id, created_by_internal_app_id, idempotency_key, pid, version, firmware_build,
			sha256, manifest_sha256, file_size, release_statement, signature, artifact_key,
			status, available_at, created_at, updated_at
		) VALUES
		($1,$3,'ops-fw-140','feeder-x1','1.4.0',140,repeat('a',64),repeat('b',64),1048576,
		 'fix feeder jam','sig-140','ota/feeder-x1/1.4.0.bin','available', now() - interval '1 hour',
		 now() - interval '1 hour', now() - interval '1 hour'),
		($2,$3,'ops-fw-130','feeder-x1','1.3.0',130,repeat('c',64),repeat('d',64),1048576,
		 'previous build','sig-130','ota/feeder-x1/1.3.0.bin','uploading', NULL,
		 now() - interval '2 hours', now() - interval '2 hours')
	`, opsFirmwareID, opsFirmwareOldID, opsSubjectID); err != nil {
		t.Fatalf("seed firmware versions: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO device_ota_deployments (
			id, device_registry_id, internal_app_id, firmware_version_id, idempotency_key,
			target_version, target_build, state, completed_at, created_at, updated_at
		) VALUES ($1,$2,$3,$4,'ops-deploy-140','1.4.0',140,'succeeded', now(), now(), now())
	`, opsOTADeploymentID, opsDeviceID, opsSubjectID, opsFirmwareID); err != nil {
		t.Fatalf("seed ota deployment: %v", err)
	}
}

func TestOperationsRepositoryProjectsOTAVersionsAndDeployments(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatalf("ApplyMigrations() error = %v", err)
	}
	seedOTAOperationsFixture(t, ctx, pool)
	repository, err := NewOperationsRepository(pool)
	if err != nil {
		t.Fatalf("NewOperationsRepository() error = %v", err)
	}
	service, err := operations.NewQueryService(repository)
	if err != nil {
		t.Fatalf("NewQueryService() error = %v", err)
	}

	versions, err := service.ListFirmwareVersions(ctx, operations.FirmwareVersionFilter{PID: "feeder-x1"})
	if err != nil || len(versions) != 2 {
		t.Fatalf("ListFirmwareVersions() = %d, err = %v; want 2", len(versions), err)
	}
	// 版本目录按 created_at 倒序：新版本在前，且 available 版本带 available_at。
	if versions[0].Version != "1.4.0" || versions[0].AvailableAt == nil || versions[0].SHA256 != strings.Repeat("a", 64) {
		t.Fatalf("newest firmware projection = %+v", versions[0])
	}
	if versions[0].PublishedBy != opsSubjectID || versions[0].FirmwareBuild != 140 {
		t.Fatalf("firmware publisher projection = %+v", versions[0])
	}
	if versions[1].Version != "1.3.0" || versions[1].AvailableAt != nil || versions[1].Status != "uploading" {
		t.Fatalf("uploading firmware projection = %+v", versions[1])
	}

	uploading, err := service.ListFirmwareVersions(ctx, operations.FirmwareVersionFilter{Status: "uploading"})
	if err != nil || len(uploading) != 1 || uploading[0].Version != "1.3.0" {
		t.Fatalf("ListFirmwareVersions(uploading) = %+v, err = %v", uploading, err)
	}
	if other, err := service.ListFirmwareVersions(ctx, operations.FirmwareVersionFilter{PID: "lamp-x1"}); err != nil || len(other) != 0 {
		t.Fatalf("ListFirmwareVersions(lamp-x1) = %d, err = %v; want 0", len(other), err)
	}

	// 键集游标必须能连续翻页，而不是重复第一页。
	firstPage, err := service.ListFirmwareVersions(ctx, operations.FirmwareVersionFilter{
		PID: "feeder-x1", Page: operations.Page{Limit: 1},
	})
	if err != nil || len(firstPage) != 1 || firstPage[0].Version != "1.4.0" {
		t.Fatalf("first firmware page = %+v, err = %v", firstPage, err)
	}
	secondPage, err := service.ListFirmwareVersions(ctx, operations.FirmwareVersionFilter{
		PID: "feeder-x1",
		Page: operations.Page{Limit: 1, Before: &operations.Cursor{
			CreatedAt: firstPage[0].CreatedAt, ID: firstPage[0].ID,
		}},
	})
	if err != nil || len(secondPage) != 1 || secondPage[0].Version != "1.3.0" {
		t.Fatalf("second firmware page = %+v, err = %v", secondPage, err)
	}

	deployments, err := service.ListOTADeployments(ctx, operations.OTADeploymentFilter{DeviceSN: opsDeviceSN, State: "succeeded"})
	if err != nil || len(deployments) != 1 {
		t.Fatalf("ListOTADeployments() = %d, err = %v; want 1", len(deployments), err)
	}
	if deployments[0].PID != "feeder-x1" || deployments[0].ProjectID != opsSubjectID ||
		deployments[0].TargetVersion != "1.4.0" || deployments[0].CompletedAt == nil {
		t.Fatalf("deployment projection = %+v", deployments[0])
	}
	if none, err := service.ListOTADeployments(ctx, operations.OTADeploymentFilter{ProjectID: opsOtherProjectID}); err != nil || len(none) != 0 {
		t.Fatalf("ListOTADeployments(other project) = %d, err = %v; want 0", len(none), err)
	}
	if running, err := service.ListOTADeployments(ctx, operations.OTADeploymentFilter{State: "installing"}); err != nil || len(running) != 0 {
		t.Fatalf("ListOTADeployments(installing) = %d, err = %v; want 0", len(running), err)
	}
}
