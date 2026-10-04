// Command server runs an HTTP server protected by the rate limiter middleware.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"rate-limiter/internal/config"
	"rate-limiter/internal/limiter"
	"rate-limiter/internal/middleware"
	"rate-limiter/internal/storage/memory"
	redisstorage "rate-limiter/internal/storage/redis"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(".env")
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	storage, closeStorage, err := newStorage(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeStorage()

	rl, err := limiter.New(storage, limiter.Config{
		IPLimit:       cfg.IPLimit,
		TokenLimits:   cfg.TokenLimits,
		BlockDuration: cfg.BlockDuration,
	})
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("Hello, World!"))
	})

	srv := &http.Server{
		Addr:              ":" + cfg.ServerPort,
		Handler:           middleware.RateLimit(rl, middleware.Options{TrustProxyHeaders: cfg.TrustProxyHeaders})(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("server listening", "addr", srv.Addr, "storage", cfg.Storage,
			"ip_limit", cfg.IPLimit, "tokens", len(cfg.TokenLimits), "block_duration", cfg.BlockDuration)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
	}

	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}

// newStorage builds the persistence strategy selected in cfg and returns a
// function that releases its resources.
func newStorage(ctx context.Context, cfg config.Config) (limiter.Storage, func(), error) {
	switch cfg.Storage {
	case config.StorageMemory:
		return memory.New(), func() {}, nil
	case config.StorageRedis:
		client := goredis.NewClient(&goredis.Options{
			Addr:         cfg.RedisAddr,
			Password:     cfg.RedisPassword,
			DB:           cfg.RedisDB,
			DialTimeout:  3 * time.Second,
			ReadTimeout:  time.Second,
			WriteTimeout: time.Second,
		})
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := client.Ping(pingCtx).Err(); err != nil {
			_ = client.Close()
			return nil, nil, fmt.Errorf("connecting to redis at %s: %w", cfg.RedisAddr, err)
		}
		closeFn := func() {
			if err := client.Close(); err != nil {
				slog.Error("closing redis client", "error", err)
			}
		}
		return redisstorage.New(client), closeFn, nil
	default:
		return nil, nil, fmt.Errorf("unknown storage strategy %q", cfg.Storage)
	}
}
