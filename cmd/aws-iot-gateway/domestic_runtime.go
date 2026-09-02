package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	redisclient "github.com/redis/go-redis/v9"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/activation"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/httpapi"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/internalauth"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/binding"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/timebootstrap"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/domesticca"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/localsigner"
	gatewaypostgres "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/postgres"
	redisstore "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/redis"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/provider"
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
	compositionFile, err := os.Open(config.ProviderCompositionPath)
	if err != nil {
		return err
	}
	defer compositionFile.Close()
	composition, err := provider.LoadComposition(compositionFile)
	if err != nil {
		return err
	}
	if composition.Metadata.Environment != config.Environment || composition.Metadata.Region != "cn" {
		return fmt.Errorf("domestic composition metadata does not match development cn runtime")
	}
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
	bindingRepository, err := gatewaypostgres.NewBindingRepository(database)
	if err != nil {
		return err
	}
	// Domestic staging uses the same provider-neutral binding contract as AWS.
	// Recommissioning remains unavailable until a domestic device-runtime
	// adapter is wired, but token issuance and offline cloud unbind are valid.
	bindingService := binding.NewService(bindingRepository, nil, time.Now, nil)
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
			Binding: bindingService,
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
