package redisstore

import (
	"context"
	"errors"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	redisclient "github.com/redis/go-redis/v9"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/device"
)

func TestACKStoreRoutesOneMatchingACKAndTimesOutClosed(t *testing.T) {
	t.Parallel()
	server := miniredis.RunT(t)
	client := redisclient.NewClient(&redisclient.Options{Addr: server.Addr()})
	store, err := NewACKStore(client, "staging")
	if err != nil {
		t.Fatalf("NewACKStore() error = %v", err)
	}
	ctx := context.Background()
	want := map[string]any{"session_id": "session-1", "code": float64(0)}
	if err := store.Store(ctx, "fdr-0001", "session-1", want, time.Minute); err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	got, err := store.Wait(ctx, "fdr-0001", "session-1", time.Second)
	if err != nil || got["session_id"] != "session-1" {
		t.Fatalf("Wait() = %#v, %v", got, err)
	}
	if _, err := store.Wait(ctx, "fdr-0001", "session-1", 10*time.Millisecond); !errors.Is(err, device.ErrACKTimeout) {
		t.Fatalf("second Wait() error = %v", err)
	}
}
