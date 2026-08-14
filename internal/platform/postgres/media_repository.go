package postgres

import (
	"context"
	"errors"
	"fmt"

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
		INSERT INTO snapshots (id, device_id, binding_id, object_key, status, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6)
	`, snapshot.ID, snapshot.DeviceID, snapshot.BindingID, snapshot.ObjectKey, snapshot.Status, snapshot.ExpiresAt); err != nil {
		return fmt.Errorf("create snapshot: %w", err)
	}
	return nil
}

func (repository *MediaRepository) SetSnapshotStatus(ctx context.Context, id, status string) error {
	if _, err := repository.pool.Exec(ctx, `UPDATE snapshots SET status=$2, updated_at=now() WHERE id=$1`, id, status); err != nil {
		return fmt.Errorf("update snapshot status: %w", err)
	}
	return nil
}

func (repository *MediaRepository) GetSnapshot(ctx context.Context, bindingID, deviceSN, id string) (media.Snapshot, error) {
	var snapshot media.Snapshot
	if err := repository.pool.QueryRow(ctx, `
		SELECT s.id::text, s.device_id::text, d.device_sn, s.binding_id::text,
		       COALESCE(s.object_key,''), s.status, s.expires_at
		FROM snapshots s JOIN devices d ON d.id=s.device_id
		WHERE s.id=$1 AND s.binding_id=$2 AND d.device_sn=$3
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
		INSERT INTO recordings (
			id, device_id, binding_id, kind, object_key, status,
			duration_seconds, device_metadata, created_at, updated_at
		) VALUES ($1,$2,$3,'cloud',$4,$5,$6,$7,$8,$8)
	`, recording.ID, recording.DeviceID, recording.BindingID, recording.ObjectKey,
		recording.Status, recording.DurationSeconds, metadata, recording.CreatedAt); err != nil {
		return fmt.Errorf("create recording: %w", err)
	}
	return nil
}

func (repository *MediaRepository) SetRecordingStatus(ctx context.Context, id, status string) error {
	if _, err := repository.pool.Exec(ctx, `UPDATE recordings SET status=$2, updated_at=now() WHERE id=$1`, id, status); err != nil {
		return fmt.Errorf("update recording status: %w", err)
	}
	return nil
}

func (repository *MediaRepository) GetRecording(ctx context.Context, bindingID, deviceSN, id string) (media.Recording, error) {
	return scanRecording(repository.pool.QueryRow(ctx, `
		SELECT r.id::text, r.device_id::text, d.device_sn, r.binding_id::text,
		       COALESCE(r.object_key,''), r.status, COALESCE(r.duration_seconds,0), r.created_at
		FROM recordings r JOIN devices d ON d.id=r.device_id
		WHERE r.id=$1 AND r.binding_id=$2 AND d.device_sn=$3 AND r.kind='cloud'
	`, id, bindingID, deviceSN))
}

func (repository *MediaRepository) ListRecordings(
	ctx context.Context,
	bindingID, deviceSN string,
	limit int,
	cursor, status string,
) ([]media.Recording, string, error) {
	rows, err := repository.pool.Query(ctx, `
		SELECT r.id::text, r.device_id::text, d.device_sn, r.binding_id::text,
		       COALESCE(r.object_key,''), r.status, COALESCE(r.duration_seconds,0), r.created_at
		FROM recordings r JOIN devices d ON d.id=r.device_id
		WHERE r.binding_id=$1 AND d.device_sn=$2 AND r.kind='cloud'
		  AND ($3='' OR r.status=$3)
		  AND ($4='' OR (r.created_at,r.id) < (
			SELECT created_at,id FROM recordings WHERE id=$4::uuid
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
