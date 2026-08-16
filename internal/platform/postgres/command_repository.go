package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/device"
)

type CommandRepository struct{ pool *pgxpool.Pool }

func NewCommandRepository(pool *pgxpool.Pool) (*CommandRepository, error) {
	if pool == nil {
		return nil, fmt.Errorf("PostgreSQL command repository requires a pool")
	}
	return &CommandRepository{pool: pool}, nil
}

func (repository *CommandRepository) Create(ctx context.Context, command device.Command) error {
	var requestID any
	if command.RequestID != "" {
		requestID = command.RequestID
	}
	if _, err := repository.pool.Exec(ctx, `
		INSERT INTO device_commands (
			id, device_registry_id, internal_app_id, session_id, request_id, command, status, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
	`, command.ID, command.DeviceID, command.InternalAppID, command.SessionID, requestID,
		command.Payload, command.Status, command.CreatedAt); err != nil {
		return fmt.Errorf("create device command: %w", err)
	}
	return nil
}

func (repository *CommandRepository) Complete(
	ctx context.Context,
	id, status string,
	ack map[string]any,
	errorCode string,
) error {
	var errorValue any
	if errorCode != "" {
		errorValue = errorCode
	}
	commandTag, err := repository.pool.Exec(ctx, `
		UPDATE device_commands
		SET status=$2::varchar, ack=$3, error_code=$4,
		    completed_at=CASE WHEN $2::varchar IN ('acked','failed','timed_out') THEN now() ELSE NULL END
		WHERE id=$1
	`, id, status, ack, errorValue)
	if err != nil {
		return fmt.Errorf("complete device command: %w", err)
	}
	if commandTag.RowsAffected() != 1 {
		return fmt.Errorf("complete device command: command not found")
	}
	return nil
}
