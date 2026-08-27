package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/events"
)

type EventRepository struct{ pool *pgxpool.Pool }

var otaDeploymentIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func NewEventRepository(pool *pgxpool.Pool) (*EventRepository, error) {
	if pool == nil {
		return nil, fmt.Errorf("PostgreSQL event repository requires a pool")
	}
	return &EventRepository{pool: pool}, nil
}

func (repository *EventRepository) UpdatePresence(ctx context.Context, deviceSN string, online bool, observedAt time.Time) error {
	commandTag, err := repository.pool.Exec(ctx, `
		UPDATE device_registry
		SET presence_online=$2,
		    presence_updated_at=$3,
		    last_seen_at=CASE WHEN $2 THEN GREATEST(COALESCE(last_seen_at,$3),$3) ELSE last_seen_at END,
		    updated_at=now()
		WHERE device_sn=$1 AND (
			presence_updated_at IS NULL OR presence_updated_at<$3 OR
			(presence_updated_at=$3 AND presence_online AND NOT $2)
		)
	`, deviceSN, online, observedAt)
	if err != nil {
		return fmt.Errorf("update device presence: %w", err)
	}
	if commandTag.RowsAffected() > 0 {
		return nil
	}
	var exists bool
	if err := repository.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM device_registry WHERE device_sn=$1)`, deviceSN).Scan(&exists); err != nil {
		return fmt.Errorf("check presence device: %w", err)
	}
	if !exists {
		return fmt.Errorf("update presence for unknown device")
	}
	return nil
}

func (repository *EventRepository) Store(ctx context.Context, event events.Event) error {
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin device event ingest: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	var deviceID, internalAppID, pid string
	if err := transaction.QueryRow(ctx, `
		SELECT d.id::text, COALESCE(b.internal_app_id, ''), d.pid
		FROM device_registry d
		LEFT JOIN device_bindings b ON b.device_registry_id=d.id AND b.status='active'
		WHERE d.device_sn=$1
	`, event.DeviceSN).Scan(&deviceID, &internalAppID, &pid); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("ingest event for unknown device")
		}
		return fmt.Errorf("locate event device: %w", err)
	}
	commandTag, err := transaction.Exec(ctx, `
		INSERT INTO device_events (
			id, device_registry_id, event_type, boot_id, event_seq, monotonic_ms,
			time_quality, time_source, occurred_at, received_at, data
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT DO NOTHING
	`, event.ID, deviceID, event.EventType, event.BootID, event.EventSeq, event.MonotonicMS,
		event.TimeQuality, event.TimeSource, event.OccurredAt, event.ReceivedAt, event.Data)
	if err != nil {
		return fmt.Errorf("insert device event: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return transaction.Commit(ctx)
	}
	if event.EventType == "record_clip_done" {
		sessionID, _ := event.Data["session_id"].(string)
		code, codeOK := event.Data["code"].(float64)
		if sessionID != "" && codeOK {
			status := "failed"
			if code == 0 {
				status = "ready"
			}
			if _, err := transaction.Exec(ctx, `
				UPDATE device_recordings SET status=$3, device_metadata=device_metadata || $4::jsonb, updated_at=now()
				WHERE device_registry_id=$1 AND device_metadata->>'session_id'=$2
			`, deviceID, sessionID, status, event.Data); err != nil {
				return fmt.Errorf("apply recording completion event: %w", err)
			}
		}
	}
	if event.EventType == "ota_progress" {
		deploymentID, state, reason, ok := otaProgressTransition(event.Data)
		if ok {
			var reasonValue any
			if reason != "" {
				reasonValue = reason
			}
			if _, err := transaction.Exec(ctx, `
				UPDATE device_ota_deployments
				SET state=$3, failure_reason=$4,
				    completed_at=CASE WHEN $3='failed' THEN now() ELSE NULL END
				WHERE id=$2 AND device_registry_id=$1 AND (
				    ($3='downloading' AND state IN ('pending','accepted','downloading')) OR
				    ($3='verifying' AND state IN ('pending','accepted','downloading','verifying')) OR
				    ($3='installing' AND state IN ('pending','accepted','downloading','verifying','installing')) OR
				    ($3='failed' AND state NOT IN ('succeeded','failed','rolled_back'))
				)
			`, deviceID, deploymentID, state, reasonValue); err != nil {
				return fmt.Errorf("apply OTA progress event: %w", err)
			}
		}
	}
	if event.EventType == "boot" {
		var deploymentID, targetVersion, deploymentState string
		var targetBuild int64
		err := transaction.QueryRow(ctx, `
			SELECT id::text, target_version, target_build, state
			FROM device_ota_deployments
			WHERE device_registry_id=$1
			  AND state IN ('accepted','downloading','verifying','installing')
			FOR UPDATE
		`, deviceID).Scan(&deploymentID, &targetVersion, &targetBuild, &deploymentState)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("locate active OTA deployment for boot: %w", err)
		}
		if err == nil {
			state, ok := otaBootOutcome(event.Data, targetVersion, targetBuild)
			if state == "rolled_back" && deploymentState != "installing" {
				ok = false
			}
			if ok {
				reason := any(nil)
				if state == "rolled_back" {
					reason = "boot_target_mismatch"
				}
				if _, err := transaction.Exec(ctx, `
					UPDATE device_ota_deployments
					SET state=$2, failure_reason=$3, completed_at=now()
					WHERE id=$1 AND state IN ('accepted','downloading','verifying','installing')
				`, deploymentID, state, reason); err != nil {
					return fmt.Errorf("complete OTA deployment from boot: %w", err)
				}
			}
		}
	}
	if internalAppID != "" {
		deliveryHash := sha256.Sum256([]byte(internalAppID + "\n" + event.ID))
		deliveryID := hex.EncodeToString(deliveryHash[:])
		payload, webhookEventType := buildWebhookPayload(deliveryID, event, pid)
		outboxID, err := postgresUUID()
		if err != nil {
			return fmt.Errorf("generate webhook outbox id: %w", err)
		}
		if _, err := transaction.Exec(ctx, `
			INSERT INTO webhook_outbox (
				id, device_event_id, internal_app_id, delivery_id, event_type, payload, status
			) VALUES ($1,$2,$3,$4,$5,$6,'pending')
		`, outboxID, event.ID, internalAppID, deliveryID, webhookEventType, payload); err != nil {
			return fmt.Errorf("create webhook outbox: %w", err)
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit device event ingest: %w", err)
	}
	return nil
}

func otaProgressTransition(data map[string]any) (deploymentID, state, reason string, ok bool) {
	deploymentID, idOK := data["deployment_id"].(string)
	state, stateOK := data["state"].(string)
	if !idOK || !otaDeploymentIDPattern.MatchString(deploymentID) || !stateOK {
		return "", "", "", false
	}
	switch state {
	case "downloading", "verifying", "installing", "failed":
	default:
		return "", "", "", false
	}
	reason, _ = data["reason"].(string)
	if len(reason) > 128 {
		return "", "", "", false
	}
	return deploymentID, state, reason, true
}

func otaBootOutcome(data map[string]any, targetVersion string, targetBuild int64) (string, bool) {
	version, versionOK := data["version"].(string)
	buildValue, buildOK := data["build"].(float64)
	if !versionOK || version == "" || !buildOK || math.IsNaN(buildValue) ||
		math.IsInf(buildValue, 0) || math.Trunc(buildValue) != buildValue ||
		buildValue < 0 || buildValue >= 9223372036854775808.0 {
		return "", false
	}
	if version == targetVersion && int64(buildValue) == targetBuild {
		return "succeeded", true
	}
	return "rolled_back", true
}

func buildWebhookPayload(deliveryID string, event events.Event, pid string) (map[string]any, string) {
	webhookEventType := event.EventType
	webhookData := event.Data
	if event.EventType == "feed_done" {
		webhookEventType = "feeding_done"
		webhookData = map[string]any{
			"portions": event.Data["portions"], "duration_ms": 0, "success": true,
		}
	}
	payload := map[string]any{
		"delivery_id": deliveryID, "event_id": event.ID, "event_type": webhookEventType,
		"device_sn": event.DeviceSN, "pid": pid,
		"ts": event.ReceivedAt.Unix(), "received_at": event.ReceivedAt.UTC().Format(time.RFC3339Nano),
		"boot_id": event.BootID, "event_seq": event.EventSeq, "monotonic_ms": event.MonotonicMS,
		"time_quality": event.TimeQuality, "time_source": event.TimeSource, "data": webhookData,
	}
	if event.OccurredAt != nil {
		payload["occurred_at"] = event.OccurredAt.Unix()
	}
	return payload, webhookEventType
}

func (repository *EventRepository) List(ctx context.Context, deviceID, eventType string, limit int) ([]events.Event, error) {
	rows, err := repository.pool.Query(ctx, `
		SELECT e.id::text, d.device_sn, e.event_type, e.boot_id, e.event_seq,
		       e.monotonic_ms, COALESCE(e.time_quality,''), COALESCE(e.time_source,''),
		       e.occurred_at, e.received_at, e.data
		FROM device_events e JOIN device_registry d ON d.id=e.device_registry_id
		WHERE e.device_registry_id=$1 AND ($2='' OR e.event_type=$2)
		ORDER BY e.received_at DESC, e.id DESC LIMIT $3
	`, deviceID, eventType, limit)
	if err != nil {
		return nil, fmt.Errorf("list device events: %w", err)
	}
	defer rows.Close()
	result := make([]events.Event, 0)
	for rows.Next() {
		var event events.Event
		var data []byte
		if err := rows.Scan(
			&event.ID, &event.DeviceSN, &event.EventType, &event.BootID, &event.EventSeq,
			&event.MonotonicMS, &event.TimeQuality, &event.TimeSource, &event.OccurredAt,
			&event.ReceivedAt, &data,
		); err != nil {
			return nil, fmt.Errorf("scan device event: %w", err)
		}
		if err := json.Unmarshal(data, &event.Data); err != nil {
			return nil, fmt.Errorf("decode device event data: %w", err)
		}
		result = append(result, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate device events: %w", err)
	}
	return result, nil
}
