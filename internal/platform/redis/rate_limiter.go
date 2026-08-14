package redisstore

import (
	"context"
	"fmt"
	"strings"
	"time"

	redisclient "github.com/redis/go-redis/v9"
)

var fixedWindowScript = redisclient.NewScript(`
local current = redis.call("INCR", KEYS[1])
if current == 1 then
  redis.call("PEXPIRE", KEYS[1], ARGV[1])
end
return current
`)

type RateLimiter struct {
	client redisclient.Cmdable
	prefix string
}

func NewRateLimiter(client redisclient.Cmdable, environment string) (*RateLimiter, error) {
	environment = strings.TrimSpace(environment)
	if client == nil || environment == "" || strings.ContainsAny(environment, ":\r\n") {
		return nil, fmt.Errorf("Redis rate limiter requires a client and safe environment")
	}
	return &RateLimiter{client: client, prefix: "aws-iot-gateway:" + environment + ":rate:"}, nil
}

func (limiter *RateLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 256 || strings.ContainsAny(key, "\r\n") || limit <= 0 || window < time.Millisecond {
		return false, fmt.Errorf("invalid rate-limit key, limit, or window")
	}
	count, err := fixedWindowScript.Run(ctx, limiter.client, []string{limiter.prefix + key}, window.Milliseconds()).Int64()
	if err != nil {
		return false, fmt.Errorf("consume rate-limit budget: %w", err)
	}
	return count <= int64(limit), nil
}
