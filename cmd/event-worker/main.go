package main

import (
	"context"
	"errors"
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
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/project"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/webhooks"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/awssqs"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/credentialcipher"
	gatewaypostgres "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/postgres"
	redisstore "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/platform/redis"
	gatewayruntime "gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/runtime"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/worker"
)

func main() {
	config, err := gatewayruntime.LoadConfigFromEnvironment()
	if err != nil {
		log.Fatalf("invalid event worker configuration: %v", err)
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
	ingestQueue, err := awssqs.New(sqs.NewFromConfig(awsConfig), config.IngestQueueURL)
	if err != nil {
		log.Fatalf("configure AWS SQS ingest queue: %v", err)
	}
	ingestWorker, err := worker.NewIngestWorker(ingestQueue, ingest)
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
	// 平台面项目 webhook 优先于静态配置：只有配置了 KEK 才启用动态解析，
	// 未配置时保持纯 INTERNAL_APPS_JSON 行为。
	var resolver webhooks.TargetResolver
	if config.CredentialKEK != "" {
		credentialCipher, err := credentialcipher.NewFromBase64(config.CredentialKEK, config.CredentialKEKID)
		if err != nil {
			log.Fatalf("configure credential cipher: %v", err)
		}
		projectRepository, err := gatewaypostgres.NewProjectRepository(database)
		if err != nil {
			log.Fatalf("configure project repository: %v", err)
		}
		projectService, err := project.NewService(projectRepository, credentialCipher, time.Now)
		if err != nil {
			log.Fatalf("configure project service: %v", err)
		}
		resolver = projectWebhookResolver{projects: projectService}
	}
	webhookDispatcher := webhooks.NewDispatcher(
		webhookRepository,
		&http.Client{Timeout: 10 * time.Second},
		webhookApps,
		time.Now,
		resolver,
	)
	log.Printf("iot-gateway event-worker started for %s", config.Environment)
	go runIngest(ctx, ingestWorker)
	go runPeriodic(ctx, "device lifecycle job", 500*time.Millisecond, lifecycleWorker.PollOnce)
	go runPeriodic(ctx, "webhook delivery", time.Second, webhookDispatcher.PollOnce)
	<-ctx.Done()
}

// projectWebhookResolver 把项目回调适配成投递器目标。
// 项目不存在或没有 active 回调都按"没有动态目标"处理，允许回退静态配置；
// 解密失败则返回错误，由投递器按可重试失败处理（fail closed）。
type projectWebhookResolver struct{ projects *project.Service }

func (resolver projectWebhookResolver) ResolveTarget(ctx context.Context, subjectID string) (webhooks.App, error) {
	if resolver.projects == nil {
		return webhooks.App{}, webhooks.ErrTargetNotFound
	}
	webhook, err := resolver.projects.ResolveActiveWebhook(ctx, subjectID)
	if err != nil {
		if errors.Is(err, project.ErrWebhookNotFound) || errors.Is(err, project.ErrProjectNotFound) {
			return webhooks.App{}, webhooks.ErrTargetNotFound
		}
		return webhooks.App{}, err
	}
	secret, err := resolver.projects.OpenWebhookSecret(webhook)
	if err != nil {
		return webhooks.App{}, err
	}
	return webhooks.App{
		ID: webhook.ProjectID, URL: webhook.CallbackURL, Secret: secret, EventTypes: webhook.EventTypes,
	}, nil
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
