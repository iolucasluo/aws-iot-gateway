package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/iot"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/jackc/pgx/v5/pgxpool"
	redisclient "github.com/redis/go-redis/v9"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/awsiot"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/events"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/webhooks"
	gatewaypostgres "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/postgres"
	redisstore "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/redis"
	gatewayruntime "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/runtime"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/worker"
)

func main() {
	config, err := gatewayruntime.LoadConfigFromEnvironment()
	if err != nil {
		log.Fatalf("invalid gateway worker configuration: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
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
	ackStore, err := redisstore.NewACKStore(redis, config.Environment)
	if err != nil {
		log.Fatalf("configure ACK store: %v", err)
	}
	eventRepository, err := gatewaypostgres.NewEventRepository(database)
	if err != nil {
		log.Fatalf("configure event repository: %v", err)
	}
	ingest, err := events.NewIngestService(config.IoTTopicPrefix, ackStore, eventRepository)
	if err != nil {
		log.Fatalf("configure IoT ingest: %v", err)
	}
	awsConfig, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(config.AWSRegion))
	if err != nil {
		log.Fatalf("load AWS configuration: %v", err)
	}
	ingestWorker, err := worker.NewIngestWorker(sqs.NewFromConfig(awsConfig), config.IngestQueueURL, ingest)
	if err != nil {
		log.Fatalf("configure ingest worker: %v", err)
	}
	lifecycleRepository, err := gatewaypostgres.NewLifecycleRepository(database)
	if err != nil {
		log.Fatalf("configure lifecycle repository: %v", err)
	}
	lifecycleClient, err := awsiot.NewLifecycle(iot.NewFromConfig(awsConfig))
	if err != nil {
		log.Fatalf("configure AWS IoT lifecycle: %v", err)
	}
	lifecycleWorker := worker.NewLifecycleWorker(lifecycleRepository, lifecycleClient)
	webhookRepository, err := gatewaypostgres.NewWebhookRepository(database)
	if err != nil {
		log.Fatalf("configure webhook repository: %v", err)
	}
	webhookApps := make([]webhooks.App, 0, len(config.InternalApps))
	for _, app := range config.InternalApps {
		webhookApps = append(webhookApps, webhooks.App{
			ID: app.ID, URL: app.WebhookURL, Secret: app.WebhookSecret, EventTypes: app.WebhookEventTypes,
		})
	}
	webhookDispatcher := webhooks.NewDispatcher(
		webhookRepository,
		&http.Client{Timeout: 10 * time.Second},
		webhookApps,
		time.Now,
	)
	log.Printf("aws-iot-gateway worker started for %s", config.Environment)
	go runIngest(ctx, ingestWorker)
	go runPeriodic(ctx, "device lifecycle job", 500*time.Millisecond, lifecycleWorker.PollOnce)
	go runPeriodic(ctx, "webhook delivery", time.Second, webhookDispatcher.PollOnce)
	<-ctx.Done()
}

func runIngest(ctx context.Context, ingestWorker *worker.IngestWorker) {
	for ctx.Err() == nil {
		if err := ingestWorker.PollOnce(ctx); err != nil && ctx.Err() == nil {
			log.Printf("IoT ingest poll failed: %v", err)
			wait(ctx, time.Second)
		}
	}
}

func runPeriodic(ctx context.Context, name string, interval time.Duration, poll func(context.Context) error) {
	for ctx.Err() == nil {
		if err := poll(ctx); err != nil && ctx.Err() == nil {
			log.Printf("%s failed: %v", name, err)
		}
		wait(ctx, interval)
	}
}

func wait(ctx context.Context, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
