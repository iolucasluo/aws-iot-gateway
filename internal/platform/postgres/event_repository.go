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
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/media"
)

type EventRepository struct{ pool *pgxpool.Pool }

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
	if event.EventType == "recording_finalized" {
		if err := projectLocalRecordingFinalized(ctx, transaction, deviceID, event); err != nil {
			return err
		}
	}
	if event.EventType == "recording_deleted" {
		if err := projectLocalRecordingDeleted(ctx, transaction, deviceID, event); err != nil {
			return err
		}
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

func projectLocalRecordingFinalized(ctx context.Context, transaction pgx.Tx, deviceID string, event events.Event) error {
	payload, err := json.Marshal(event.Data)
	if err != nil {
		return fmt.Errorf("encode recording finalized event: %w", err)
	}
	finalized, err := media.ParseRecordingFinalizedV1(event.ID, payload)
	if err != nil {
		return fmt.Errorf("parse recording finalized event: %w", err)
	}
	startAt, _ := time.Parse(time.RFC3339, finalized.Recording.StartAt)
	endAt, _ := time.Parse(time.RFC3339, finalized.Recording.EndAt)
	var tombstoned bool
	if err := transaction.QueryRow(ctx, `
        SELECT EXISTS(
            SELECT 1 FROM device_local_recording_tombstones
            WHERE device_registry_id=$1 AND recording_id=$2
        )
    `, deviceID, finalized.Recording.RecordingID).Scan(&tombstoned); err != nil {
		return fmt.Errorf("check local recording tombstone: %w", err)
	}
	if tombstoned {
		return nil
	}
	projectionID, err := postgresUUID()
	if err != nil {
		return fmt.Errorf("generate local recording projection id: %w", err)
	}
	var storedID, projectionStatus, thumbnailStatus string
	if err := transaction.QueryRow(ctx, `
        INSERT INTO device_local_recordings (
            id, device_registry_id, source_event_id, recording_id, revision,
            start_at, end_at, recording_date, duration_ms, size_bytes, recording_type, codec,
            width, height, fps_num, fps_den, playable, seekable,
            thumbnail_status, thumbnail_etag, thumbnail_size_bytes
        ) VALUES (
            $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,true,$17,'pending',$18,$19
        )
        ON CONFLICT (device_registry_id, recording_id) DO UPDATE SET
            projection_status=CASE
                WHEN device_local_recordings.projection_status='deleted' THEN 'deleted'
                WHEN device_local_recordings.revision=EXCLUDED.revision
                    THEN device_local_recordings.projection_status
                ELSE 'conflict'
            END,
            conflicting_revision=CASE
                WHEN device_local_recordings.projection_status='deleted'
                    OR device_local_recordings.revision=EXCLUDED.revision
                    THEN device_local_recordings.conflicting_revision
                ELSE EXCLUDED.revision
            END,
            updated_at=now()
        RETURNING id::text, projection_status, thumbnail_status
    `, projectionID, deviceID, event.ID, finalized.Recording.RecordingID,
		finalized.Recording.Revision, startAt, endAt, finalized.Recording.StartAt[:10],
		finalized.Recording.DurationMS, finalized.Recording.SizeBytes, finalized.Recording.Type,
		finalized.Recording.Codec, finalized.Recording.Width, finalized.Recording.Height,
		finalized.Recording.FPSNum, finalized.Recording.FPSDen, finalized.Recording.Seekable,
		finalized.Recording.ThumbnailETag, finalized.Thumbnail.SizeBytes,
	).Scan(&storedID, &projectionStatus, &thumbnailStatus); err != nil {
		return fmt.Errorf("upsert local recording projection: %w", err)
	}
	if projectionStatus != "active" || (thumbnailStatus != "pending" && thumbnailStatus != "failed") {
		return nil
	}
	outboxID, err := postgresUUID()
	if err != nil {
		return fmt.Errorf("generate local recording thumbnail outbox id: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
        INSERT INTO local_recording_thumbnail_outbox (
            id, local_recording_id, status, next_attempt_at
        ) VALUES ($1,$2,'pending',now())
        ON CONFLICT (local_recording_id) DO UPDATE SET
            status=CASE
                WHEN local_recording_thumbnail_outbox.status='ready' THEN 'ready'
                ELSE 'pending'
            END,
            attempts=CASE
                WHEN local_recording_thumbnail_outbox.status='ready' THEN local_recording_thumbnail_outbox.attempts
                ELSE 0
            END,
            next_attempt_at=now(), last_error=NULL, updated_at=now()
    `, outboxID, storedID); err != nil {
		return fmt.Errorf("enqueue local recording thumbnail: %w", err)
	}
	return nil
}

func projectLocalRecordingDeleted(ctx context.Context, transaction pgx.Tx, deviceID string, event events.Event) error {
	payload, err := json.Marshal(event.Data)
	if err != nil {
		return fmt.Errorf("encode recording deleted event: %w", err)
	}
	deleted, err := media.ParseRecordingDeletedV1(payload)
	if err != nil {
		return fmt.Errorf("parse recording deleted event: %w", err)
	}
	tombstoneID, err := postgresUUID()
	if err != nil {
		return fmt.Errorf("generate local recording tombstone id: %w", err)
	}
	deletedAt := event.ReceivedAt
	if event.OccurredAt != nil {
		deletedAt = *event.OccurredAt
	}
	if _, err := transaction.Exec(ctx, `
        INSERT INTO device_local_recording_tombstones (
            id, device_registry_id, source_event_id, recording_id, revision, deleted_at
        ) VALUES ($1,$2,$3,$4,$5,$6)
        ON CONFLICT (device_registry_id, recording_id) DO UPDATE SET
            source_event_id=EXCLUDED.source_event_id,
            revision=EXCLUDED.revision,
            deleted_at=GREATEST(device_local_recording_tombstones.deleted_at, EXCLUDED.deleted_at)
    `, tombstoneID, deviceID, event.ID, deleted.RecordingID, deleted.Revision, deletedAt); err != nil {
		return fmt.Errorf("upsert local recording tombstone: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
        UPDATE device_local_recordings
        SET projection_status='deleted', conflicting_revision=NULL, deleted_at=$3, thumbnail_status='failed', updated_at=now()
        WHERE device_registry_id=$1 AND recording_id=$2 AND projection_status <> 'deleted'
    `, deviceID, deleted.RecordingID, deletedAt); err != nil {
		return fmt.Errorf("delete local recording projection: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
        UPDATE local_recording_thumbnail_outbox outbox
        SET status='delete_pending', attempts=0, next_attempt_at=now(), last_error=NULL, updated_at=now()
        FROM device_local_recordings recording
        WHERE outbox.local_recording_id=recording.id
          AND recording.device_registry_id=$1 AND recording.recording_id=$2
          AND recording.thumbnail_object_key IS NOT NULL
    `, deviceID, deleted.RecordingID); err != nil {
		return fmt.Errorf("enqueue local recording thumbnail deletion: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
        DELETE FROM local_recording_thumbnail_outbox outbox
        USING device_local_recordings recording
        WHERE outbox.local_recording_id=recording.id
          AND recording.device_registry_id=$1 AND recording.recording_id=$2
          AND recording.thumbnail_object_key IS NULL
    `, deviceID, deleted.RecordingID); err != nil {
		return fmt.Errorf("delete empty local recording thumbnail intent: %w", err)
	}
	return nil
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
