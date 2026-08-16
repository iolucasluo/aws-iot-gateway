package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/device"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/events"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/media"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/webhooks"
)

func TestRuntimeRepositoriesPersistCommandMediaEventAndWebhookOutbox(t *testing.T) {
	ctx := context.Background()
	pool := newIsolatedMigrationPool(t, ctx)
	if err := ApplyMigrations(ctx, pool); err != nil {
		t.Fatalf("ApplyMigrations() error = %v", err)
	}
	const (
		deviceID  = "038f0000-0000-7000-8000-000000000001"
		bindingID = "038f0000-0000-7000-8000-000000000002"
		deviceSN  = "fdr-20000000000000000000000000000001"
	)
	if _, err := pool.Exec(ctx, `
		INSERT INTO device_registry (id, device_sn, thing_name, pid, certificate_id, status)
		VALUES ($1,$2,$2,'feeder-x1','certificate-runtime-test','active')
	`, deviceID, deviceSN); err != nil {
		t.Fatalf("insert device: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO device_bindings (id, device_registry_id, internal_app_id, binding_external_user_id, status)
		VALUES ($1,$2,'livpet-staging','runtime-user','active')
	`, bindingID, deviceID); err != nil {
		t.Fatalf("insert binding: %v", err)
	}

	commandRepository, _ := NewCommandRepository(pool)
	command := device.Command{
		ID: "038f0000-0000-7000-8000-000000000010", DeviceID: deviceID,
		InternalAppID: "livpet-staging", SessionID: "session-runtime-test",
		Payload: map[string]any{"action": "feed", "session_id": "session-runtime-test"},
		Status:  "pending", CreatedAt: time.Now().UTC(),
	}
	if err := commandRepository.Create(ctx, command); err != nil {
		t.Fatalf("Create command: %v", err)
	}
	if err := commandRepository.Complete(ctx, command.ID, "acked", map[string]any{"code": 0}, ""); err != nil {
		t.Fatalf("Complete command: %v", err)
	}

	mediaRepository, _ := NewMediaRepository(pool)
	recordingID := "038f0000-0000-7000-8000-000000000020"
	if err := mediaRepository.CreateRecording(ctx, media.Recording{
		ID: recordingID, DeviceID: deviceID, DeviceSN: deviceSN, BindingID: bindingID,
		ObjectKey: "staging/recordings/device/recording.h264", Status: "pending",
		DurationSeconds: 30, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateRecording(): %v", err)
	}
	snapshotID := "038f0000-0000-7000-8000-000000000021"
	if err := mediaRepository.CreateSnapshot(ctx, media.Snapshot{
		ID: snapshotID, DeviceID: deviceID, DeviceSN: deviceSN, BindingID: bindingID,
		ObjectKey: "staging/snapshots/device/snapshot.h264", Status: "uploading", ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("CreateSnapshot(): %v", err)
	}
	if _, err := mediaRepository.GetSnapshot(ctx, bindingID, deviceSN, snapshotID); err != nil {
		t.Fatalf("GetSnapshot(): %v", err)
	}

	eventRepository, _ := NewEventRepository(pool)
	statusAt := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	offlineAt := statusAt.Add(time.Minute)
	if err := eventRepository.UpdatePresence(ctx, deviceSN, true, statusAt); err != nil {
		t.Fatalf("store online presence: %v", err)
	}
	if err := eventRepository.UpdatePresence(ctx, deviceSN, false, offlineAt); err != nil {
		t.Fatalf("store offline presence: %v", err)
	}
	if err := eventRepository.UpdatePresence(ctx, deviceSN, true, statusAt.Add(-time.Minute)); err != nil {
		t.Fatalf("ignore reordered presence: %v", err)
	}
	if err := eventRepository.UpdatePresence(ctx, deviceSN, true, offlineAt); err != nil {
		t.Fatalf("ignore same-timestamp heartbeat after LWT: %v", err)
	}
	var presenceOnline bool
	var presenceUpdatedAt, lastSeenAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT presence_online, presence_updated_at, last_seen_at FROM device_registry WHERE id=$1
	`, deviceID).Scan(&presenceOnline, &presenceUpdatedAt, &lastSeenAt); err != nil {
		t.Fatalf("read device presence: %v", err)
	}
	if presenceOnline || !presenceUpdatedAt.Equal(offlineAt) || !lastSeenAt.Equal(statusAt) {
		t.Fatalf("presence = online:%t updated:%s last_seen:%s", presenceOnline, presenceUpdatedAt, lastSeenAt)
	}
	if err := eventRepository.UpdatePresence(ctx, "fdr-unknown", true, statusAt); err == nil {
		t.Fatal("UpdatePresence() accepted an unknown device")
	}
	occurredAt := time.Now().UTC()
	if err := eventRepository.Store(ctx, events.Event{
		ID: "038f0000-0000-7000-8000-000000000030", DeviceSN: deviceSN,
		EventType: "record_clip_done", BootID: "5ce0b7f2-1f0a-4ed7-9f06-bf6ce8393148",
		EventSeq: 1, OccurredAt: &occurredAt, ReceivedAt: time.Now().UTC(),
		Data: map[string]any{"session_id": recordingID, "code": float64(0), "file_size": float64(1024)},
	}); err != nil {
		t.Fatalf("Store event: %v", err)
	}
	recording, err := mediaRepository.GetRecording(ctx, bindingID, deviceSN, recordingID)
	if err != nil || recording.Status != "ready" {
		t.Fatalf("recording after event = %#v, %v", recording, err)
	}

	var payload []byte
	if err := pool.QueryRow(ctx, `SELECT payload FROM webhook_outbox WHERE device_event_id=$1`, "038f0000-0000-7000-8000-000000000030").Scan(&payload); err != nil {
		t.Fatalf("read webhook outbox: %v", err)
	}
	var webhook map[string]any
	if err := json.Unmarshal(payload, &webhook); err != nil || webhook["project_id"] != "livpet-staging" || webhook["pid"] != "feeder-x1" {
		t.Fatalf("webhook payload = %#v, %v", webhook, err)
	}
	webhookRepository, err := NewWebhookRepository(pool)
	if err != nil {
		t.Fatalf("NewWebhookRepository(): %v", err)
	}
	job, err := webhookRepository.Claim(ctx)
	if err != nil {
		t.Fatalf("Claim webhook: %v", err)
	}
	if job.InternalAppID != "livpet-staging" || job.EventType != "record_clip_done" || string(job.Payload) != string(payload) {
		t.Fatalf("claimed webhook = %#v", job)
	}
	if err := webhookRepository.Complete(ctx, job, 204); err != nil {
		t.Fatalf("Complete webhook: %v", err)
	}
	var outboxStatus string
	var deliveryStatus int
	if err := pool.QueryRow(ctx, `
		SELECT o.status, d.status_code
		FROM webhook_outbox o JOIN webhook_deliveries d ON d.webhook_outbox_id=o.id
		WHERE o.id=$1
	`, job.ID).Scan(&outboxStatus, &deliveryStatus); err != nil {
		t.Fatalf("read webhook delivery: %v", err)
	}
	if outboxStatus != "delivered" || deliveryStatus != 204 {
		t.Fatalf("webhook state = %q/%d", outboxStatus, deliveryStatus)
	}
	var _ webhooks.Repository = webhookRepository
}
