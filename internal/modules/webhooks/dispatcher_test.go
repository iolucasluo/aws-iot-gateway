package webhooks

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type repositoryStub struct {
	job       Job
	delivered bool
}

func (repository *repositoryStub) Claim(context.Context) (Job, error) { return repository.job, nil }
func (repository *repositoryStub) Complete(context.Context, Job, int) error {
	repository.delivered = true
	return nil
}
func (repository *repositoryStub) Retry(context.Context, Job, int, string) error { return nil }

func TestDispatcherPreservesExploreXWebhookWireSignature(t *testing.T) {
	t.Parallel()
	body := []byte(`{"delivery_id":"delivery-1","event_type":"feed_done"}`)
	repository := &repositoryStub{job: Job{
		ID: "job-1", InternalAppID: "livpet-staging", DeliveryID: "delivery-1",
		EventType: "feed_done", Payload: body, Attempts: 1,
	}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		timestamp := request.Header.Get("X-Explorex-Timestamp")
		mac := hmac.New(sha256.New, []byte("webhook-secret"))
		mac.Write([]byte(timestamp + "."))
		mac.Write(body)
		want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		if request.Header.Get("X-Explorex-Signature") != want || request.Header.Get("X-Explorex-Delivery") != "delivery-1" {
			t.Errorf("headers = %#v", request.Header)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	dispatcher := NewDispatcher(repository, server.Client(), []App{{
		ID: "livpet-staging", URL: server.URL, Secret: "webhook-secret", EventTypes: []string{"feed_done"},
	}}, func() time.Time { return time.Unix(1_718_000_000, 0) })
	if err := dispatcher.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}
	if !repository.delivered {
		t.Fatal("webhook delivery was not completed")
	}
}
