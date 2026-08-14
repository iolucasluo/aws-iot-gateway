package redisstore

import (
	"context"
	"fmt"
	"strings"
	"time"

	redisclient "github.com/redis/go-redis/v9"
)

type NonceStore struct {
	client redisclient.Cmdable
	prefix string
}

func NewNonceStore(client redisclient.Cmdable, environment string) (*NonceStore, error) {
	environment = strings.TrimSpace(environment)
	if client == nil || environment == "" || strings.ContainsAny(environment, ":\r\n") {
		return nil, fmt.Errorf("Redis nonce store requires a client and safe environment")
	}
	return &NonceStore{
		client: client,
		prefix: "aws-iot-gateway:" + environment + ":hmac-nonce:",
	}, nil
}

func (store *NonceStore) Consume(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "\r\n") || ttl <= 0 {
		return false, fmt.Errorf("invalid nonce key or TTL")
	}
	consumed, err := store.client.SetNX(ctx, store.prefix+key, "1", ttl).Result()
	if err != nil {
		return false, fmt.Errorf("consume HMAC nonce: %w", err)
	}
	return consumed, nil
}
