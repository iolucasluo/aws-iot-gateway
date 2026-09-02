package main

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	redisclient "github.com/redis/go-redis/v9"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/activation"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/httpapi"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/internalauth"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/timebootstrap"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/domesticca"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/localsigner"
	gatewaypostgres "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/postgres"
	redisstore "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/redis"
	gatewayruntime "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/runtime"
)

// runDomesticGateway is intentionally narrow. It owns only the domestic
// development activation/time surface until provider-neutral device runtime,
// media and realtime adapters are implemented and independently verified.
func runDomesticGateway(
	ctx context.Context,
	config gatewayruntime.Config,
	database *pgxpool.Pool,
	redis *redisclient.Client,
	verifier *internalauth.Verifier,
) error {
	provisioner, err := domesticca.New(domesticca.Config{
		Endpoint: config.DomesticMQTTEndpoint, CACertPath: config.DomesticCACertPath,
		CAKeyPath: config.DomesticCAKeyPath,
	})
	if err != nil {
		return err
	}
	activationRepository, err := gatewaypostgres.NewActivationRepository(database)
	if err != nil {
		return err
	}
	activationService := activation.NewServiceWithRepository(provisioner, activationRepository)
	timeSigner, err := localsigner.Load(config.TimeBootstrapLocalPrivateKeyPath)
	if err != nil {
		return err
	}
	timeService, err := timebootstrap.NewService(
		config.TimeBootstrapKeyID, timeSigner, time.Now, timebootstrap.NewSystemClockHealth(time.Now),
	)
	if err != nil {
		return err
	}
	rateLimiter, err := redisstore.NewRateLimiter(redis, config.Environment)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr: config.ListenAddress,
		Handler: httpapi.NewInternalAPI(activationService, verifier, httpapi.Dependencies{
			TimeBootstrap: timeService, TimeBootstrapLimiter: rateLimiter,
			DeviceRuntime: httpapi.DeviceRuntimeConfig{
				ProviderMode: "domestic-dev", Endpoint: config.DomesticMQTTEndpoint,
				TopicPrefix: config.DomesticMQTTTopicPrefix,
			},
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
