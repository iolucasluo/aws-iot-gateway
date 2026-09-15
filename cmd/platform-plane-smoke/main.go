// Command platform-plane-smoke 起一个最小的 gateway 内部 API（openapi HMAC 面），
// 只服务于平台面本地端到端验证（test/tool/platform_plane_e2e.sh）：
//
//   - 复用真实的 internalauth 验签与 DB 凭证来源（project.Service 作为 CredentialStore）；
//   - 复用真实的项目 scope（device-products 收窄）与 OTA 只读目录；
//   - 不连接 AWS 与国内 provider，因此不需要任何云凭据。
//
// 它不是可部署组件：Dockerfile 不构建它，默认只监听回环地址。
//
// 模式（-mode）：
//
//	serve     默认；起 HTTP 面
//	prepare   建隔离 schema（SMOKE_SCHEMA）+ 应用迁移后退出
//	seed-ota  向隔离 schema 写一版 available 固件（SMOKE_OTA_PID / SMOKE_OTA_PROJECT）
//	drop      删除 SMOKE_SCHEMA（CASCADE）
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	redisclient "github.com/redis/go-redis/v9"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/activation"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/httpapi"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/internalauth"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/ota"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/project"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/credentialcipher"
	gatewaypostgres "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/postgres"
	redisstore "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/redis"
	gatewayruntime "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/runtime"
)

var schemaPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,40}$`)

func main() {
	mode := flag.String("mode", "serve", "serve | prepare | seed-ota | drop")
	flag.Parse()
	if err := run(context.Background(), os.Getenv, *mode); err != nil {
		log.Fatalf("platform plane smoke gateway: %v", err)
	}
}

func run(ctx context.Context, getenv func(string) string, mode string) error {
	databaseURL, err := gatewayruntime.LoadDatabaseURL(getenv)
	if err != nil {
		return err
	}
	schema := strings.TrimSpace(getenv("SMOKE_SCHEMA"))
	if schema != "" && !schemaPattern.MatchString(schema) {
		return fmt.Errorf("SMOKE_SCHEMA must match %s", schemaPattern)
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("configure PostgreSQL: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("connect PostgreSQL: %w", err)
	}
	if schema != "" {
		if _, err := pool.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+pgx.Identifier{schema}.Sanitize()); err != nil {
			return fmt.Errorf("create isolated schema: %w", err)
		}
	}

	switch mode {
	case "drop":
		if schema == "" {
			return fmt.Errorf("-mode=drop requires SMOKE_SCHEMA")
		}
		if _, err := pool.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
			return fmt.Errorf("drop isolated schema: %w", err)
		}
		return nil
	case "prepare":
		return gatewaypostgres.ApplyMigrations(ctx, pool)
	case "seed-ota":
		return seedOTAFirmwareVersion(ctx, pool, getenv)
	case "serve":
	default:
		return fmt.Errorf("unknown mode %q", mode)
	}
	if err := gatewaypostgres.ApplyMigrations(ctx, pool); err != nil {
		return fmt.Errorf("migrate PostgreSQL: %w", err)
	}
	credentialCipher, err := credentialcipher.NewFromBase64(
		getenv("PLATFORM_CREDENTIAL_KEK"), valueOr(getenv("PLATFORM_CREDENTIAL_KEK_ID"), "kek-v1"),
	)
	if err != nil {
		return fmt.Errorf("configure credential cipher: %w", err)
	}
	redisURL := strings.TrimSpace(getenv("REDIS_URL"))
	if redisURL == "" {
		return fmt.Errorf("REDIS_URL is required")
	}
	redisOptions, err := redisclient.ParseURL(redisURL)
	if err != nil {
		return fmt.Errorf("configure Redis: %w", err)
	}
	redis := redisclient.NewClient(redisOptions)
	defer redis.Close()
	if err := redis.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("connect Redis: %w", err)
	}
	environment := valueOr(getenv("GATEWAY_ENVIRONMENT"), "development")
	nonceStore, err := redisstore.NewNonceStore(redis, environment)
	if err != nil {
		return err
	}
	projectRepository, err := gatewaypostgres.NewProjectRepository(pool)
	if err != nil {
		return err
	}
	projectService, err := project.NewService(projectRepository, credentialCipher, time.Now)
	if err != nil {
		return err
	}
	// 静态 INTERNAL_APPS_JSON 主体刻意不注册：本入口只验证 DB 凭证来源。
	verifier, err := internalauth.NewVerifier(nil, nonceStore, time.Now, projectService)
	if err != nil {
		return err
	}
	otaRepository, err := gatewaypostgres.NewOTARepository(pool)
	if err != nil {
		return err
	}
	// artifacts/commands 为空：本入口只提供只读目录，写路径由真实 gateway 负责。
	otaService := ota.NewService(otaRepository, nil, nil, environment, nil, nil, time.Now)

	handler := httpapi.NewInternalAPI(activation.NewService(), verifier, httpapi.Dependencies{
		OTA: otaService, ProjectScope: projectService,
	})
	listenAddress := valueOr(getenv("SMOKE_LISTEN_ADDRESS"), "127.0.0.1:14080")
	server := &http.Server{Addr: listenAddress, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	shutdownSignal, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-shutdownSignal.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	log.Printf("platform-plane-smoke listening on %s schema=%s", listenAddress, valueOr(schema, "(default search_path)"))
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// seedOTAFirmwareVersion 写一版 available 固件，让 e2e 的 OTA 目录断言拿到非空结果。
func seedOTAFirmwareVersion(ctx context.Context, pool *pgxpool.Pool, getenv func(string) string) error {
	pid := strings.TrimSpace(getenv("SMOKE_OTA_PID"))
	projectID := strings.TrimSpace(getenv("SMOKE_OTA_PROJECT"))
	version := valueOr(getenv("SMOKE_OTA_VERSION"), "9.9.9")
	if pid == "" || projectID == "" {
		return fmt.Errorf("-mode=seed-ota requires SMOKE_OTA_PID and SMOKE_OTA_PROJECT")
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO ota_firmware_versions (
			id, created_by_internal_app_id, idempotency_key, pid, version, firmware_build,
			sha256, manifest_sha256, file_size, release_statement, signature, artifact_key,
			status, available_at
		) VALUES (
			gen_random_uuid(), $1, $2, $3, $4, $5,
			repeat('a', 64), repeat('b', 64), 1048576, 'platform plane e2e fixture', 'e2e-signature',
			'ota/' || $6 || '/' || $7 || '.bin', 'available', now()
		)
		ON CONFLICT (pid, version) DO NOTHING
	`, projectID, "platform-plane-e2e-"+pid+"-"+version, pid, version, 999, pid, version)
	if err != nil {
		return fmt.Errorf("seed ota firmware version: %w", err)
	}
	return nil
}

func valueOr(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}
