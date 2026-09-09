package redisstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	redisclient "github.com/redis/go-redis/v9"
	"gitlab.explorex-ai.com/hardware/aws-iot-gateway/internal/modules/device"
)

const ackPollInterval = 25 * time.Millisecond

type ACKStore struct {
	client    *redisclient.Client
	namespace string
}

func NewACKStore(client *redisclient.Client, namespace string) (*ACKStore, error) {
	if client == nil || strings.TrimSpace(namespace) == "" || strings.ContainsAny(namespace, ":\r\n") {
		return nil, fmt.Errorf("Redis ACK store requires a client and safe namespace")
	}
	return &ACKStore{client: client, namespace: namespace}, nil
}

func (store *ACKStore) Store(ctx context.Context, deviceSN, sessionID string, ack map[string]any, ttl time.Duration) error {
	if strings.TrimSpace(deviceSN) == "" || strings.TrimSpace(sessionID) == "" || ack == nil || ttl <= 0 {
		return device.ErrInvalidInput
	}
	payload, err := json.Marshal(ack)
	if err != nil {
		return fmt.Errorf("marshal device ack: %w", err)
	}
	key := store.key(deviceSN, sessionID)
	if err := store.client.SetNX(ctx, key, payload, ttl).Err(); err != nil {
		return fmt.Errorf("store device ack: %w", err)
	}
	tracef("store sn=%s session=%s", deviceSN, sessionID)
	return nil
}

func (store *ACKStore) Wait(ctx context.Context, deviceSN, sessionID string, timeout time.Duration) (map[string]any, error) {
	if strings.TrimSpace(deviceSN) == "" || strings.TrimSpace(sessionID) == "" || timeout <= 0 {
		return nil, device.ErrInvalidInput
	}
	waitContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tracef("wait sn=%s session=%s timeout=%s", deviceSN, sessionID, timeout)
	ticker := time.NewTicker(ackPollInterval)
	defer ticker.Stop()
	for {
		value, err := store.client.GetDel(waitContext, store.key(deviceSN, sessionID)).Bytes()
		if err == nil {
			var ack map[string]any
			if err := json.Unmarshal(value, &ack); err != nil {
				return nil, fmt.Errorf("decode device ack: %w", err)
			}
			tracef("found sn=%s session=%s", deviceSN, sessionID)
			return ack, nil
		}
		if !errors.Is(err, redisclient.Nil) {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				return nil, device.ErrACKTimeout
			}
			return nil, fmt.Errorf("read device ack: %w", err)
		}
		select {
		case <-waitContext.Done():
			tracef("timeout sn=%s session=%s", deviceSN, sessionID)
			return nil, device.ErrACKTimeout
		case <-ticker.C:
		}
	}
}

func tracef(format string, args ...any) {
	if os.Getenv("EXPLOREX_MQTT_TRACE") == "1" {
		log.Printf("redis-ack: "+format, args...)
	}
}

func (store *ACKStore) key(deviceSN, sessionID string) string {
	return "gateway:" + store.namespace + ":ack:" + deviceSN + ":" + sessionID
}
