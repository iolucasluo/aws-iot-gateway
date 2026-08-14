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

var ErrJobNotFound = errors.New("webhook job not found")

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
	now        func() time.Time
}

func NewDispatcher(repository Repository, client *http.Client, apps []App, now func() time.Time) *Dispatcher {
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
	return &Dispatcher{repository: repository, client: client, apps: configuredApps, now: now}
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
