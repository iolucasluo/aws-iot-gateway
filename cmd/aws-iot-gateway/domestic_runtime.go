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
	devicemodule "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/device"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/media"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/timebootstrap"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/domesticca"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/explorexsignal"
	gatewayffmpeg "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/ffmpeg"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/localsigner"
	gatewaypostgres "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/postgres"
	redisstore "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/redis"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/provider"
	gatewayruntime "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/runtime"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/worker"
)

// runDomesticGateway owns the domestic development activation/device/MQTT
// surface plus provider-neutral realtime and media-object orchestration.
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
	var objectStore provider.ObjectStore
	if composition.Bindings["media_object"] != "" {
		objectStore, err = gatewayruntime.BuildObjectStore(composition, os.Getenv)
		if err != nil {
			return err
		}
	}
	realtimeInstance := composition.Bindings["realtime"]
	if realtimeInstance == "" || composition.Instances[realtimeInstance].Driver != "explorex_signal" {
		return fmt.Errorf("domestic composition realtime binding must use explorex_signal")
	}
	realtimeClient, err := explorexsignal.NewClient(explorexsignal.Config{
		Endpoint: config.DomesticRealtimeEndpoint, AuthorizationToken: config.DomesticRealtimeAuthorizationToken,
		Capabilities: provider.RealtimeCapabilities{Protocols: []string{explorexsignal.Protocol}, DataChannel: true, AudioSend: true, AudioReceive: true},
	})
	if err != nil {
		return err
	}
	if err := realtimeClient.Health(ctx); err != nil {
		return fmt.Errorf("check domestic realtime provider: %w", err)
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
	eventRepository, err := gatewaypostgres.NewEventRepository(database)
	if err != nil {
		return err
	}
	ackStore, err := redisstore.NewACKStore(redis, config.Environment)
	if err != nil {
		return err
	}
	messaging, err := gatewayruntime.BuildMessaging(composition, domesticMessagingEnvironment(config))
	if err != nil {
		return err
	}
	defer messaging.Close()
	deviceSubscriptions, err := gatewayruntime.StartDeviceIngest(ctx, messaging, ackStore, eventRepository)
	if err != nil {
		return err
	}
	defer deviceSubscriptions.Close()
	devicePublisher, err := messaging.NewDevicePublisher(time.Now)
	if err != nil {
		return err
	}
	commandRepository, err := gatewaypostgres.NewCommandRepository(database)
	if err != nil {
		return err
	}
	deviceService := devicemodule.NewService(
		devicePublisher, ackStore, commandRepository, config.DomesticMQTTTopicPrefix, nil,
	)
	var assetService *media.AssetService
	if objectStore != nil {
		mediaRepository, err := gatewaypostgres.NewMediaRepository(database)
		if err != nil {
			return err
		}
		snapshotConverter, err := gatewayffmpeg.NewConverter("ffmpeg")
		if err != nil {
			return err
		}
		assetService = media.NewAssetService(
			objectStore, deviceService, mediaRepository, config.Environment, snapshotConverter, nil,
		)
		// The object store is what makes a device-local recording readable by the app, so
		// the workers that use it belong to the same guard. The thumbnail worker fetches
		// the poster frame from the device and uploads it; without it thumbnail_object_key
		// stays empty, the asset service exposes no thumbnail URL, and the app shows no
		// first-frame image. The reconciliation worker projects the device catalog that
		// the outbox rows are keyed on.
		thumbnailStore, ok := objectStore.(worker.LocalThumbnailStore)
		if !ok {
			return fmt.Errorf("media object store cannot serve local recording thumbnails")
		}
		localThumbnailWorker, err := worker.NewLocalThumbnailWorker(
			mediaRepository, thumbnailStore, deviceService, config.Environment,
		)
		if err != nil {
			return fmt.Errorf("configure local thumbnail worker: %w", err)
		}
		go runLocalThumbnailWorker(ctx, localThumbnailWorker)
		localReconciliationWorker, err := worker.NewLocalReconciliationWorker(
			mediaRepository, deviceService,
		)
		if err != nil {
			return fmt.Errorf("configure local recording reconciliation worker: %w", err)
		}
		go runLocalReconciliationWorker(ctx, localReconciliationWorker)
	}
	bindingService, err := newDomesticBindingService(bindingRepository, deviceService)
	if err != nil {
		return err
	}
	streamRepository, err := gatewaypostgres.NewStreamRepository(database)
	if err != nil {
		return err
	}
	streamService := media.NewRealtimeStreamService(realtimeClient, realtimeInstance, deviceService, streamRepository, nil)
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
			Binding: bindingService, Media: streamService, Assets: assetService,
			Device:        deviceService,
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

func newDomesticBindingService(repository binding.Repository, commander binding.DeviceCommander) (*binding.Service, error) {
	recommissioner, err := binding.NewDeviceRecommissioner(commander)
	if err != nil {
		return nil, err
	}
	return binding.NewService(repository, nil, time.Now, recommissioner), nil
}

func domesticMessagingEnvironment(config gatewayruntime.Config) func(string) string {
	values := map[string]string{
		"GATEWAY_MQTT_CLIENT_ID":        config.DomesticMQTTClientID,
		"GATEWAY_MQTT_TOPIC_PREFIX":     config.DomesticMQTTTopicPrefix,
		"GATEWAY_MQTT_CA_FILE":          config.DomesticCACertPath,
		"GATEWAY_MQTT_CLIENT_CERT_FILE": config.DomesticMQTTClientCertPath,
		"GATEWAY_MQTT_CLIENT_KEY_FILE":  config.DomesticMQTTClientKeyPath,
	}
	return func(key string) string { return values[key] }
}
