package redisstore

import (
	"context"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
	redisclient "github.com/redis/go-redis/v9"
)

func TestLocalMediaRevisionStoreAllocatesMonotonicDeviceScopedRevisions(t *testing.T) {
	t.Parallel()

	server := miniredis.RunT(t)
	client := redisclient.NewClient(&redisclient.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store, err := NewLocalMediaRevisionStore(client, "staging")
	if err != nil {
		t.Fatalf("NewLocalMediaRevisionStore() error = %v", err)
	}

	const workers = 16
	revisions := make(chan int64, workers)
	errors := make(chan error, workers)
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			revision, err := store.Next(context.Background(), "fdr-0001", 1_718_000_000_000)
			if err != nil {
				errors <- err
				return
			}
			revisions <- revision
		}()
	}
	wait.Wait()
	close(errors)
	close(revisions)
	for err := range errors {
		t.Fatalf("Next() error = %v", err)
	}
	seen := make(map[int64]bool, workers)
	for revision := range revisions {
		seen[revision] = true
	}
	for revision := int64(1_718_000_000_000); revision < 1_718_000_000_000+workers; revision++ {
		if !seen[revision] {
			t.Fatalf("concurrent revisions = %v, missing %d", seen, revision)
		}
	}

	jumped, err := store.Next(context.Background(), "fdr-0001", 1_718_000_010_000)
	if err != nil || jumped != 1_718_000_010_000 {
		t.Fatalf("floor jump Next() = %d, %v", jumped, err)
	}
	other, err := store.Next(context.Background(), "fdr-0002", 99)
	if err != nil || other != 99 {
		t.Fatalf("other device Next() = %d, %v", other, err)
	}
	if !server.Exists("aws-iot-gateway:staging:local-media-revision:fdr-0001") {
		t.Fatal("revision was not stored under the environment and device namespace")
	}
}

func TestLocalMediaRevisionStoreRejectsUnsafeInputsAndRedisFailure(t *testing.T) {
	t.Parallel()

	server := miniredis.RunT(t)
	client := redisclient.NewClient(&redisclient.Options{Addr: server.Addr()})
	store, err := NewLocalMediaRevisionStore(client, "staging")
	if err != nil {
		t.Fatalf("NewLocalMediaRevisionStore() error = %v", err)
	}
	for _, deviceSN := range []string{"", "bad:device", "bad\ndevice"} {
		if _, err := store.Next(context.Background(), deviceSN, 1); err == nil {
			t.Fatalf("Next() accepted unsafe device SN %q", deviceSN)
		}
	}
	if _, err := store.Next(context.Background(), "fdr-0001", 0); err == nil {
		t.Fatal("Next() accepted a non-positive floor")
	}
	_ = client.Close()
	if _, err := store.Next(context.Background(), "fdr-0001", 1); err == nil {
		t.Fatal("Next() hid Redis unavailability")
	}
}
