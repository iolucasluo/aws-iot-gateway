package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/media"
)

type StreamRepository struct{ pool *pgxpool.Pool }

func NewStreamRepository(pool *pgxpool.Pool) (*StreamRepository, error) {
	if pool == nil {
		return nil, fmt.Errorf("PostgreSQL stream repository requires a pool")
	}
	return &StreamRepository{pool: pool}, nil
}

func (repository *StreamRepository) Create(ctx context.Context, stream media.Stream) error {
	if _, err := repository.pool.Exec(ctx, `
		INSERT INTO device_streams (id, device_registry_id, device_binding_id, kvs_channel_arn, status, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6)
	`, stream.ID, stream.DeviceID, stream.BindingID, stream.ChannelARN, stream.Status, stream.ExpiresAt); err != nil {
		return fmt.Errorf("create KVS stream: %w", err)
	}
	return nil
}

func (repository *StreamRepository) Find(ctx context.Context, bindingID, deviceSN, streamID string) (bool, error) {
	var found bool
	if err := repository.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM device_streams s JOIN device_registry d ON d.id=s.device_registry_id
			WHERE s.id=$1 AND s.device_binding_id=$2 AND d.device_sn=$3 AND s.status IN ('ready','creating')
		)
	`, streamID, bindingID, deviceSN).Scan(&found); err != nil {
		return false, fmt.Errorf("find KVS stream: %w", err)
	}
	return found, nil
}

func (repository *StreamRepository) Close(ctx context.Context, bindingID, deviceSN, streamID string) (bool, error) {
	commandTag, err := repository.pool.Exec(ctx, `
		UPDATE device_streams s SET status='closed', updated_at=now()
		FROM device_registry d
		WHERE s.device_registry_id=d.id AND s.id=$1 AND s.device_binding_id=$2 AND d.device_sn=$3
		  AND s.status IN ('ready','creating')
	`, streamID, bindingID, deviceSN)
	if err != nil {
		return false, fmt.Errorf("close KVS stream: %w", err)
	}
	return commandTag.RowsAffected() == 1, nil
}
