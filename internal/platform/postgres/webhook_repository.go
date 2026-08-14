package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/webhooks"
)

type WebhookRepository struct{ pool *pgxpool.Pool }

func NewWebhookRepository(pool *pgxpool.Pool) (*WebhookRepository, error) {
	if pool == nil {
		return nil, fmt.Errorf("PostgreSQL webhook repository requires a pool")
	}
	return &WebhookRepository{pool: pool}, nil
}

func (repository *WebhookRepository) Claim(ctx context.Context) (webhooks.Job, error) {
	var job webhooks.Job
	err := repository.pool.QueryRow(ctx, `
		WITH candidate AS (
			SELECT id FROM webhook_outbox
			WHERE ((status='pending' AND next_attempt_at<=now()) OR (status='queued' AND updated_at<now()-interval '5 minutes'))
			ORDER BY next_attempt_at, created_at
			FOR UPDATE SKIP LOCKED LIMIT 1
		)
		UPDATE webhook_outbox o
		SET status='queued', attempts=o.attempts+1, updated_at=now()
		FROM candidate c WHERE o.id=c.id
		RETURNING o.id::text, o.internal_app_id, o.delivery_id, o.event_type, o.payload::text, o.attempts
	`).Scan(&job.ID, &job.InternalAppID, &job.DeliveryID, &job.EventType, &job.Payload, &job.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return webhooks.Job{}, webhooks.ErrJobNotFound
	}
	if err != nil {
		return webhooks.Job{}, fmt.Errorf("claim webhook job: %w", err)
	}
	return job, nil
}

func (repository *WebhookRepository) Complete(ctx context.Context, job webhooks.Job, statusCode int) error {
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin webhook completion: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	if err := insertWebhookDelivery(ctx, transaction, job, nullableStatusCode(statusCode), ""); err != nil {
		return err
	}
	if _, err := transaction.Exec(ctx, `
		UPDATE webhook_outbox SET status='delivered', updated_at=now() WHERE id=$1
	`, job.ID); err != nil {
		return fmt.Errorf("complete webhook outbox: %w", err)
	}
	return transaction.Commit(ctx)
}

func (repository *WebhookRepository) Retry(ctx context.Context, job webhooks.Job, statusCode int, _ string) error {
	transaction, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin webhook retry: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	errorCode := "delivery_error"
	if statusCode != 0 {
		errorCode = "http_status"
	}
	if err := insertWebhookDelivery(ctx, transaction, job, nullableStatusCode(statusCode), errorCode); err != nil {
		return err
	}
	status := "pending"
	if job.Attempts >= 8 {
		status = "dead"
	}
	delay := time.Duration(1<<min(job.Attempts, 8)) * time.Second
	if _, err := transaction.Exec(ctx, `
		UPDATE webhook_outbox
		SET status=$2, next_attempt_at=now()+$3::interval, updated_at=now()
		WHERE id=$1
	`, job.ID, status, fmt.Sprintf("%f seconds", delay.Seconds())); err != nil {
		return fmt.Errorf("retry webhook outbox: %w", err)
	}
	return transaction.Commit(ctx)
}

func insertWebhookDelivery(
	ctx context.Context,
	transaction pgx.Tx,
	job webhooks.Job,
	statusCode any,
	errorCode string,
) error {
	deliveryID, err := postgresUUID()
	if err != nil {
		return fmt.Errorf("generate webhook delivery id: %w", err)
	}
	var nullableErrorCode any
	if errorCode != "" {
		nullableErrorCode = errorCode
	}
	if _, err := transaction.Exec(ctx, `
		INSERT INTO webhook_deliveries (id, outbox_id, attempt, status_code, error_code)
		VALUES ($1,$2,$3,$4,$5)
	`, deliveryID, job.ID, job.Attempts, statusCode, nullableErrorCode); err != nil {
		return fmt.Errorf("record webhook delivery: %w", err)
	}
	return nil
}

func nullableStatusCode(statusCode int) any {
	if statusCode == 0 {
		return nil
	}
	return statusCode
}
