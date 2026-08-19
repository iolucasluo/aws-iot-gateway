package postgres

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/db/seeds"
)

const seedAdvisoryLockID int64 = 7_114_920_042_026_081_301

// ApplySeeds explicitly applies every embedded non-production seed in filename order.
func ApplySeeds(ctx context.Context, pool *pgxpool.Pool) error {
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
	entries, err := fs.ReadDir(seeds.Files, ".")
	if err != nil {
		return fmt.Errorf("read embedded seeds: %w", err)
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].Name() < entries[right].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		seedSQL, err := seeds.Files.ReadFile(entry.Name())
		if err != nil {
			return fmt.Errorf("read seed %s: %w", entry.Name(), err)
		}
		if _, err := transaction.Exec(ctx, string(seedSQL)); err != nil {
			return fmt.Errorf("apply seed %s: %w", entry.Name(), err)
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit database seeds: %w", err)
	}
	return nil
}
