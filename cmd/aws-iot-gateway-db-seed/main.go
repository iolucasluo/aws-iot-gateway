package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	gatewaypostgres "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/postgres"
	gatewayruntime "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/runtime"
)

func main() {
	if err := run(context.Background(), os.Getenv); err != nil {
		log.Fatalf("seed Gateway database: %v", err)
	}
	log.Print("Gateway database seeds applied")
}

func run(ctx context.Context, getenv func(string) string) error {
	environment := strings.TrimSpace(getenv("GATEWAY_ENVIRONMENT"))
	if err := validateEnvironment(environment); err != nil {
		return err
	}
	databaseURL, err := gatewayruntime.LoadDatabaseURL(getenv)
	if err != nil {
		return err
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("configure PostgreSQL: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("connect PostgreSQL: %w", err)
	}
	if err := gatewaypostgres.ApplyMigrations(ctx, pool); err != nil {
		return fmt.Errorf("migrate PostgreSQL: %w", err)
	}
	if environment == "production" {
		return gatewaypostgres.ApplyContractSeeds(ctx, pool)
	}
	return gatewaypostgres.ApplySeeds(ctx, pool)
}

func validateEnvironment(environment string) error {
	switch strings.TrimSpace(environment) {
	case "development", "staging", "production":
		return nil
	default:
		return fmt.Errorf("GATEWAY_ENVIRONMENT must be development, staging or production for database seeds")
	}
}
