package redisstore

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redisclient "github.com/redis/go-redis/v9"
)

func TestRateLimiterSharesFixedWindowBudget(t *testing.T) {
	server := miniredis.RunT(t)
	client := redisclient.NewClient(&redisclient.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	limiter, err := NewRateLimiter(client, "staging")
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		allowed, err := limiter.Allow(context.Background(), "time-bootstrap:global", 2, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if allowed != (attempt <= 2) {
			t.Fatalf("attempt %d allowed=%t", attempt, allowed)
		}
	}
	server.FastForward(time.Minute)
	allowed, err := limiter.Allow(context.Background(), "time-bootstrap:global", 2, time.Minute)
	if err != nil || !allowed {
		t.Fatalf("new window allowed=%t err=%v", allowed, err)
	}
}

func TestRateLimiterFailsClosedOnInvalidInputAndRedisFailure(t *testing.T) {
	server := miniredis.RunT(t)
	client := redisclient.NewClient(&redisclient.Options{Addr: server.Addr()})
	limiter, err := NewRateLimiter(client, "staging")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := limiter.Allow(context.Background(), "", 1, time.Minute); err == nil {
		t.Fatal("accepted empty key")
	}
	_ = client.Close()
	if _, err := limiter.Allow(context.Background(), "time-bootstrap:global", 1, time.Minute); err == nil {
		t.Fatal("hid Redis failure")
	}
}
