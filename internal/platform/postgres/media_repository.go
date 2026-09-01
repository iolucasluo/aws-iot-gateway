package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/media"
)

type MediaRepository struct{ pool *pgxpool.Pool }

func NewMediaRepository(pool *pgxpool.Pool) (*MediaRepository, error) {
	if pool == nil {
		return nil, fmt.Errorf("PostgreSQL media repository requires a pool")
	}
	return &MediaRepository{pool: pool}, nil
}

func (repository *MediaRepository) CreateSnapshot(ctx context.Context, snapshot media.Snapshot) error {
	if _, err := repository.pool.Exec(ctx, `
		INSERT INTO device_snapshots (id, device_registry_id, device_binding_id, object_key, status, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6)
	`, snapshot.ID, snapshot.DeviceID, snapshot.BindingID, snapshot.ObjectKey, snapshot.Status, snapshot.ExpiresAt); err != nil {
		return fmt.Errorf("create snapshot: %w", err)
	}
	return nil
}

func (repository *MediaRepository) SetSnapshotStatus(ctx context.Context, id, status string) error {
	if _, err := repository.pool.Exec(ctx, `UPDATE device_snapshots SET status=$2, updated_at=now() WHERE id=$1`, id, status); err != nil {
		return fmt.Errorf("update snapshot status: %w", err)
	}
	return nil
}

func (repository *MediaRepository) TransitionSnapshotStatus(ctx context.Context, id, from, to string) (bool, error) {
	result, err := repository.pool.Exec(ctx, `
		UPDATE device_snapshots SET status=$3, updated_at=now() WHERE id=$1 AND status=$2
	`, id, from, to)
	if err != nil {
		return false, fmt.Errorf("transition snapshot status: %w", err)
	}
	return result.RowsAffected() == 1, nil
}

func (repository *MediaRepository) GetSnapshot(ctx context.Context, bindingID, deviceSN, id string) (media.Snapshot, error) {
	var snapshot media.Snapshot
	if err := repository.pool.QueryRow(ctx, `
		SELECT s.id::text, s.device_registry_id::text, d.device_sn, s.device_binding_id::text,
		       COALESCE(s.object_key,''), s.status, s.expires_at
		FROM device_snapshots s JOIN device_registry d ON d.id=s.device_registry_id
		WHERE s.id=$1 AND s.device_binding_id=$2 AND d.device_sn=$3
	`, id, bindingID, deviceSN).Scan(
		&snapshot.ID, &snapshot.DeviceID, &snapshot.DeviceSN, &snapshot.BindingID,
		&snapshot.ObjectKey, &snapshot.Status, &snapshot.ExpiresAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return media.Snapshot{}, media.ErrSnapshotNotFound
		}
		return media.Snapshot{}, fmt.Errorf("get snapshot: %w", err)
	}
	return snapshot, nil
}

func (repository *MediaRepository) CreateRecording(ctx context.Context, recording media.Recording) error {
	metadata := map[string]any{"session_id": recording.ID}
	if _, err := repository.pool.Exec(ctx, `
		INSERT INTO device_recordings (
			id, device_registry_id, device_binding_id, kind, object_key, status,
			duration_seconds, device_metadata, created_at, updated_at
		) VALUES ($1,$2,$3,'cloud',$4,$5,$6,$7,$8,$8)
	`, recording.ID, recording.DeviceID, recording.BindingID, recording.ObjectKey,
		recording.Status, recording.DurationSeconds, metadata, recording.CreatedAt); err != nil {
		return fmt.Errorf("create recording: %w", err)
	}
	return nil
}

func (repository *MediaRepository) SetRecordingStatus(ctx context.Context, id, status string) error {
	if _, err := repository.pool.Exec(ctx, `UPDATE device_recordings SET status=$2, updated_at=now() WHERE id=$1`, id, status); err != nil {
		return fmt.Errorf("update recording status: %w", err)
	}
	return nil
}

func (repository *MediaRepository) GetRecording(ctx context.Context, bindingID, deviceSN, id string) (media.Recording, error) {
	return scanRecording(repository.pool.QueryRow(ctx, `
		SELECT r.id::text, r.device_registry_id::text, d.device_sn, r.device_binding_id::text,
		       COALESCE(r.object_key,''), r.status, COALESCE(r.duration_seconds,0), r.created_at
		FROM device_recordings r JOIN device_registry d ON d.id=r.device_registry_id
		WHERE r.id=$1 AND r.device_binding_id=$2 AND d.device_sn=$3 AND r.kind='cloud'
	`, id, bindingID, deviceSN))
}

func (repository *MediaRepository) ListRecordings(
	ctx context.Context,
	bindingID, deviceSN string,
	limit int,
	cursor, status string,
) ([]media.Recording, string, error) {
	rows, err := repository.pool.Query(ctx, `
		SELECT r.id::text, r.device_registry_id::text, d.device_sn, r.device_binding_id::text,
		       COALESCE(r.object_key,''), r.status, COALESCE(r.duration_seconds,0), r.created_at
		FROM device_recordings r JOIN device_registry d ON d.id=r.device_registry_id
		WHERE r.device_binding_id=$1 AND d.device_sn=$2 AND r.kind='cloud'
		  AND ($3='' OR r.status=$3)
		  AND ($4='' OR (r.created_at,r.id) < (
			SELECT created_at,id FROM device_recordings WHERE id=$4::uuid
		  ))
		ORDER BY r.created_at DESC, r.id DESC LIMIT $5
	`, bindingID, deviceSN, status, cursor, limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("list recordings: %w", err)
	}
	defer rows.Close()
	items := make([]media.Recording, 0, limit+1)
	for rows.Next() {
		item, err := scanRecording(rows)
		if err != nil {
			return nil, "", err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("iterate recordings: %w", err)
	}
	nextCursor := ""
	if len(items) > limit {
		nextCursor = items[limit-1].ID
		items = items[:limit]
	}
	return items, nextCursor, nil
}

type recordingRow interface{ Scan(...any) error }

func scanRecording(row recordingRow) (media.Recording, error) {
	var recording media.Recording
	if err := row.Scan(
		&recording.ID, &recording.DeviceID, &recording.DeviceSN, &recording.BindingID,
		&recording.ObjectKey, &recording.Status, &recording.DurationSeconds, &recording.CreatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return media.Recording{}, media.ErrRecordingNotFound
		}
		return media.Recording{}, fmt.Errorf("scan recording: %w", err)
	}
	return recording, nil
}

func (repository *MediaRepository) ListLocalRecordingProjections(
	ctx context.Context,
	bindingID, deviceSN string,
	limit int,
	cursor, dateFilter, typeFilter string,
) ([]media.LocalRecordingProjection, string, error) {
	if limit <= 0 || limit > 100 || bindingID == "" || deviceSN == "" {
		return nil, "", media.ErrInvalidInput
	}
	if dateFilter != "" {
		if _, err := time.Parse("2006-01-02", dateFilter); err != nil {
			return nil, "", media.ErrInvalidInput
		}
	}
	if typeFilter != "" && typeFilter != "event" && typeFilter != "manual" && typeFilter != "continuous" {
		return nil, "", media.ErrInvalidInput
	}
	var deviceID string
	if err := repository.pool.QueryRow(ctx, `
		SELECT b.device_registry_id::text
		FROM device_bindings b JOIN device_registry d ON d.id=b.device_registry_id
		WHERE b.id=$1 AND b.status='active' AND d.device_sn=$2
	`, bindingID, deviceSN).Scan(&deviceID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", media.ErrInvalidInput
		}
		return nil, "", fmt.Errorf("locate local recording owner: %w", err)
	}
	var afterStart *time.Time
	var afterRecordingID string
	if cursor != "" {
		var cursorDate, cursorType string
		var cursorStart time.Time
		if err := repository.pool.QueryRow(ctx, `
			SELECT after_start_at, after_recording_id,
			       COALESCE(date_filter::text,''), COALESCE(type_filter,'')
			FROM local_recording_cursors
			WHERE token=$1::uuid AND device_binding_id=$2 AND device_registry_id=$3
			  AND expires_at>now()
		`, cursor, bindingID, deviceID).Scan(
			&cursorStart, &afterRecordingID, &cursorDate, &cursorType,
		); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, "", media.ErrInvalidInput
			}
			return nil, "", fmt.Errorf("resolve local recording cursor: %w", err)
		}
		if (dateFilter != "" && dateFilter != cursorDate) ||
			(typeFilter != "" && typeFilter != cursorType) {
			return nil, "", media.ErrInvalidInput
		}
		dateFilter = cursorDate
		typeFilter = cursorType
		afterStart = &cursorStart
	}
	rows, err := repository.pool.Query(ctx, `
		SELECT recording.id::text, recording.device_registry_id::text,
		       recording.recording_id, recording.revision,
		       recording.start_at, recording.end_at, recording.duration_ms,
		       recording.size_bytes, recording.recording_type, recording.codec,
		       recording.width, recording.height, recording.fps_num, recording.fps_den,
		       recording.playable, recording.seekable,
		       recording.thumbnail_status, recording.thumbnail_etag,
		       COALESCE(recording.thumbnail_object_key,'')
		FROM device_local_recordings recording
		WHERE recording.device_registry_id=$1 AND recording.projection_status='active'
		  AND (NULLIF($2,'') IS NULL OR recording.recording_date=NULLIF($2,'')::date)
		  AND ($3='' OR recording.recording_type=$3)
		  AND ($4::timestamptz IS NULL OR
		       (recording.start_at,recording.recording_id)<($4, $5))
		ORDER BY recording.start_at DESC, recording.recording_id DESC
		LIMIT $6
	`, deviceID, dateFilter, typeFilter, afterStart, afterRecordingID, limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("list local recording projections: %w", err)
	}
	defer rows.Close()
	items := make([]media.LocalRecordingProjection, 0, limit+1)
	for rows.Next() {
		var item media.LocalRecordingProjection
		var startAt, endAt time.Time
		if err := rows.Scan(
			&item.LocalRecordingListItem.InternalID, &item.DeviceID,
			&item.RecordingID, &item.Revision, &startAt, &endAt, &item.DurationMS,
			&item.SizeBytes, &item.Type, &item.Codec, &item.Width, &item.Height,
			&item.FPSNum, &item.FPSDen, &item.Playable, &item.Seekable,
			&item.Thumbnail.Status, &item.Thumbnail.ETag, &item.ThumbnailObjectKey,
		); err != nil {
			return nil, "", fmt.Errorf("scan local recording projection: %w", err)
		}
		item.StartAt = startAt.Format(time.RFC3339)
		item.EndAt = endAt.Format(time.RFC3339)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("iterate local recording projections: %w", err)
	}
	nextCursor := ""
	if len(items) > limit {
		last := items[limit-1]
		cursorID, err := postgresUUID()
		if err != nil {
			return nil, "", fmt.Errorf("generate local recording cursor: %w", err)
		}
		lastStart, _ := time.Parse(time.RFC3339, last.StartAt)
		if _, err := repository.pool.Exec(ctx, `
			INSERT INTO local_recording_cursors (
				token, device_binding_id, device_registry_id, after_start_at,
				after_recording_id, date_filter, type_filter, expires_at
			) VALUES ($1,$2,$3,$4,$5,NULLIF($6,'')::date,NULLIF($7,''),now()+interval '15 minutes')
		`, cursorID, bindingID, deviceID, lastStart, last.RecordingID, dateFilter, typeFilter); err != nil {
			return nil, "", fmt.Errorf("create local recording cursor: %w", err)
		}
		nextCursor = cursorID
		items = items[:limit]
	}
	_, _ = repository.pool.Exec(ctx, `DELETE FROM local_recording_cursors WHERE expires_at<=now()`)
	return items, nextCursor, nil
}

func (repository *MediaRepository) ClaimLocalThumbnail(ctx context.Context) (media.LocalThumbnailTask, error) {
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return media.LocalThumbnailTask{}, fmt.Errorf("begin local thumbnail claim: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	_, _ = transaction.Exec(ctx, `
		UPDATE local_recording_thumbnail_outbox outbox
		SET status='delete_pending', attempts=0, next_attempt_at=now(),
		    last_error='binding released; delete private thumbnail', updated_at=now()
		FROM device_local_recordings recording
		WHERE outbox.local_recording_id=recording.id
		  AND recording.thumbnail_object_key IS NOT NULL
		  AND outbox.status NOT IN ('delete_pending','delete_processing','deleted')
		  AND NOT EXISTS (
			SELECT 1 FROM device_bindings binding
			WHERE binding.device_registry_id=recording.device_registry_id AND binding.status='active'
		  )
	`)
	_, _ = transaction.Exec(ctx, `
		UPDATE local_recording_thumbnail_outbox outbox
		SET status='dead', last_error='device is not actively bound', updated_at=now()
		FROM device_local_recordings recording
		WHERE outbox.local_recording_id=recording.id
		  AND recording.thumbnail_object_key IS NULL
		  AND outbox.status IN ('pending','processing')
		  AND NOT EXISTS (
			SELECT 1 FROM device_bindings binding
			WHERE binding.device_registry_id=recording.device_registry_id AND binding.status='active'
		  )
	`)
	var task media.LocalThumbnailTask
	var outboxStatus string
	if err := transaction.QueryRow(ctx, `
		SELECT outbox.id::text, outbox.status, recording.recording_id, recording.revision,
		       recording.thumbnail_etag, recording.thumbnail_size_bytes,
		       recording.device_registry_id::text, device.device_sn,
		       COALESCE(binding.internal_app_id,''), outbox.attempts,
		       COALESCE(recording.thumbnail_object_key,'')
		FROM local_recording_thumbnail_outbox outbox
		JOIN device_local_recordings recording ON recording.id=outbox.local_recording_id
		JOIN device_registry device ON device.id=recording.device_registry_id
		LEFT JOIN device_bindings binding ON binding.device_registry_id=recording.device_registry_id
		  AND binding.status='active'
		WHERE outbox.next_attempt_at<=now() AND (
			(outbox.status IN ('pending','processing') AND binding.id IS NOT NULL
			 AND recording.projection_status='active'
			 AND recording.thumbnail_status IN ('pending','failed','uploading'))
			OR
			(outbox.status IN ('delete_pending','delete_processing')
			 AND recording.thumbnail_object_key IS NOT NULL)
		)
		ORDER BY outbox.next_attempt_at, outbox.id
		FOR UPDATE OF outbox SKIP LOCKED LIMIT 1
	`).Scan(
		&task.OutboxID, &outboxStatus, &task.RecordingID, &task.Revision, &task.ETag,
		&task.SizeBytes, &task.DeviceID, &task.DeviceSN, &task.InternalAppID, &task.Attempts,
		&task.ObjectKey,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return media.LocalThumbnailTask{}, media.ErrLocalThumbnailTaskNotFound
		}
		return media.LocalThumbnailTask{}, fmt.Errorf("claim local thumbnail: %w", err)
	}
	task.Operation = "upload"
	processingStatus := "processing"
	if outboxStatus == "delete_pending" || outboxStatus == "delete_processing" {
		task.Operation = "delete"
		processingStatus = "delete_processing"
	}
	task.Attempts++
	if _, err := transaction.Exec(ctx, `
		UPDATE local_recording_thumbnail_outbox
		SET status=$3, attempts=$2, next_attempt_at=now()+interval '2 minutes', updated_at=now()
		WHERE id=$1
	`, task.OutboxID, task.Attempts, processingStatus); err != nil {
		return media.LocalThumbnailTask{}, fmt.Errorf("mark local thumbnail processing: %w", err)
	}
	if task.Operation == "upload" {
		if _, err := transaction.Exec(ctx, `
			UPDATE device_local_recordings SET thumbnail_status='uploading', updated_at=now()
			WHERE device_registry_id=$1 AND recording_id=$2 AND projection_status='active'
		`, task.DeviceID, task.RecordingID); err != nil {
			return media.LocalThumbnailTask{}, fmt.Errorf("mark local thumbnail uploading: %w", err)
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return media.LocalThumbnailTask{}, fmt.Errorf("commit local thumbnail claim: %w", err)
	}
	return task, nil
}

func (repository *MediaRepository) CompleteLocalThumbnail(
	ctx context.Context, outboxID, recordingID, objectKey string,
) error {
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin local thumbnail completion: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	result, err := transaction.Exec(ctx, `
		UPDATE device_local_recordings recording
		SET thumbnail_status='ready', thumbnail_object_key=$3, updated_at=now()
		FROM local_recording_thumbnail_outbox outbox
		WHERE outbox.id=$1 AND outbox.local_recording_id=recording.id
		  AND recording.recording_id=$2 AND recording.projection_status='active'
	`, outboxID, recordingID, objectKey)
	if err != nil {
		return fmt.Errorf("complete local thumbnail projection: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("complete local thumbnail projection: recording is unavailable")
	}
	if _, err := transaction.Exec(ctx, `
		UPDATE local_recording_thumbnail_outbox
		SET status='ready', last_error=NULL, updated_at=now() WHERE id=$1
	`, outboxID); err != nil {
		return fmt.Errorf("complete local thumbnail outbox: %w", err)
	}
	return transaction.Commit(ctx)
}

func (repository *MediaRepository) CompleteLocalThumbnailDeletion(
	ctx context.Context, outboxID, recordingID string,
) error {
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin local thumbnail deletion completion: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	result, err := transaction.Exec(ctx, `
		UPDATE device_local_recordings recording
		SET thumbnail_object_key=NULL, thumbnail_status='failed', updated_at=now()
		FROM local_recording_thumbnail_outbox outbox
		WHERE outbox.id=$1 AND outbox.local_recording_id=recording.id
		  AND recording.recording_id=$2
	`, outboxID, recordingID)
	if err != nil {
		return fmt.Errorf("clear deleted local thumbnail object key: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("clear deleted local thumbnail object key: recording is unavailable")
	}
	if _, err := transaction.Exec(ctx, `
		UPDATE local_recording_thumbnail_outbox
		SET status='deleted', last_error=NULL, updated_at=now() WHERE id=$1
	`, outboxID); err != nil {
		return fmt.Errorf("complete local thumbnail deletion outbox: %w", err)
	}
	return transaction.Commit(ctx)
}

func (repository *MediaRepository) RetryLocalThumbnail(
	ctx context.Context, outboxID, message string, attempts int,
) error {
	if len(message) > 1000 {
		message = message[:1000]
	}
	dead := attempts >= 5
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin local thumbnail retry: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	if _, err := transaction.Exec(ctx, `
		UPDATE local_recording_thumbnail_outbox
		SET status=CASE
		        WHEN $2 THEN 'dead'
		        WHEN status='delete_processing' THEN 'delete_pending'
		        ELSE 'pending'
		    END,
		    last_error=$3,
		    next_attempt_at=CASE WHEN $2 THEN next_attempt_at
		        ELSE now()+LEAST(interval '5 minutes', interval '5 seconds' * power(2, LEAST(attempts,6))) END,
		    updated_at=now()
		WHERE id=$1
	`, outboxID, dead, message); err != nil {
		return fmt.Errorf("retry local thumbnail outbox: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
		UPDATE device_local_recordings recording
		SET thumbnail_status=CASE
		        WHEN outbox.status IN ('delete_pending','delete_processing','deleted') THEN 'failed'
		        WHEN $2 THEN 'failed'
		        ELSE 'pending'
		    END,
		    updated_at=now()
		FROM local_recording_thumbnail_outbox outbox
		WHERE outbox.id=$1 AND outbox.local_recording_id=recording.id
	`, outboxID, dead); err != nil {
		return fmt.Errorf("retry local thumbnail projection: %w", err)
	}
	return transaction.Commit(ctx)
}

func (repository *MediaRepository) ClaimLocalReconciliation(ctx context.Context) (media.LocalReconciliationTask, error) {
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return media.LocalReconciliationTask{}, fmt.Errorf("begin local reconciliation claim: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	generation, err := postgresUUID()
	if err != nil {
		return media.LocalReconciliationTask{}, fmt.Errorf("generate reconciliation generation: %w", err)
	}
	_, _ = transaction.Exec(ctx, `
		INSERT INTO local_recording_reconciliations (
			device_registry_id, generation, status, next_attempt_at
		)
		SELECT device.id, $1, 'pending', now()
		FROM device_registry device
		WHERE EXISTS (
			SELECT 1 FROM device_bindings binding
			WHERE binding.device_registry_id=device.id AND binding.status='active'
		) AND NOT EXISTS (
			SELECT 1 FROM local_recording_reconciliations reconciliation
			WHERE reconciliation.device_registry_id=device.id
		)
		ORDER BY device.id LIMIT 1
		ON CONFLICT DO NOTHING
	`, generation)
	var task media.LocalReconciliationTask
	if err := transaction.QueryRow(ctx, `
		SELECT reconciliation.device_registry_id::text, device.device_sn,
		       binding.internal_app_id, reconciliation.generation::text,
		       COALESCE(reconciliation.cursor,'')
		FROM local_recording_reconciliations reconciliation
		JOIN device_registry device ON device.id=reconciliation.device_registry_id
		JOIN device_bindings binding ON binding.device_registry_id=device.id AND binding.status='active'
		WHERE reconciliation.next_attempt_at<=now()
		ORDER BY reconciliation.next_attempt_at, reconciliation.device_registry_id
		FOR UPDATE OF reconciliation SKIP LOCKED LIMIT 1
	`).Scan(
		&task.DeviceID, &task.DeviceSN, &task.InternalAppID, &task.Generation, &task.Cursor,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return media.LocalReconciliationTask{}, media.ErrLocalReconciliationTaskNotFound
		}
		return media.LocalReconciliationTask{}, fmt.Errorf("claim local reconciliation: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
		UPDATE local_recording_reconciliations
		SET status='processing', next_attempt_at=now()+interval '2 minutes', updated_at=now()
		WHERE device_registry_id=$1
	`, task.DeviceID); err != nil {
		return media.LocalReconciliationTask{}, fmt.Errorf("mark local reconciliation processing: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return media.LocalReconciliationTask{}, fmt.Errorf("commit local reconciliation claim: %w", err)
	}
	return task, nil
}

func (repository *MediaRepository) ApplyLocalReconciliationPage(
	ctx context.Context, task media.LocalReconciliationTask, page media.DeviceCatalogPage,
) error {
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin local reconciliation page: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	for index := range page.Items {
		item := page.Items[index]
		var tombstoned bool
		if err := transaction.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM device_local_recording_tombstones
				WHERE device_registry_id=$1 AND recording_id=$2
			)
		`, task.DeviceID, item.RecordingID).Scan(&tombstoned); err != nil {
			return fmt.Errorf("check reconciled recording tombstone: %w", err)
		}
		if !tombstoned {
			projectionID, err := postgresUUID()
			if err != nil {
				return fmt.Errorf("generate reconciled projection id: %w", err)
			}
			startAt, _ := time.Parse(time.RFC3339, item.StartAt)
			endAt, _ := time.Parse(time.RFC3339, item.EndAt)
			var storedID, projectionStatus, thumbnailStatus string
			if err := transaction.QueryRow(ctx, `
				INSERT INTO device_local_recordings (
					id, device_registry_id, source_event_id, recording_id, revision,
					start_at, end_at, recording_date, duration_ms, size_bytes,
					recording_type, codec, width, height, fps_num, fps_den,
					playable, seekable, thumbnail_status, thumbnail_etag, thumbnail_size_bytes
				) VALUES (
					$1,$2,NULL,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,
					true,true,'pending',$16,$17
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
			`, projectionID, task.DeviceID, item.RecordingID, item.Revision, startAt, endAt,
				item.StartAt[:10], item.DurationMS, item.SizeBytes, item.Type, item.Codec,
				item.Width, item.Height, item.FPSNum, item.FPSDen, item.Thumbnail.ETag,
				item.Thumbnail.SizeBytes,
			).Scan(&storedID, &projectionStatus, &thumbnailStatus); err != nil {
				return fmt.Errorf("upsert reconciled local recording: %w", err)
			}
			if projectionStatus == "active" && (thumbnailStatus == "pending" || thumbnailStatus == "failed") {
				outboxID, err := postgresUUID()
				if err != nil {
					return fmt.Errorf("generate reconciled thumbnail outbox id: %w", err)
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
					return fmt.Errorf("enqueue reconciled thumbnail: %w", err)
				}
			}
		}
		if _, err := transaction.Exec(ctx, `
			INSERT INTO local_recording_reconciliation_sightings (
				device_registry_id, generation, recording_id
			) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING
		`, task.DeviceID, task.Generation, item.RecordingID); err != nil {
			return fmt.Errorf("record local reconciliation sighting: %w", err)
		}
	}
	if page.NextCursor != nil {
		if _, err := transaction.Exec(ctx, `
			UPDATE local_recording_reconciliations
			SET cursor=$3, status='pending', next_attempt_at=now(), last_error=NULL, updated_at=now()
			WHERE device_registry_id=$1 AND generation=$2
		`, task.DeviceID, task.Generation, *page.NextCursor); err != nil {
			return fmt.Errorf("advance local reconciliation cursor: %w", err)
		}
	} else {
		if _, err := transaction.Exec(ctx, `
			UPDATE device_local_recordings recording
			SET projection_status='deleted', conflicting_revision=NULL, deleted_at=now(), thumbnail_status='failed', updated_at=now()
			WHERE recording.device_registry_id=$1 AND recording.projection_status='active'
			  AND recording.created_at <= (
				SELECT reconciliation.started_at
				FROM local_recording_reconciliations reconciliation
				WHERE reconciliation.device_registry_id=$1 AND reconciliation.generation=$2
			  )
			  AND NOT EXISTS (
				SELECT 1 FROM local_recording_reconciliation_sightings sighting
				WHERE sighting.device_registry_id=$1 AND sighting.generation=$2
				  AND sighting.recording_id=recording.recording_id
			  )
		`, task.DeviceID, task.Generation); err != nil {
			return fmt.Errorf("hide missing local recording projections: %w", err)
		}
		if _, err := transaction.Exec(ctx, `
			UPDATE local_recording_thumbnail_outbox outbox
			SET status='delete_pending', attempts=0, next_attempt_at=now(), last_error=NULL, updated_at=now()
			FROM device_local_recordings recording
			WHERE outbox.local_recording_id=recording.id
			  AND recording.device_registry_id=$1 AND recording.projection_status='deleted'
			  AND recording.thumbnail_object_key IS NOT NULL
		`, task.DeviceID); err != nil {
			return fmt.Errorf("enqueue missing recording thumbnail deletions: %w", err)
		}
		if _, err := transaction.Exec(ctx, `
			DELETE FROM local_recording_thumbnail_outbox outbox
			USING device_local_recordings recording
			WHERE outbox.local_recording_id=recording.id
			  AND recording.device_registry_id=$1 AND recording.projection_status='deleted'
			  AND recording.thumbnail_object_key IS NULL
		`, task.DeviceID); err != nil {
			return fmt.Errorf("delete empty missing recording thumbnail intents: %w", err)
		}
		if _, err := transaction.Exec(ctx, `
			DELETE FROM local_recording_reconciliation_sightings
			WHERE device_registry_id=$1 AND generation=$2
		`, task.DeviceID, task.Generation); err != nil {
			return fmt.Errorf("clear local reconciliation sightings: %w", err)
		}
		nextGeneration, err := postgresUUID()
		if err != nil {
			return fmt.Errorf("generate next reconciliation generation: %w", err)
		}
		if _, err := transaction.Exec(ctx, `
			UPDATE local_recording_reconciliations
			SET generation=$2, started_at=now(), cursor=NULL, status='pending',
			    next_attempt_at=now()+interval '15 minutes', last_error=NULL, updated_at=now()
			WHERE device_registry_id=$1
		`, task.DeviceID, nextGeneration); err != nil {
			return fmt.Errorf("complete local reconciliation generation: %w", err)
		}
	}
	return transaction.Commit(ctx)
}

func (repository *MediaRepository) RetryLocalReconciliation(
	ctx context.Context, deviceID, message string,
) error {
	if len(message) > 1000 {
		message = message[:1000]
	}
	nextGeneration, err := postgresUUID()
	if err != nil {
		return fmt.Errorf("generate retry reconciliation generation: %w", err)
	}
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin local reconciliation retry: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	if _, err := transaction.Exec(ctx, `
		DELETE FROM local_recording_reconciliation_sightings
		WHERE device_registry_id=$1
	`, deviceID); err != nil {
		return fmt.Errorf("clear failed reconciliation sightings: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
		UPDATE local_recording_reconciliations
		SET generation=$2, started_at=now(), cursor=NULL, status='pending',
		    next_attempt_at=now()+interval '30 seconds', last_error=$3, updated_at=now()
		WHERE device_registry_id=$1
	`, deviceID, nextGeneration, message); err != nil {
		return fmt.Errorf("retry local reconciliation: %w", err)
	}
	return transaction.Commit(ctx)
}

func (repository *MediaRepository) GetLocalRecordingThumbnail(
	ctx context.Context, bindingID, deviceSN, recordingID string,
) (string, string, error) {
	var objectKey, etag string
	if err := repository.pool.QueryRow(ctx, `
		SELECT recording.thumbnail_object_key, recording.thumbnail_etag
		FROM device_local_recordings recording
		JOIN device_registry device ON device.id=recording.device_registry_id
		JOIN device_bindings binding ON binding.device_registry_id=device.id
		WHERE binding.id=$1 AND binding.status='active' AND device.device_sn=$2
		  AND recording.recording_id=$3 AND recording.projection_status='active'
		  AND recording.thumbnail_status='ready' AND recording.thumbnail_object_key IS NOT NULL
	`, bindingID, deviceSN, recordingID).Scan(&objectKey, &etag); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", media.ErrRecordingNotFound
		}
		return "", "", fmt.Errorf("get local recording thumbnail: %w", err)
	}
	return objectKey, etag, nil
}
