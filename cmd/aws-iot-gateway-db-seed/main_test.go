package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestValidateEnvironmentAllowsOnlyNonProduction(t *testing.T) {
	t.Parallel()

	for _, environment := range []string{"development", "staging"} {
		if err := validateEnvironment(environment); err != nil {
			t.Errorf("validateEnvironment(%q) error = %v", environment, err)
		}
	}
	for _, environment := range []string{"", "production", "test"} {
		if err := validateEnvironment(environment); err == nil {
			t.Errorf("validateEnvironment(%q) error = nil, want rejection", environment)
		}
	}
}

func TestRunRejectsProductionBeforeReadingDatabaseConfiguration(t *testing.T) {
	t.Parallel()

	var requested []string
	err := run(context.Background(), func(key string) string {
		requested = append(requested, key)
		if key == "GATEWAY_ENVIRONMENT" {
			return "production"
		}
		return "must-not-be-read"
	})
	if err == nil || !strings.Contains(err.Error(), "must be development or staging") {
		t.Fatalf("run(production) error = %v, want environment rejection", err)
	}
	if want := []string{"GATEWAY_ENVIRONMENT"}; !reflect.DeepEqual(requested, want) {
		t.Fatalf("run(production) requested environment keys = %v, want %v", requested, want)
	}
}

func TestRunMigratesThenSeedsProductCatalogIdempotently(t *testing.T) {
	ctx := context.Background()
	databaseURL, pool := newIsolatedSeedRunnerDatabase(t, ctx)
	environment := map[string]string{
		"GATEWAY_ENVIRONMENT": "development",
		"DATABASE_URL":        databaseURL,
	}
	getenv := func(key string) string { return environment[key] }

	for attempt := 1; attempt <= 2; attempt++ {
		if err := run(ctx, getenv); err != nil {
			t.Fatalf("run() attempt %d error = %v", attempt, err)
		}
	}

	rows, err := pool.Query(ctx, `SELECT pid FROM products WHERE status='active' ORDER BY pid`)
	if err != nil {
		t.Fatalf("list seeded products: %v", err)
	}
	defer rows.Close()
	var products []string
	for rows.Next() {
		var pid string
		if err := rows.Scan(&pid); err != nil {
			t.Fatalf("scan seeded product: %v", err)
		}
		products = append(products, pid)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate seeded products: %v", err)
	}
	want := []string{"feeder-x1", "minion-cam-x1"}
	if !reflect.DeepEqual(products, want) {
		t.Fatalf("seeded products after repeated runner execution = %v, want %v", products, want)
	}
}

func newIsolatedSeedRunnerDatabase(t *testing.T, ctx context.Context) (string, *pgxpool.Pool) {
	t.Helper()
	databaseURL := os.Getenv("TEST_POSTGRES_URL")
	if databaseURL == "" {
		t.Skip("TEST_POSTGRES_URL is not configured")
	}
	base, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect base PostgreSQL: %v", err)
	}
	schema := fmt.Sprintf("gateway_seed_runner_test_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := base.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		base.Close()
		t.Fatalf("create isolated schema: %v", err)
	}

	parsedURL, err := url.Parse(databaseURL)
	if err != nil {
		_, _ = base.Exec(ctx, "DROP SCHEMA "+identifier+" CASCADE")
		base.Close()
		t.Fatalf("parse PostgreSQL URL: %v", err)
	}
	query := parsedURL.Query()
	query.Set("search_path", schema)
	parsedURL.RawQuery = query.Encode()
	isolatedDatabaseURL := parsedURL.String()
	config, err := pgxpool.ParseConfig(isolatedDatabaseURL)
	if err != nil {
		_, _ = base.Exec(ctx, "DROP SCHEMA "+identifier+" CASCADE")
		base.Close()
		t.Fatalf("parse PostgreSQL URL: %v", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		_, _ = base.Exec(ctx, "DROP SCHEMA "+identifier+" CASCADE")
		base.Close()
		t.Fatalf("connect isolated schema: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = base.Exec(ctx, "DROP SCHEMA "+identifier+" CASCADE")
		base.Close()
	})
	return isolatedDatabaseURL, pool
}
