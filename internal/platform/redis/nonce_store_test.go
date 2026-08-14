package redisstore

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redisclient "github.com/redis/go-redis/v9"
)

func TestNonceStoreAtomicallyConsumesNamespacedNonce(t *testing.T) {
	t.Parallel()

	server := miniredis.RunT(t)
	client := redisclient.NewClient(&redisclient.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store, err := NewNonceStore(client, "staging")
	if err != nil {
		t.Fatalf("NewNonceStore() error = %v", err)
	}

	consumed, err := store.Consume(context.Background(), "livpet:nonce-1", 10*time.Minute)
	if err != nil || !consumed {
		t.Fatalf("first Consume() = %t, %v", consumed, err)
	}
	consumed, err = store.Consume(context.Background(), "livpet:nonce-1", 10*time.Minute)
	if err != nil || consumed {
		t.Fatalf("replayed Consume() = %t, %v", consumed, err)
	}
	if !server.Exists("aws-iot-gateway:staging:hmac-nonce:livpet:nonce-1") {
		t.Fatal("nonce was not stored under the environment namespace")
	}
}

func TestNonceStoreRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	server := miniredis.RunT(t)
	client := redisclient.NewClient(&redisclient.Options{Addr: server.Addr()})
	store, err := NewNonceStore(client, "staging")
	if err != nil {
		t.Fatalf("NewNonceStore() error = %v", err)
	}
	if _, err := store.Consume(context.Background(), "", time.Minute); err == nil {
		t.Fatal("Consume() accepted an empty nonce key")
	}
	if _, err := store.Consume(context.Background(), "nonce", 0); err == nil {
		t.Fatal("Consume() accepted a non-positive TTL")
	}
	_ = client.Close()
	if _, err := store.Consume(context.Background(), "nonce", time.Minute); err == nil {
		t.Fatal("Consume() hid Redis unavailability")
	}
}
