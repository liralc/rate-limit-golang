package memory

import (
	"context"
	"testing"
	"time"
)

func TestStorage_IncrementExpires(t *testing.T) {
	now := time.Unix(0, 0)
	s := NewWithClock(func() time.Time { return now })
	ctx := context.Background()

	for want := int64(1); want <= 3; want++ {
		got, err := s.Increment(ctx, "k", time.Second)
		if err != nil || got != want {
			t.Fatalf("Increment = %d, %v; want %d", got, err, want)
		}
	}

	now = now.Add(time.Second)
	if got, _ := s.Increment(ctx, "k", time.Second); got != 1 {
		t.Fatalf("after window Increment = %d, want 1", got)
	}
}

func TestStorage_Block(t *testing.T) {
	now := time.Unix(0, 0)
	s := NewWithClock(func() time.Time { return now })
	ctx := context.Background()

	if blocked, _ := s.IsBlocked(ctx, "k"); blocked {
		t.Fatal("unknown key reported as blocked")
	}
	if err := s.Block(ctx, "k", time.Minute); err != nil {
		t.Fatal(err)
	}
	if blocked, _ := s.IsBlocked(ctx, "k"); !blocked {
		t.Fatal("key not blocked")
	}
	now = now.Add(time.Minute)
	if blocked, _ := s.IsBlocked(ctx, "k"); blocked {
		t.Fatal("key still blocked after duration")
	}
}

func TestStorage_SweepRemovesExpiredEntries(t *testing.T) {
	now := time.Unix(0, 0)
	s := NewWithClock(func() time.Time { return now })
	ctx := context.Background()

	_, _ = s.Increment(ctx, "old", time.Second)
	_ = s.Block(ctx, "old", time.Second)

	now = now.Add(sweepInterval)
	_, _ = s.Increment(ctx, "new", time.Second)

	if _, ok := s.counters["old"]; ok {
		t.Fatal("expired counter was not swept")
	}
	if _, ok := s.blocks["old"]; ok {
		t.Fatal("expired block was not swept")
	}
	if _, ok := s.counters["new"]; !ok {
		t.Fatal("live counter was removed")
	}
}
