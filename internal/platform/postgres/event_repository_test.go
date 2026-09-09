package postgres

import (
	"os"
	"strings"
	"testing"
	"time"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/events"
)

func TestOTAStateSQLAcceptsEarlyProgressWithoutRegressingOnACK(t *testing.T) {
	t.Parallel()
	eventSource, err := os.ReadFile("event_repository.go")
	if err != nil {
		t.Fatal(err)
	}
	repositorySource, err := os.ReadFile("ota_repository.go")
	if err != nil {
		t.Fatal(err)
	}
	progressSQL := string(eventSource)
	ackSQL := string(repositorySource)
	for _, expected := range []string{
		"$3='downloading' AND state IN ('pending','accepted','downloading')",
		"$3='verifying' AND state IN ('pending','accepted','downloading','verifying')",
		"$3='installing' AND state IN ('pending','accepted','downloading','verifying','installing')",
	} {
		if !strings.Contains(progressSQL, expected) {
			t.Fatalf("OTA progress SQL misses forward-only early-event transition %q", expected)
		}
	}
	if !strings.Contains(ackSQL, "SET state=CASE WHEN $2='accepted' AND state <> 'pending' THEN state ELSE $2 END") {
		t.Fatal("OTA ACK SQL must preserve a state already advanced by an early progress event")
	}
}

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

func TestOTAProgressAcceptsOnlyDeploymentScopedLifecycleStates(t *testing.T) {
	t.Parallel()
	deploymentID, state, reason, ok := otaProgressTransition(map[string]any{
		"deployment_id": "11111111-1111-4111-8111-111111111111", "state": "verifying", "reason": "",
	})
	if !ok || deploymentID != "11111111-1111-4111-8111-111111111111" || state != "verifying" || reason != "" {
		t.Fatalf("transition = %q %q %q %v", deploymentID, state, reason, ok)
	}
	for _, data := range []map[string]any{
		{"state": "verifying"},
		{"deployment_id": "11111111-1111-4111-8111-111111111111", "state": "succeeded"},
		{"deployment_id": "11111111-1111-4111-8111-111111111111", "state": "campaigning"},
	} {
		if _, _, _, ok := otaProgressTransition(data); ok {
			t.Fatalf("accepted invalid OTA progress: %#v", data)
		}
	}
}

func TestOTABootOutcomeRequiresExactTargetVersionAndBuild(t *testing.T) {
	t.Parallel()
	if state, ok := otaBootOutcome(map[string]any{"version": "v1.2.3", "build": float64(42)}, "v1.2.3", 42); !ok || state != "succeeded" {
		t.Fatalf("matching boot outcome = %q %v", state, ok)
	}
	if state, ok := otaBootOutcome(map[string]any{"version": "v1.2.2", "build": float64(41)}, "v1.2.3", 42); !ok || state != "rolled_back" {
		t.Fatalf("mismatched boot outcome = %q %v", state, ok)
	}
	for _, data := range []map[string]any{
		{"version": "v1.2.3"},
		{"version": "v1.2.3", "build": 42.5},
	} {
		if _, ok := otaBootOutcome(data, "v1.2.3", 42); ok {
			t.Fatalf("accepted unprovable boot: %#v", data)
		}
	}
}
