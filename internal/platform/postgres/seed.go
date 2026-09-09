package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/db/seeds"
)

const seedAdvisoryLockID int64 = 7_114_920_042_026_081_301

// ApplySeeds applies demo products and runtime contracts for development/staging.
func ApplySeeds(ctx context.Context, pool *pgxpool.Pool) error {
	return applySeeds(ctx, pool, []string{"001_products.sql", "002_dp_profiles.sql"})
}

// ApplyContractSeeds publishes only runtime contracts, including in production.
// It never loads demo products.
func ApplyContractSeeds(ctx context.Context, pool *pgxpool.Pool) error {
	return applySeeds(ctx, pool, []string{"002_dp_profiles.sql"})
}

func applySeeds(ctx context.Context, pool *pgxpool.Pool, names []string) error {
	if pool == nil {
		return fmt.Errorf("PostgreSQL pool is required")
	}
	transaction, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin seed transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	if _, err := transaction.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, seedAdvisoryLockID); err != nil {
		return fmt.Errorf("lock database seeds: %w", err)
	}
	for _, name := range names {
		seedSQL, err := seeds.Files.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read seed %s: %w", name, err)
		}
		if _, err := transaction.Exec(ctx, string(seedSQL)); err != nil {
			return fmt.Errorf("apply seed %s: %w", name, err)
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit database seeds: %w", err)
	}
	return nil
}
