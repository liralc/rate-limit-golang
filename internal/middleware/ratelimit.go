// Package middleware adapts the limiter to net/http.
package middleware

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
)

// TokenHeader is the header that carries the access token.
const TokenHeader = "API_KEY"

// LimitExceededMessage is the body returned when a request is rejected.
const LimitExceededMessage = "you have reached the maximum number of requests or actions allowed within a certain time frame"

// Limiter is the behavior the middleware needs from the rate limiter.
type Limiter interface {
	Allow(ctx context.Context, ip, token string) (bool, error)
}

// Options configures the RateLimit middleware.
type Options struct {
	// TrustProxyHeaders makes the client IP be read from X-Forwarded-For or
	// X-Real-IP. Enable it only behind a trusted reverse proxy, otherwise
	// clients can spoof their IP.
	TrustProxyHeaders bool
	// Logger receives limiter errors. Nil means slog.Default().
	Logger *slog.Logger
}

// RateLimit returns a middleware that rejects requests with 429 when l does
// not allow them. If the limiter fails, the request is rejected with 500.
func RateLimit(l Limiter, opts Options) func(http.Handler) http.Handler {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := ClientIP(r, opts.TrustProxyHeaders)
			token := strings.TrimSpace(r.Header.Get(TokenHeader))

			allowed, err := l.Allow(r.Context(), ip, token)
			if err != nil {
				logger.ErrorContext(r.Context(), "rate limiter failure", "ip", ip, "error", err)
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				return
			}
			if !allowed {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(LimitExceededMessage))
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// ClientIP returns the IP address of the client that sent r.
func ClientIP(r *http.Request, trustProxyHeaders bool) string {
	if trustProxyHeaders {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first, _, _ := strings.Cut(xff, ",")
			if ip := strings.TrimSpace(first); ip != "" {
				return ip
			}
		}
		if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" {
			return ip
		}
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
