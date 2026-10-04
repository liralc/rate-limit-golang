// Package memory implements the limiter.Storage strategy in process memory.
// It is useful for local development and tests, and shows how the persistence
// strategy can be swapped. State is not shared between application instances.
package memory

import (
	"context"
	"sync"
	"time"
)

// sweepInterval controls how often expired entries are purged.
const sweepInterval = time.Minute

type counter struct {
	value     int64
	expiresAt time.Time
}

// Storage is an in-memory limiter.Storage. It is safe for concurrent use.
type Storage struct {
	mu        sync.Mutex
	counters  map[string]counter
	blocks    map[string]time.Time
	now       func() time.Time
	lastSweep time.Time
}

// New returns an empty Storage that uses the system clock.
func New() *Storage {
	return NewWithClock(time.Now)
}

// NewWithClock returns an empty Storage that reads the current time from now.
func NewWithClock(now func() time.Time) *Storage {
	return &Storage{
		counters:  make(map[string]counter),
		blocks:    make(map[string]time.Time),
		now:       now,
		lastSweep: now(),
	}
}

// Increment implements limiter.Storage.
func (s *Storage) Increment(_ context.Context, key string, window time.Duration) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	s.sweep(now)

	c, ok := s.counters[key]
	if !ok || !now.Before(c.expiresAt) {
		c = counter{expiresAt: now.Add(window)}
	}
	c.value++
	s.counters[key] = c
	return c.value, nil
}

// Block implements limiter.Storage.
func (s *Storage) Block(_ context.Context, key string, duration time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.blocks[key] = s.now().Add(duration)
	return nil
}

// IsBlocked implements limiter.Storage.
func (s *Storage) IsBlocked(_ context.Context, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	until, ok := s.blocks[key]
	if !ok {
		return false, nil
	}
	if !s.now().Before(until) {
		delete(s.blocks, key)
		return false, nil
	}
	return true, nil
}

// sweep removes expired entries so memory does not grow with every distinct
// key ever seen. It must be called with s.mu held.
func (s *Storage) sweep(now time.Time) {
	if now.Sub(s.lastSweep) < sweepInterval {
		return
	}
	s.lastSweep = now
	for k, c := range s.counters {
		if !now.Before(c.expiresAt) {
			delete(s.counters, k)
		}
	}
	for k, until := range s.blocks {
		if !now.Before(until) {
			delete(s.blocks, k)
		}
	}
}
