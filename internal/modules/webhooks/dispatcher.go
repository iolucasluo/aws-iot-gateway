package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

var (
	ErrJobNotFound = errors.New("webhook job not found")
	// ErrTargetNotFound 表示该主体没有动态投递目标；
	// 调用方据此回退部署期静态配置，而不是把任务判死。
	ErrTargetNotFound = errors.New("webhook target not found")
)

// TargetResolver 按主体 id 解析投递目标（平台面项目 webhook）。
//
// 约定：未命中返回 ErrTargetNotFound（允许回退静态 INTERNAL_APPS_JSON 配置）；
// 其它错误表示目标来源不可用，按可重试失败处理，不回退。
type TargetResolver interface {
	ResolveTarget(ctx context.Context, subjectID string) (App, error)
}

type Job struct {
	ID            string
	InternalAppID string
	DeliveryID    string
	EventType     string
	Payload       []byte
	Attempts      int
}

type Repository interface {
	Claim(context.Context) (Job, error)
	Complete(context.Context, Job, int) error
	Retry(context.Context, Job, int, string) error
}

type App struct {
	ID         string
	URL        string
	Secret     string
	EventTypes []string
}

type Dispatcher struct {
	repository Repository
	client     *http.Client
	apps       map[string]App
	resolver   TargetResolver
	now        func() time.Time
}

// NewDispatcher 组装投递器。resolver 可选：给了就先按主体解析动态目标，
// 未命中再回退静态 apps。
func NewDispatcher(repository Repository, client *http.Client, apps []App, now func() time.Time, resolver ...TargetResolver) *Dispatcher {
	configuredApps := make(map[string]App, len(apps))
	for _, app := range apps {
		configuredApps[app.ID] = app
	}
	if client == nil {
		client = http.DefaultClient
	}
	if now == nil {
		now = time.Now
	}
	dispatcher := &Dispatcher{repository: repository, client: client, apps: configuredApps, now: now}
	if len(resolver) > 0 {
		dispatcher.resolver = resolver[0]
	}
	return dispatcher
}

func (dispatcher *Dispatcher) PollOnce(ctx context.Context) error {
	if dispatcher == nil || dispatcher.repository == nil {
		return nil
	}
	job, err := dispatcher.repository.Claim(ctx)
	if errors.Is(err, ErrJobNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	app, configured := dispatcher.apps[job.InternalAppID]
	if dispatcher.resolver != nil {
		resolved, err := dispatcher.resolver.ResolveTarget(ctx, job.InternalAppID)
		switch {
		case err == nil:
			app, configured = resolved, true
		case errors.Is(err, ErrTargetNotFound):
			// 该项目没有自助配置的 webhook：保持静态配置的结果。
		default:
			return dispatcher.retry(ctx, job, 0, err)
		}
	}
	if !configured || app.URL == "" || app.Secret == "" || !subscribes(app.EventTypes, job.EventType) {
		return dispatcher.repository.Complete(ctx, job, 0)
	}
	timestamp := strconv.FormatInt(dispatcher.now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(app.Secret))
	_, _ = mac.Write([]byte(timestamp + "."))
	_, _ = mac.Write(job.Payload)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, app.URL, bytes.NewReader(job.Payload))
	if err != nil {
		return dispatcher.retry(ctx, job, 0, err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Explorex-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	request.Header.Set("X-Explorex-Timestamp", timestamp)
	request.Header.Set("X-Explorex-Delivery", job.DeliveryID)
	request.Header.Set("X-Explorex-Event", job.EventType)
	response, err := dispatcher.client.Do(request)
	if err != nil {
		return dispatcher.retry(ctx, job, 0, err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	_ = response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return dispatcher.retry(ctx, job, response.StatusCode, fmt.Errorf("webhook returned HTTP %d", response.StatusCode))
	}
	return dispatcher.repository.Complete(ctx, job, response.StatusCode)
}

func (dispatcher *Dispatcher) retry(ctx context.Context, job Job, statusCode int, deliveryError error) error {
	if err := dispatcher.repository.Retry(ctx, job, statusCode, deliveryError.Error()); err != nil {
		return errors.Join(deliveryError, err)
	}
	return deliveryError
}

func subscribes(eventTypes []string, eventType string) bool {
	for _, configured := range eventTypes {
		if configured == eventType {
			return true
		}
	}
	return false
}
