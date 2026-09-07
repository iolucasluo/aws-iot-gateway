package redisstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	redisclient "github.com/redis/go-redis/v9"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/ota"
)

func TestOTADownloadGrantStoreHashesTokenBindsDeviceAndAllowsInitialDownloadPlusThreeRetries(t *testing.T) {
	t.Parallel()
	server := miniredis.RunT(t)
	client := redisclient.NewClient(&redisclient.Options{Addr: server.Addr()})
	store, err := NewOTADownloadGrantStore(client, "staging")
	if err != nil {
		t.Fatal(err)
	}
	grant := ota.DownloadGrant{DeviceSN: "fdr-device-1", DeploymentID: "22222222-2222-4222-8222-222222222222", ArtifactKey: "ota/private.tar.gz", SHA256: strings.Repeat("a", 64), FileSize: 1234}
	token := "0123456789abcdef0123456789abcdef"
	if err := store.Store(context.Background(), token, grant, 4, time.Minute); err != nil {
		t.Fatal(err)
	}
	other := grant
	other.DeviceSN = "fdr-other-device"
	if err := store.Store(context.Background(), token, other, 4, time.Minute); !errors.Is(err, ota.ErrDownloadUnavailable) {
		t.Fatalf("duplicate token Store() error = %v", err)
	}
	keys, err := client.Keys(context.Background(), "*").Result()
	if err != nil || len(keys) != 1 {
		t.Fatalf("Redis keys = %v, %v", keys, err)
	}
	fields, err := client.HGetAll(context.Background(), keys[0]).Result()
	if err != nil || strings.Contains(keys[0], token) || strings.Contains(strings.Join([]string{fields["device_sn"], fields["grant"], fields["remaining"]}, ""), token) {
		t.Fatalf("Redis persisted plaintext capability token in %q: %#v", keys[0], fields)
	}
	if _, err := store.Redeem(context.Background(), token, "fdr-wrong"); !errors.Is(err, ota.ErrDownloadNotFound) {
		t.Fatalf("wrong-device Redeem() error = %v", err)
	}
	for attempt := 1; attempt <= 4; attempt++ {
		got, err := store.Redeem(context.Background(), token, grant.DeviceSN)
		if err != nil || got != grant {
			t.Fatalf("Redeem attempt %d = %+v, %v", attempt, got, err)
		}
	}
	if _, err := store.Redeem(context.Background(), token, grant.DeviceSN); !errors.Is(err, ota.ErrDownloadNotFound) {
		t.Fatalf("fifth Redeem() error = %v", err)
	}
	if _, err := store.Redeem(context.Background(), "forged-token-0123456789abcdef012345", grant.DeviceSN); !errors.Is(err, ota.ErrDownloadNotFound) {
		t.Fatalf("forged Redeem() error = %v", err)
	}
	expiringToken := "fedcba9876543210fedcba9876543210"
	if err := store.Store(context.Background(), expiringToken, grant, 4, time.Minute); err != nil {
		t.Fatal(err)
	}
	server.FastForward(time.Minute + time.Second)
	if _, err := store.Redeem(context.Background(), expiringToken, grant.DeviceSN); !errors.Is(err, ota.ErrDownloadNotFound) {
		t.Fatalf("expired Redeem() error = %v", err)
	}
}
