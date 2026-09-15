package webhooks

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type resolverStub struct {
	target App
	err    error
	calls  int
}

func (stub *resolverStub) ResolveTarget(context.Context, string) (App, error) {
	stub.calls++
	if stub.err != nil {
		return App{}, stub.err
	}
	return stub.target, nil
}

// newCallbackServer 返回一个总是 200 的回调端点；测试先把 URL 写进目标再组装投递器。
func newCallbackServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return server
}

func fixedClock() func() time.Time {
	return func() time.Time { return time.Unix(1_718_000_000, 0) }
}

// 动态目标命中时优先于静态配置：这正是"项目自助配置 webhook"的语义。
func TestDispatcherPrefersResolvedTargetOverStaticConfig(t *testing.T) {
	t.Parallel()
	server := newCallbackServer(t)
	const subjectID = "50000000-0000-0000-0000-0000000000a1"
	job := Job{
		ID: "job-1", InternalAppID: subjectID,
		DeliveryID: "delivery-1", EventType: "device.event", Payload: []byte("{}"),
	}
	repository := &repositoryStub{job: job}
	resolver := &resolverStub{target: App{
		ID: subjectID, URL: server.URL, Secret: "resolved-secret", EventTypes: []string{"device.event"},
	}}
	dispatcher := NewDispatcher(repository, server.Client(), []App{{
		ID: subjectID, URL: "https://static.example.invalid/hook",
		Secret: "static-secret", EventTypes: []string{"device.event"},
	}}, fixedClock(), resolver)

	if err := dispatcher.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}
	if !repository.delivered {
		t.Fatal("resolved target was not delivered to")
	}
	if resolver.calls != 1 {
		t.Fatalf("resolver calls = %d, want 1", resolver.calls)
	}
}

// 未命中时回退静态配置；解析失败则按可重试失败处理，不回退。
func TestDispatcherFallsBackOnlyWhenTheTargetIsMissing(t *testing.T) {
	t.Parallel()
	server := newCallbackServer(t)
	job := Job{
		ID: "job-1", InternalAppID: "livpet-staging",
		DeliveryID: "delivery-1", EventType: "device.event", Payload: []byte("{}"),
	}
	static := []App{{
		ID: "livpet-staging", URL: server.URL, Secret: "static-secret", EventTypes: []string{"device.event"},
	}}

	repository := &repositoryStub{job: job}
	dispatcher := NewDispatcher(repository, server.Client(), static, fixedClock(), &resolverStub{err: ErrTargetNotFound})
	if err := dispatcher.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce() error = %v", err)
	}
	if !repository.delivered {
		t.Fatal("ErrTargetNotFound did not fall back to the static config")
	}

	repository = &repositoryStub{job: job}
	dispatcher = NewDispatcher(repository, server.Client(), static, fixedClock(),
		&resolverStub{err: errors.New("postgres unavailable")})
	if err := dispatcher.PollOnce(context.Background()); err == nil {
		t.Fatal("resolver failure was swallowed, want a retryable error")
	}
	if repository.delivered {
		t.Fatal("resolver failure must not fall back to the static config")
	}
}
