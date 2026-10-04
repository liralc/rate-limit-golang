// Package limiter contains the rate limiting business rules, independent of
// HTTP and of the persistence mechanism.
package limiter

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// DefaultWindow is the counting window. Limits are expressed in requests per second.
const DefaultWindow = time.Second

// Storage is the persistence strategy used by the Limiter. Implementations
// must be safe for concurrent use and Increment must be atomic.
type Storage interface {
	// Increment adds one to the counter identified by key and returns the new
	// value. The counter is reset when window elapses after its first increment.
	Increment(ctx context.Context, key string, window time.Duration) (int64, error)
	// Block marks key as blocked for the given duration.
	Block(ctx context.Context, key string, duration time.Duration) error
	// IsBlocked reports whether key is currently blocked.
	IsBlocked(ctx context.Context, key string) (bool, error)
}

// Config holds the limiter rules.
type Config struct {
	// IPLimit is the maximum number of requests per window for a single IP.
	IPLimit int64
	// TokenLimits maps an access token to its maximum number of requests per
	// window. A configured token takes precedence over the IP limit.
	TokenLimits map[string]int64
	// BlockDuration is how long an IP or token stays blocked after exceeding its limit.
	BlockDuration time.Duration
	// Window is the counting window. Zero means DefaultWindow.
	Window time.Duration
}

// Limiter decides whether a request is allowed.
type Limiter struct {
	storage Storage
	cfg     Config
}

// New validates cfg and returns a Limiter backed by storage.
func New(storage Storage, cfg Config) (*Limiter, error) {
	if storage == nil {
		return nil, errors.New("limiter: storage is required")
	}
	if cfg.IPLimit <= 0 {
		return nil, errors.New("limiter: IP limit must be greater than zero")
	}
	if cfg.BlockDuration <= 0 {
		return nil, errors.New("limiter: block duration must be greater than zero")
	}
	for token, limit := range cfg.TokenLimits {
		if token == "" {
			return nil, errors.New("limiter: token must not be empty")
		}
		if limit <= 0 {
			return nil, fmt.Errorf("limiter: limit for token %q must be greater than zero", token)
		}
	}
	if cfg.Window <= 0 {
		cfg.Window = DefaultWindow
	}
	return &Limiter{storage: storage, cfg: cfg}, nil
}

// Allow reports whether a request from ip, optionally carrying token, may proceed.
//
// When token is configured, its limit overrides the IP limit and the request is
// counted against the token. Otherwise the request is counted against the IP.
// Exceeding the limit blocks the key for the configured block duration.
func (l *Limiter) Allow(ctx context.Context, ip, token string) (bool, error) {
	key, limit := l.resolve(ip, token)

	blocked, err := l.storage.IsBlocked(ctx, key)
	if err != nil {
		return false, fmt.Errorf("limiter: checking block for %s: %w", key, err)
	}
	if blocked {
		return false, nil
	}

	count, err := l.storage.Increment(ctx, key, l.cfg.Window)
	if err != nil {
		return false, fmt.Errorf("limiter: incrementing counter for %s: %w", key, err)
	}
	if count <= limit {
		return true, nil
	}

	if err := l.storage.Block(ctx, key, l.cfg.BlockDuration); err != nil {
		return false, fmt.Errorf("limiter: blocking %s: %w", key, err)
	}
	return false, nil
}

// resolve returns the storage key and limit that apply to the request.
// Unknown tokens fall back to the IP limit so that sending random tokens
// cannot be used to bypass the IP restriction.
func (l *Limiter) resolve(ip, token string) (string, int64) {
	if token != "" {
		if limit, ok := l.cfg.TokenLimits[token]; ok {
			return "token:" + token, limit
		}
	}
	return "ip:" + ip, l.cfg.IPLimit
}
