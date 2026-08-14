package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/events"
)

type EventRepository struct{ pool *pgxpool.Pool }

func NewEventRepository(pool *pgxpool.Pool) (*EventRepository, error) {
	if pool == nil {
		return nil, fmt.Errorf("PostgreSQL event repository requires a pool")
	}
	return &EventRepository{pool: pool}, nil
}

func (repository *EventRepository) Store(ctx context.Context, event events.Event) error {
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin device event ingest: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	var deviceID, internalAppID string
	if err := transaction.QueryRow(ctx, `
		SELECT d.id::text, COALESCE(b.internal_app_id, '')
		FROM devices d
		LEFT JOIN bindings b ON b.device_id=d.id AND b.status='active'
		WHERE d.device_sn=$1
	`, event.DeviceSN).Scan(&deviceID, &internalAppID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("ingest event for unknown device")
		}
		return fmt.Errorf("locate event device: %w", err)
	}
	commandTag, err := transaction.Exec(ctx, `
		INSERT INTO device_events (
			id, device_id, event_type, boot_id, event_seq, monotonic_ms,
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
				UPDATE recordings SET status=$3, device_metadata=device_metadata || $4::jsonb, updated_at=now()
				WHERE device_id=$1 AND device_metadata->>'session_id'=$2
			`, deviceID, sessionID, status, event.Data); err != nil {
				return fmt.Errorf("apply recording completion event: %w", err)
			}
		}
	}
	if internalAppID != "" {
		deliveryHash := sha256.Sum256([]byte(internalAppID + "\n" + event.ID))
		deliveryID := hex.EncodeToString(deliveryHash[:])
		payload, webhookEventType := buildWebhookPayload(internalAppID, deliveryID, event)
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

func buildWebhookPayload(internalAppID, deliveryID string, event events.Event) (map[string]any, string) {
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
		"device_sn": event.DeviceSN, "pid": "feeder-x1", "project_id": internalAppID,
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
		FROM device_events e JOIN devices d ON d.id=e.device_id
		WHERE e.device_id=$1 AND ($2='' OR e.event_type=$2)
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
