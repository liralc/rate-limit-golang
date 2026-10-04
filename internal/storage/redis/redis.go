// Package redis implements the limiter.Storage strategy on top of Redis.
package redis

import (
	"context"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

const (
	counterPrefix = "ratelimit:count:"
	blockPrefix   = "ratelimit:block:"
)

// incrementScript increments the counter and sets its expiration on the first
// hit, atomically, so a counter can never be left without a TTL.
var incrementScript = goredis.NewScript(`
local count = redis.call("INCR", KEYS[1])
if count == 1 then
	redis.call("PEXPIRE", KEYS[1], ARGV[1])
end
return count
`)

// Storage is a Redis backed limiter.Storage.
type Storage struct {
	client goredis.UniversalClient
}

// New returns a Storage that uses client.
func New(client goredis.UniversalClient) *Storage {
	return &Storage{client: client}
}

// Increment implements limiter.Storage.
func (s *Storage) Increment(ctx context.Context, key string, window time.Duration) (int64, error) {
	return incrementScript.Run(ctx, s.client, []string{counterPrefix + key}, window.Milliseconds()).Int64()
}

// Block implements limiter.Storage.
func (s *Storage) Block(ctx context.Context, key string, duration time.Duration) error {
	return s.client.Set(ctx, blockPrefix+key, 1, duration).Err()
}

// IsBlocked implements limiter.Storage.
func (s *Storage) IsBlocked(ctx context.Context, key string) (bool, error) {
	n, err := s.client.Exists(ctx, blockPrefix+key).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
