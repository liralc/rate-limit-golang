package redis_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"rate-limiter/internal/limiter"
	redisstorage "rate-limiter/internal/storage/redis"
)

// Compile-time check that the strategy satisfies the limiter contract.
var _ limiter.Storage = (*redisstorage.Storage)(nil)

func newMiniredis(t *testing.T) (*miniredis.Miniredis, *redisstorage.Storage) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return mr, redisstorage.New(client)
}

func TestStorage_IncrementSetsTTLAndResets(t *testing.T) {
	mr, s := newMiniredis(t)
	ctx := context.Background()

	for want := int64(1); want <= 3; want++ {
		got, err := s.Increment(ctx, "ip:1.2.3.4", time.Second)
		if err != nil || got != want {
			t.Fatalf("Increment = %d, %v; want %d", got, err, want)
		}
	}
	if ttl := mr.TTL("ratelimit:count:ip:1.2.3.4"); ttl <= 0 || ttl > time.Second {
		t.Fatalf("counter TTL = %v, want (0, 1s]", ttl)
	}

	mr.FastForward(time.Second)
	if got, err := s.Increment(ctx, "ip:1.2.3.4", time.Second); err != nil || got != 1 {
		t.Fatalf("after window Increment = %d, %v; want 1", got, err)
	}
}

func TestStorage_Block(t *testing.T) {
	mr, s := newMiniredis(t)
	ctx := context.Background()

	if blocked, err := s.IsBlocked(ctx, "token:abc"); err != nil || blocked {
		t.Fatalf("IsBlocked = %v, %v; want false", blocked, err)
	}
	if err := s.Block(ctx, "token:abc", 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	if blocked, err := s.IsBlocked(ctx, "token:abc"); err != nil || !blocked {
		t.Fatalf("IsBlocked = %v, %v; want true", blocked, err)
	}

	mr.FastForward(5 * time.Minute)
	if blocked, err := s.IsBlocked(ctx, "token:abc"); err != nil || blocked {
		t.Fatalf("after duration IsBlocked = %v, %v; want false", blocked, err)
	}
}

func TestStorage_ConnectionError(t *testing.T) {
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	s := redisstorage.New(client)
	mr.Close()

	ctx := context.Background()
	if _, err := s.Increment(ctx, "k", time.Second); err == nil {
		t.Fatal("Increment: expected error")
	}
	if _, err := s.IsBlocked(ctx, "k"); err == nil {
		t.Fatal("IsBlocked: expected error")
	}
	if err := s.Block(ctx, "k", time.Second); err == nil {
		t.Fatal("Block: expected error")
	}
}

// TestStorage_RealRedis runs against a real Redis when REDIS_TEST_ADDR is set
// (docker compose sets it for the tests service).
func TestStorage_RealRedis(t *testing.T) {
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		t.Skip("REDIS_TEST_ADDR not set")
	}
	client := goredis.NewClient(&goredis.Options{Addr: addr})
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("ping %s: %v", addr, err)
	}

	key := "ip:test-" + time.Now().Format(time.RFC3339Nano)
	l, err := limiter.New(redisstorage.New(client), limiter.Config{IPLimit: 10, BlockDuration: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}

	var (
		mu      sync.Mutex
		allowed int
		wg      sync.WaitGroup
	)
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := l.Allow(ctx, key, "")
			if err != nil {
				t.Errorf("Allow: %v", err)
				return
			}
			if ok {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != 10 {
		t.Fatalf("allowed %d concurrent requests, want 10", allowed)
	}
}
