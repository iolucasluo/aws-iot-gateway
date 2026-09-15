package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	redisclient "github.com/redis/go-redis/v9"

	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/portalaccount"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/project"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/credentialcipher"
	gatewaypostgres "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/postgres"
	redisstore "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/redis"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platformapi"
	gatewayruntime "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/runtime"
)

func main() {
	if err := run(context.Background(), os.Getenv); err != nil {
		log.Fatalf("run platform api: %v", err)
	}
}

func run(ctx context.Context, getenv func(string) string) error {
	config, err := gatewayruntime.LoadPlatformConfig(getenv)
	if err != nil {
		return err
	}
	pool, err := pgxpool.New(ctx, config.DatabaseURL)
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
	accounts, err := portalaccount.NewService(repository, time.Now)
	if err != nil {
		return err
	}
	// 项目/产品/凭证服务：只有配置了 KEK 才启用，避免在缺少主密钥时静默降级。
	var projects *project.Service
	if config.CredentialKEK != "" {
		credentialCipher, err := credentialcipher.NewFromBase64(config.CredentialKEK, config.CredentialKEKID)
		if err != nil {
			return fmt.Errorf("configure credential cipher: %w", err)
		}
		projectRepository, err := gatewaypostgres.NewProjectRepository(pool)
		if err != nil {
			return err
		}
		projects, err = project.NewService(projectRepository, credentialCipher, time.Now)
		if err != nil {
			return err
		}
	}
	redisOptions, err := redisclient.ParseURL(config.RedisURL)
	if err != nil {
		return fmt.Errorf("configure Redis: %w", err)
	}
	redis := redisclient.NewClient(redisOptions)
	defer redis.Close()
	if err := redis.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("connect Redis: %w", err)
	}
	limiter, err := redisstore.NewRateLimiter(redis, config.Environment)
	if err != nil {
		return err
	}
	handler, err := platformapi.New(accounts, projects, limiter, platformapi.Options{
		AllowedOrigins:     config.CORSAllowedOrigins,
		LoginRateLimit:     config.LoginRateLimit,
		LoginRateWindow:    config.LoginRateWindow,
		TrustedProxyHeader: config.TrustedProxyHeader,
	})
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              config.ListenAddress,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	shutdownContext, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	failure := make(chan error, 1)
	go func() {
		log.Printf("platform api listening on %s (%s)", config.ListenAddress, config.Environment)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			failure <- err
		}
	}()
	select {
	case err := <-failure:
		return fmt.Errorf("serve platform api: %w", err)
	case <-shutdownContext.Done():
	}
	stopContext, cancel := context.WithTimeout(context.Background(), config.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(stopContext); err != nil {
		return fmt.Errorf("shutdown platform api: %w", err)
	}
	return nil
}
