package middleware_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"rate-limiter/internal/limiter"
	"rate-limiter/internal/middleware"
	redisstorage "rate-limiter/internal/storage/redis"
)

type stubLimiter struct {
	allowed   bool
	err       error
	gotIP     string
	gotToken  string
	callCount int
}

func (s *stubLimiter) Allow(_ context.Context, ip, token string) (bool, error) {
	s.callCount++
	s.gotIP, s.gotToken = ip, token
	return s.allowed, s.err
}

var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	_, _ = io.WriteString(w, "ok")
})

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestRateLimit_Allowed(t *testing.T) {
	stub := &stubLimiter{allowed: true}
	h := middleware.RateLimit(stub, middleware.Options{})(okHandler)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.7:51234"
	req.Header.Set("API_KEY", " abc123 ")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("got %d %q, want 200 ok", rec.Code, rec.Body.String())
	}
	if stub.gotIP != "203.0.113.7" || stub.gotToken != "abc123" {
		t.Fatalf("limiter got ip=%q token=%q", stub.gotIP, stub.gotToken)
	}
}

func TestRateLimit_Rejected(t *testing.T) {
	h := middleware.RateLimit(&stubLimiter{allowed: false}, middleware.Options{})(okHandler)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if rec.Body.String() != middleware.LimitExceededMessage {
		t.Fatalf("body = %q, want %q", rec.Body.String(), middleware.LimitExceededMessage)
	}
}

func TestRateLimit_LimiterError(t *testing.T) {
	h := middleware.RateLimit(&stubLimiter{err: errors.New("redis down")},
		middleware.Options{Logger: quietLogger()})(okHandler)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestClientIP(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		headers    map[string]string
		trust      bool
		want       string
	}{
		{"remote addr ipv4", "10.0.0.1:1234", nil, false, "10.0.0.1"},
		{"remote addr ipv6", "[2001:db8::1]:1234", nil, false, "2001:db8::1"},
		{"remote addr without port", "10.0.0.1", nil, false, "10.0.0.1"},
		{"proxy headers ignored when untrusted", "10.0.0.1:1234", map[string]string{"X-Forwarded-For": "1.1.1.1"}, false, "10.0.0.1"},
		{"x-forwarded-for first hop", "10.0.0.1:1234", map[string]string{"X-Forwarded-For": "1.1.1.1, 2.2.2.2"}, true, "1.1.1.1"},
		{"x-real-ip", "10.0.0.1:1234", map[string]string{"X-Real-IP": "3.3.3.3"}, true, "3.3.3.3"},
		{"trusted without headers", "10.0.0.1:1234", nil, true, "10.0.0.1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tc.remoteAddr
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			if got := middleware.ClientIP(req, tc.trust); got != tc.want {
				t.Fatalf("ClientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRateLimit_EndToEnd exercises the whole stack over real HTTP: middleware,
// limiter and the Redis strategy (backed by an in-process Redis server).
func TestRateLimit_EndToEnd(t *testing.T) {
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	l, err := limiter.New(redisstorage.New(client), limiter.Config{
		IPLimit:       10,
		TokenLimits:   map[string]int64{"abc123": 100},
		BlockDuration: 5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(middleware.RateLimit(l, middleware.Options{})(okHandler))
	t.Cleanup(srv.Close)

	do := func(token string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("API_KEY", token)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, string(body)
	}
	count := func(n int, token string) (ok, limited int) {
		t.Helper()
		for range n {
			switch code, body := do(token); code {
			case http.StatusOK:
				ok++
			case http.StatusTooManyRequests:
				if body != middleware.LimitExceededMessage {
					t.Fatalf("429 body = %q", body)
				}
				limited++
			default:
				t.Fatalf("unexpected status %d", code)
			}
		}
		return ok, limited
	}

	// IP limit: 10 req/s.
	if ok, limited := count(15, ""); ok != 10 || limited != 5 {
		t.Fatalf("IP: ok=%d limited=%d, want 10/5", ok, limited)
	}

	// Token precedence: 100 req/s even though the same IP is blocked.
	if ok, limited := count(105, "abc123"); ok != 100 || limited != 5 {
		t.Fatalf("token: ok=%d limited=%d, want 100/5", ok, limited)
	}

	// Blocks survive new windows and are lifted after the block duration.
	mr.FastForward(time.Minute)
	if ok, _ := count(1, ""); ok != 0 {
		t.Fatal("IP unblocked before block duration")
	}
	if ok, _ := count(1, "abc123"); ok != 0 {
		t.Fatal("token unblocked before block duration")
	}
	mr.FastForward(4 * time.Minute)
	if ok, _ := count(10, ""); ok != 10 {
		t.Fatalf("IP after block allowed %d, want 10", ok)
	}
	if ok, _ := count(100, "abc123"); ok != 100 {
		t.Fatalf("token after block allowed %d, want 100", ok)
	}
}
