package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrLifecycleJobNotFound = errors.New("device lifecycle job not found")

type LifecycleJob struct {
	ID            string
	CertificateID string
	Attempts      int
}

type LifecycleRepository struct{ pool *pgxpool.Pool }

func NewLifecycleRepository(pool *pgxpool.Pool) (*LifecycleRepository, error) {
	if pool == nil {
		return nil, fmt.Errorf("PostgreSQL lifecycle repository requires a pool")
	}
	return &LifecycleRepository{pool: pool}, nil
}

func (repository *LifecycleRepository) Claim(ctx context.Context) (LifecycleJob, error) {
	var job LifecycleJob
	err := repository.pool.QueryRow(ctx, `
		WITH candidate AS (
			SELECT id FROM device_lifecycle_outbox
			WHERE ((status='pending' AND next_attempt_at<=now()) OR (status='processing' AND updated_at<now()-interval '5 minutes'))
			ORDER BY next_attempt_at, created_at
			FOR UPDATE SKIP LOCKED LIMIT 1
		)
		UPDATE device_lifecycle_outbox o
		SET status='processing', attempts=o.attempts+1, updated_at=now()
		FROM candidate c WHERE o.id=c.id
		RETURNING o.id::text, o.payload->>'certificate_id', o.attempts
	`).Scan(&job.ID, &job.CertificateID, &job.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return LifecycleJob{}, ErrLifecycleJobNotFound
	}
	if err != nil {
		return LifecycleJob{}, fmt.Errorf("claim device lifecycle job: %w", err)
	}
	return job, nil
}

func (repository *LifecycleRepository) Complete(ctx context.Context, id string) error {
	_, err := repository.pool.Exec(ctx, `
		UPDATE device_lifecycle_outbox SET status='done', last_error=NULL, updated_at=now() WHERE id=$1
	`, id)
	return err
}

func (repository *LifecycleRepository) Retry(ctx context.Context, id, lastError string, attempts int) error {
	status := "pending"
	if attempts >= 8 {
		status = "dead"
	}
	if len(lastError) > 255 {
		lastError = lastError[:255]
	}
	delay := time.Duration(1<<min(attempts, 8)) * time.Second
	_, err := repository.pool.Exec(ctx, `
		UPDATE device_lifecycle_outbox
		SET status=$2, last_error=$3, next_attempt_at=now()+$4::interval, updated_at=now()
		WHERE id=$1
	`, id, status, lastError, fmt.Sprintf("%f seconds", delay.Seconds()))
	return err
}
