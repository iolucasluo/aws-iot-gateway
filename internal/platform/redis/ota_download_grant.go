package redisstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	redisclient "github.com/redis/go-redis/v9"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/ota"
)

var storeOTADownloadGrant = redisclient.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then
  return 0
end
redis.call('HSET', KEYS[1], 'device_sn', ARGV[1], 'grant', ARGV[2], 'remaining', ARGV[3])
redis.call('EXPIRE', KEYS[1], ARGV[4])
return 1
`)

var redeemOTADownloadGrant = redisclient.NewScript(`
local device = redis.call('HGET', KEYS[1], 'device_sn')
if not device or device ~= ARGV[1] then
  return nil
end
local remaining = tonumber(redis.call('HGET', KEYS[1], 'remaining') or '0')
if remaining <= 0 then
  return nil
end
local grant = redis.call('HGET', KEYS[1], 'grant')
if not grant then
  return nil
end
redis.call('HINCRBY', KEYS[1], 'remaining', -1)
return grant
`)

type OTADownloadGrantStore struct {
	client    *redisclient.Client
	namespace string
}

func NewOTADownloadGrantStore(client *redisclient.Client, namespace string) (*OTADownloadGrantStore, error) {
	if client == nil || strings.TrimSpace(namespace) == "" || strings.ContainsAny(namespace, ":\r\n") {
		return nil, fmt.Errorf("OTA download grant store requires a client and safe namespace")
	}
	return &OTADownloadGrantStore{client: client, namespace: namespace}, nil
}

func (store *OTADownloadGrantStore) Store(ctx context.Context, token string, grant ota.DownloadGrant, attempts int, ttl time.Duration) error {
	if store == nil || store.client == nil || strings.TrimSpace(token) == "" || attempts <= 0 || ttl <= 0 ||
		strings.TrimSpace(grant.DeviceSN) == "" || strings.TrimSpace(grant.ArtifactKey) == "" {
		return ota.ErrDownloadUnavailable
	}
	payload, err := json.Marshal(grant)
	if err != nil {
		return fmt.Errorf("marshal OTA download grant: %w", err)
	}
	seconds := int64(ttl / time.Second)
	if seconds <= 0 {
		return ota.ErrDownloadUnavailable
	}
	created, err := storeOTADownloadGrant.Run(ctx, store.client, []string{store.key(token)},
		grant.DeviceSN, payload, attempts, seconds).Int()
	if err != nil {
		return fmt.Errorf("store OTA download grant: %w", err)
	}
	if created != 1 {
		return ota.ErrDownloadUnavailable
	}
	return nil
}

func (store *OTADownloadGrantStore) Redeem(ctx context.Context, token, deviceSN string) (ota.DownloadGrant, error) {
	if store == nil || store.client == nil || strings.TrimSpace(token) == "" || strings.TrimSpace(deviceSN) == "" {
		return ota.DownloadGrant{}, ota.ErrDownloadNotFound
	}
	value, err := redeemOTADownloadGrant.Run(ctx, store.client, []string{store.key(token)}, deviceSN).Result()
	if errors.Is(err, redisclient.Nil) || value == nil {
		return ota.DownloadGrant{}, ota.ErrDownloadNotFound
	}
	if err != nil {
		return ota.DownloadGrant{}, fmt.Errorf("%w: redeem capability", ota.ErrDownloadUnavailable)
	}
	raw, ok := value.(string)
	if !ok || raw == "" {
		return ota.DownloadGrant{}, ota.ErrDownloadNotFound
	}
	var grant ota.DownloadGrant
	if err := json.Unmarshal([]byte(raw), &grant); err != nil {
		return ota.DownloadGrant{}, fmt.Errorf("decode OTA download grant: %w", err)
	}
	return grant, nil
}

func (store *OTADownloadGrantStore) key(token string) string {
	digest := sha256.Sum256([]byte(token))
	return "gateway:" + store.namespace + ":ota-download:" + hex.EncodeToString(digest[:])
}
