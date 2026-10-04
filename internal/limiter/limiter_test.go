package limiter_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"rate-limiter/internal/limiter"
	"rate-limiter/internal/storage/memory"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1_700_000_000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

const (
	ip    = "203.0.113.10"
	token = "abc123"
)

func newLimiter(t *testing.T, clock *fakeClock, cfg limiter.Config) *limiter.Limiter {
	t.Helper()
	l, err := limiter.New(memory.NewWithClock(clock.Now), cfg)
	if err != nil {
		t.Fatalf("limiter.New: %v", err)
	}
	return l
}

// hit sends n requests and returns how many were allowed.
func hit(t *testing.T, l *limiter.Limiter, n int, ip, token string) int {
	t.Helper()
	allowed := 0
	for range n {
		ok, err := l.Allow(context.Background(), ip, token)
		if err != nil {
			t.Fatalf("Allow: %v", err)
		}
		if ok {
			allowed++
		}
	}
	return allowed
}

func TestAllow_IPLimitAndBlock(t *testing.T) {
	clock := newFakeClock()
	l := newLimiter(t, clock, limiter.Config{IPLimit: 5, BlockDuration: 5 * time.Minute})

	if got := hit(t, l, 5, ip, ""); got != 5 {
		t.Fatalf("allowed %d requests within the limit, want 5", got)
	}
	if got := hit(t, l, 1, ip, ""); got != 0 {
		t.Fatal("request above the limit was allowed")
	}

	// A new window must not release a blocked IP.
	clock.Advance(2 * time.Second)
	if got := hit(t, l, 1, ip, ""); got != 0 {
		t.Fatal("blocked IP was allowed before the block duration elapsed")
	}

	// Other IPs are not affected.
	if got := hit(t, l, 5, "198.51.100.1", ""); got != 5 {
		t.Fatalf("other IP allowed %d requests, want 5", got)
	}

	clock.Advance(5 * time.Minute)
	if got := hit(t, l, 5, ip, ""); got != 5 {
		t.Fatalf("after block expired allowed %d requests, want 5", got)
	}
}

func TestAllow_WindowResetsCounter(t *testing.T) {
	clock := newFakeClock()
	l := newLimiter(t, clock, limiter.Config{IPLimit: 3, BlockDuration: time.Minute})

	for second := range 5 {
		if got := hit(t, l, 3, ip, ""); got != 3 {
			t.Fatalf("second %d: allowed %d requests, want 3", second, got)
		}
		clock.Advance(time.Second)
	}
}

func TestAllow_TokenOverridesIP(t *testing.T) {
	clock := newFakeClock()
	l := newLimiter(t, clock, limiter.Config{
		IPLimit:       10,
		TokenLimits:   map[string]int64{token: 100},
		BlockDuration: 5 * time.Minute,
	})

	if got := hit(t, l, 100, ip, token); got != 100 {
		t.Fatalf("token allowed %d requests, want 100", got)
	}
	if got := hit(t, l, 1, ip, token); got != 0 {
		t.Fatal("token request above its limit was allowed")
	}
}

func TestAllow_TokenLowerThanIP(t *testing.T) {
	clock := newFakeClock()
	l := newLimiter(t, clock, limiter.Config{
		IPLimit:       10,
		TokenLimits:   map[string]int64{token: 2},
		BlockDuration: time.Minute,
	})

	if got := hit(t, l, 5, ip, token); got != 2 {
		t.Fatalf("token allowed %d requests, want 2 (token limit overrides IP limit)", got)
	}
}

func TestAllow_TokenAndIPAreCountedSeparately(t *testing.T) {
	clock := newFakeClock()
	l := newLimiter(t, clock, limiter.Config{
		IPLimit:       2,
		TokenLimits:   map[string]int64{token: 5},
		BlockDuration: time.Minute,
	})

	// Blocking the IP does not block the token used from the same IP.
	if got := hit(t, l, 3, ip, ""); got != 2 {
		t.Fatalf("IP allowed %d requests, want 2", got)
	}
	if got := hit(t, l, 5, ip, token); got != 5 {
		t.Fatalf("token on blocked IP allowed %d requests, want 5", got)
	}
}

func TestAllow_UnknownTokenFallsBackToIP(t *testing.T) {
	clock := newFakeClock()
	l := newLimiter(t, clock, limiter.Config{
		IPLimit:       3,
		TokenLimits:   map[string]int64{token: 100},
		BlockDuration: time.Minute,
	})

	// Rotating unknown tokens must not bypass the IP limit.
	allowed := 0
	for _, tk := range []string{"x1", "x2", "x3", "x4", "x5"} {
		allowed += hit(t, l, 1, ip, tk)
	}
	if allowed != 3 {
		t.Fatalf("unknown tokens allowed %d requests, want 3 (IP limit)", allowed)
	}
}

func TestAllow_Concurrent(t *testing.T) {
	clock := newFakeClock()
	const limit = 50
	l := newLimiter(t, clock, limiter.Config{IPLimit: limit, BlockDuration: time.Minute})

	var allowed atomic.Int64
	var wg sync.WaitGroup
	for range 500 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := l.Allow(context.Background(), ip, "")
			if err != nil {
				t.Errorf("Allow: %v", err)
				return
			}
			if ok {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := allowed.Load(); got != limit {
		t.Fatalf("allowed %d concurrent requests, want %d", got, limit)
	}
}

type failingStorage struct {
	failOn string
	err    error
}

func (s failingStorage) Increment(context.Context, string, time.Duration) (int64, error) {
	if s.failOn == "increment" {
		return 0, s.err
	}
	return 100, nil
}

func (s failingStorage) Block(context.Context, string, time.Duration) error {
	if s.failOn == "block" {
		return s.err
	}
	return nil
}

func (s failingStorage) IsBlocked(context.Context, string) (bool, error) {
	if s.failOn == "isblocked" {
		return false, s.err
	}
	return false, nil
}

func TestAllow_StorageErrors(t *testing.T) {
	errBoom := errors.New("boom")
	for _, op := range []string{"isblocked", "increment", "block"} {
		t.Run(op, func(t *testing.T) {
			l, err := limiter.New(failingStorage{failOn: op, err: errBoom},
				limiter.Config{IPLimit: 1, BlockDuration: time.Minute})
			if err != nil {
				t.Fatal(err)
			}
			ok, err := l.Allow(context.Background(), ip, "")
			if !errors.Is(err, errBoom) {
				t.Fatalf("err = %v, want %v", err, errBoom)
			}
			if ok {
				t.Fatal("request allowed on storage error")
			}
		})
	}
}

func TestNew_Validation(t *testing.T) {
	storage := memory.New()
	tests := map[string]struct {
		storage limiter.Storage
		cfg     limiter.Config
	}{
		"nil storage":        {nil, limiter.Config{IPLimit: 1, BlockDuration: time.Second}},
		"zero IP limit":      {storage, limiter.Config{IPLimit: 0, BlockDuration: time.Second}},
		"zero block":         {storage, limiter.Config{IPLimit: 1}},
		"empty token":        {storage, limiter.Config{IPLimit: 1, BlockDuration: time.Second, TokenLimits: map[string]int64{"": 1}}},
		"invalid token rate": {storage, limiter.Config{IPLimit: 1, BlockDuration: time.Second, TokenLimits: map[string]int64{"t": 0}}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := limiter.New(tc.storage, tc.cfg); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
