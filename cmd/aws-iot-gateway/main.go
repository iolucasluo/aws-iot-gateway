package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/iot"
	"github.com/aws/aws-sdk-go-v2/service/iotdataplane"
	"github.com/jackc/pgx/v5/pgxpool"
	redisclient "github.com/redis/go-redis/v9"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/activation"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/awsiot"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/httpapi"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/internalauth"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/binding"
	devicemodule "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/device"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/events"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/media"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/awskvs"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/awss3"
	gatewaypostgres "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/postgres"
	redisstore "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/redis"
	gatewayruntime "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/runtime"
)

func main() {
	config, err := gatewayruntime.LoadConfigFromEnvironment()
	if err != nil {
		log.Fatalf("invalid gateway configuration: %v", err)
	}
	ctx := context.Background()
	database, err := pgxpool.New(ctx, config.DatabaseURL)
	if err != nil {
		log.Fatalf("configure PostgreSQL: %v", err)
	}
	defer database.Close()
	if err := database.Ping(ctx); err != nil {
		log.Fatalf("connect PostgreSQL: %v", err)
	}
	if err := gatewaypostgres.ApplyMigrations(ctx, database); err != nil {
		log.Fatalf("migrate PostgreSQL: %v", err)
	}
	redisOptions, err := redisclient.ParseURL(config.RedisURL)
	if err != nil {
		log.Fatalf("configure Redis: %v", err)
	}
	redis := redisclient.NewClient(redisOptions)
	defer redis.Close()
	if err := redis.Ping(ctx).Err(); err != nil {
		log.Fatalf("connect Redis: %v", err)
	}
	nonceStore, err := redisstore.NewNonceStore(redis, config.Environment)
	if err != nil {
		log.Fatalf("configure replay protection: %v", err)
	}
	credentials := make([]internalauth.Credential, 0, len(config.InternalApps))
	for _, app := range config.InternalApps {
		credentials = append(credentials, internalauth.Credential{
			InternalAppID: app.ID, APIKey: app.APIKey, APISecret: app.APISecret,
		})
	}
	verifier, err := internalauth.NewVerifier(credentials, nonceStore, time.Now)
	if err != nil {
		log.Fatalf("configure internal app authentication: %v", err)
	}
	awsConfig, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion(config.AWSRegion))
	if err != nil {
		log.Fatalf("load AWS configuration: %v", err)
	}
	iotControl := iot.NewFromConfig(awsConfig)
	provisioner, err := awsiot.New(
		iotControl,
		config.DevicePolicyName,
	)
	if err != nil {
		log.Fatalf("configure AWS IoT provisioner: %v", err)
	}
	activationRepository, err := gatewaypostgres.NewActivationRepository(database)
	if err != nil {
		log.Fatalf("configure activation repository: %v", err)
	}
	service := activation.NewServiceWithRepository(provisioner, activationRepository)
	bindingRepository, err := gatewaypostgres.NewBindingRepository(database)
	if err != nil {
		log.Fatalf("configure binding repository: %v", err)
	}
	dataEndpoint, err := iotControl.DescribeEndpoint(ctx, &iot.DescribeEndpointInput{EndpointType: aws.String("iot:Data-ATS")})
	if err != nil || dataEndpoint.EndpointAddress == nil || *dataEndpoint.EndpointAddress == "" {
		log.Fatalf("resolve AWS IoT data endpoint: %v", err)
	}
	iotData := iotdataplane.NewFromConfig(awsConfig, func(options *iotdataplane.Options) {
		options.BaseEndpoint = aws.String("https://" + *dataEndpoint.EndpointAddress)
	})
	deviceRuntime, err := awsiot.NewRuntime(iotData)
	if err != nil {
		log.Fatalf("configure AWS IoT runtime: %v", err)
	}
	ackStore, err := redisstore.NewACKStore(redis, config.Environment)
	if err != nil {
		log.Fatalf("configure device ACK store: %v", err)
	}
	commandRepository, err := gatewaypostgres.NewCommandRepository(database)
	if err != nil {
		log.Fatalf("configure command repository: %v", err)
	}
	deviceService := devicemodule.NewService(deviceRuntime, ackStore, commandRepository, config.IoTTopicPrefix, nil)
	recommissioner, err := binding.NewDeviceRecommissioner(deviceService)
	if err != nil {
		log.Fatalf("configure device recommissioner: %v", err)
	}
	bindingService := binding.NewService(bindingRepository, nil, time.Now, recommissioner)
	eventRepository, err := gatewaypostgres.NewEventRepository(database)
	if err != nil {
		log.Fatalf("configure event repository: %v", err)
	}
	kvsClient, err := awskvs.NewClient(awsConfig, config.AWSRegion)
	if err != nil {
		log.Fatalf("configure AWS KVS: %v", err)
	}
	streamRepository, err := gatewaypostgres.NewStreamRepository(database)
	if err != nil {
		log.Fatalf("configure stream repository: %v", err)
	}
	streamService := media.NewStreamService(
		kvsClient, deviceService, streamRepository, config.KVSChannelPrefix, nil,
	)
	mediaStore, err := awss3.NewClient(awsConfig, config.S3MediaBucket)
	if err != nil {
		log.Fatalf("configure AWS S3 media store: %v", err)
	}
	mediaRepository, err := gatewaypostgres.NewMediaRepository(database)
	if err != nil {
		log.Fatalf("configure media repository: %v", err)
	}
	assetService := media.NewAssetService(mediaStore, deviceService, mediaRepository, config.Environment, nil)
	server := &http.Server{
		Addr: config.ListenAddress,
		Handler: httpapi.NewInternalAPI(service, verifier, httpapi.Dependencies{
			Binding: bindingService, Device: deviceService, Events: events.NewQueryService(eventRepository),
			Media: streamService, Assets: assetService,
			DeviceRuntime: httpapi.DeviceRuntimeConfig{
				AWSRegion: config.AWSRegion, CredentialEndpoint: config.IoTCredentialEndpoint,
				RoleAlias: config.IoTRoleAlias, TopicPrefix: config.IoTTopicPrefix,
				ShadowName: config.IoTShadowName, KVSChannelPrefix: config.KVSChannelPrefix,
			},
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("aws-iot-gateway listening on %s", config.ListenAddress)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve gateway: %v", err)
	}
}
