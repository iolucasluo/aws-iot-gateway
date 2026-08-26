package redisstore

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	redisclient "github.com/redis/go-redis/v9"
)

const localMediaRevisionTTL = 7 * 24 * time.Hour

var localMediaDeviceSNPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

const nextLocalMediaRevisionScript = `
local current = tonumber(redis.call("GET", KEYS[1]) or "0")
local floor = tonumber(ARGV[1])
local next = current + 1
if next < floor then
  next = floor
end
redis.call("SET", KEYS[1], tostring(next), "PX", ARGV[2])
return next
`

type LocalMediaRevisionStore struct {
	client redisclient.Cmdable
	prefix string
}

func NewLocalMediaRevisionStore(client redisclient.Cmdable, environment string) (*LocalMediaRevisionStore, error) {
	environment = strings.TrimSpace(environment)
	if client == nil || environment == "" || strings.ContainsAny(environment, ":\r\n") {
		return nil, fmt.Errorf("Redis local media revision store requires a client and safe environment")
	}
	return &LocalMediaRevisionStore{
		client: client,
		prefix: "aws-iot-gateway:" + environment + ":local-media-revision:",
	}, nil
}

func (store *LocalMediaRevisionStore) Next(ctx context.Context, deviceSN string, floor int64) (int64, error) {
	if store == nil || store.client == nil || !localMediaDeviceSNPattern.MatchString(deviceSN) || floor <= 0 {
		return 0, fmt.Errorf("invalid local media revision input")
	}
	revision, err := store.client.Eval(
		ctx,
		nextLocalMediaRevisionScript,
		[]string{store.prefix + deviceSN},
		floor,
		localMediaRevisionTTL.Milliseconds(),
	).Int64()
	if err != nil {
		return 0, fmt.Errorf("allocate local media revision: %w", err)
	}
	if revision < floor {
		return 0, fmt.Errorf("Redis returned an invalid local media revision")
	}
	return revision, nil
}
