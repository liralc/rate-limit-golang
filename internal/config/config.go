// Package config loads the application settings from environment variables,
// optionally seeded by a .env file.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Supported storage strategies.
const (
	StorageRedis  = "redis"
	StorageMemory = "memory"
)

// Config holds every application setting.
type Config struct {
	ServerPort        string
	IPLimit           int64
	TokenLimits       map[string]int64
	BlockDuration     time.Duration
	TrustProxyHeaders bool

	Storage       string
	RedisAddr     string
	RedisPassword string
	RedisDB       int
}

// Load reads the .env file at path, if it exists, and then the environment.
// Variables already present in the environment take precedence over the file.
func Load(path string) (Config, error) {
	if err := godotenv.Load(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("config: loading %s: %w", path, err)
	}
	return FromEnv(os.Getenv)
}

// FromEnv builds a Config using getenv to read variables.
func FromEnv(getenv func(string) string) (Config, error) {
	get := func(key, fallback string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return fallback
	}

	var errs []error

	ipLimit, err := strconv.ParseInt(get("RATE_LIMIT_IP", "10"), 10, 64)
	if err != nil || ipLimit <= 0 {
		errs = append(errs, fmt.Errorf("RATE_LIMIT_IP must be a positive integer"))
	}

	tokenLimits, err := parseTokenLimits(get("RATE_LIMIT_TOKENS", ""))
	if err != nil {
		errs = append(errs, err)
	}

	blockDuration, err := time.ParseDuration(get("BLOCK_DURATION", "5m"))
	if err != nil || blockDuration <= 0 {
		errs = append(errs, fmt.Errorf("BLOCK_DURATION must be a positive duration such as 30s or 5m"))
	}

	trustProxy, err := strconv.ParseBool(get("TRUST_PROXY_HEADERS", "false"))
	if err != nil {
		errs = append(errs, fmt.Errorf("TRUST_PROXY_HEADERS must be a boolean"))
	}

	storage := strings.ToLower(get("STORAGE_STRATEGY", StorageRedis))
	if storage != StorageRedis && storage != StorageMemory {
		errs = append(errs, fmt.Errorf("STORAGE_STRATEGY must be %q or %q", StorageRedis, StorageMemory))
	}

	redisDB, err := strconv.Atoi(get("REDIS_DB", "0"))
	if err != nil || redisDB < 0 {
		errs = append(errs, fmt.Errorf("REDIS_DB must be a non-negative integer"))
	}

	if err := errors.Join(errs...); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}

	return Config{
		ServerPort:        get("SERVER_PORT", "8080"),
		IPLimit:           ipLimit,
		TokenLimits:       tokenLimits,
		BlockDuration:     blockDuration,
		TrustProxyHeaders: trustProxy,
		Storage:           storage,
		RedisAddr:         get("REDIS_ADDR", "localhost:6379"),
		RedisPassword:     getenv("REDIS_PASSWORD"),
		RedisDB:           redisDB,
	}, nil
}

// parseTokenLimits parses "token1:100,token2:50" into a map.
func parseTokenLimits(raw string) (map[string]int64, error) {
	limits := make(map[string]int64)
	if raw == "" {
		return limits, nil
	}
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		token, rawLimit, ok := strings.Cut(pair, ":")
		token = strings.TrimSpace(token)
		limit, err := strconv.ParseInt(strings.TrimSpace(rawLimit), 10, 64)
		if !ok || token == "" || err != nil || limit <= 0 {
			return nil, fmt.Errorf("RATE_LIMIT_TOKENS entry %q must be in the form token:limit with a positive limit", pair)
		}
		if _, dup := limits[token]; dup {
			return nil, fmt.Errorf("RATE_LIMIT_TOKENS has duplicated token %q", token)
		}
		limits[token] = limit
	}
	return limits, nil
}
