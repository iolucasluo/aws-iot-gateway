package postgres

import (
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/events"
)

func TestWebhookPayloadPreservesLivPetUnixSecondContract(t *testing.T) {
	t.Parallel()
	occurredAt := time.Date(2026, 8, 13, 10, 11, 12, 987_000_000, time.UTC)
	receivedAt := time.Date(2026, 8, 13, 10, 11, 15, 123_000_000, time.UTC)
	payload, eventType := buildWebhookPayload("delivery-1", events.Event{
		ID: "event-1", DeviceSN: "fdr-1", EventType: "feed_done",
		OccurredAt: &occurredAt, ReceivedAt: receivedAt,
		Data: map[string]any{"portions": float64(2), "trigger": "app"},
	}, "minion-cam-x1")
	if eventType != "feeding_done" || payload["event_type"] != "feeding_done" {
		t.Fatalf("webhook event type = %q payload=%#v", eventType, payload)
	}
	data, ok := payload["data"].(map[string]any)
	if !ok || data["portions"] != float64(2) || data["duration_ms"] != 0 || data["success"] != true || len(data) != 3 {
		t.Fatalf("feeding_done data = %#v", payload["data"])
	}
	if payload["occurred_at"] != occurredAt.Unix() || payload["ts"] != receivedAt.Unix() {
		t.Fatalf("webhook timestamps = occurred_at=%#v ts=%#v", payload["occurred_at"], payload["ts"])
	}
	if _, found := payload["project_id"]; found {
		t.Fatalf("webhook payload must not expose platform project metadata: %#v", payload)
	}
	if payload["pid"] != "minion-cam-x1" {
		t.Fatalf("webhook compatibility fields = %#v", payload)
	}
}

func TestWebhookPayloadOmitsUnknownDeviceOccurrenceButKeepsDeliveryTime(t *testing.T) {
	t.Parallel()
	receivedAt := time.Date(2026, 8, 13, 10, 11, 15, 0, time.UTC)
	payload, eventType := buildWebhookPayload("delivery-1", events.Event{
		ID: "event-1", DeviceSN: "fdr-1", EventType: "motion_detected",
		ReceivedAt: receivedAt, Data: map[string]any{},
	}, "feeder-x1")
	if eventType != "motion_detected" {
		t.Fatalf("webhook event type = %q", eventType)
	}
	if _, found := payload["occurred_at"]; found {
		t.Fatalf("webhook must omit unknown occurred_at: %#v", payload)
	}
	if payload["ts"] != receivedAt.Unix() {
		t.Fatalf("webhook ts = %#v", payload["ts"])
	}
}
