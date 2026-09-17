package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/portalaccount"
	gatewaypostgres "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/postgres"
	gatewayruntime "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/runtime"
)

// explorex-platform-bootstrap 只做一件事：在还没有任何 staff 账号时创建第一个，
// 并在 stdout 打印一次 setup token。它不读取口令、不写任何长期秘密。
func main() {
	if err := run(context.Background(), os.Getenv); err != nil {
		log.Fatalf("bootstrap platform staff account: %v", err)
	}
}

func run(ctx context.Context, getenv func(string) string) error {
	databaseURL, err := gatewayruntime.LoadDatabaseURL(getenv)
	if err != nil {
		return err
	}
	email := strings.ToLower(strings.TrimSpace(getenv("PLATFORM_BOOTSTRAP_STAFF_EMAIL")))
	if email == "" {
		return fmt.Errorf("PLATFORM_BOOTSTRAP_STAFF_EMAIL is required")
	}
	displayName := strings.TrimSpace(getenv("PLATFORM_BOOTSTRAP_STAFF_NAME"))

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
	repository, err := gatewaypostgres.NewPortalAccountRepository(pool)
	if err != nil {
		return err
	}
	accounts, err := portalaccount.NewService(repository, nil)
	if err != nil {
		return err
	}
	result, err := accounts.BootstrapStaffAccount(ctx, email, displayName)
	if err != nil {
		return err
	}
	fmt.Printf("staff account created: %s (%s)\n", result.Account.Email, result.Account.ID)
	fmt.Printf("setup token (shown once, expires in %s): %s\n", portalaccount.SetupTokenTTL, result.SetupToken)
	return nil
}
