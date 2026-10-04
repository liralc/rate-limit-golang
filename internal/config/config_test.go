package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestFromEnv_Defaults(t *testing.T) {
	cfg, err := FromEnv(envFrom(nil))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		ServerPort:    "8080",
		IPLimit:       10,
		TokenLimits:   map[string]int64{},
		BlockDuration: 5 * time.Minute,
		Storage:       StorageRedis,
		RedisAddr:     "localhost:6379",
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
}

func TestFromEnv_Custom(t *testing.T) {
	cfg, err := FromEnv(envFrom(map[string]string{
		"SERVER_PORT":         "9090",
		"RATE_LIMIT_IP":       "5",
		"RATE_LIMIT_TOKENS":   "abc123:100, def456:50 ,",
		"BLOCK_DURATION":      "30s",
		"TRUST_PROXY_HEADERS": "true",
		"STORAGE_STRATEGY":    "MEMORY",
		"REDIS_ADDR":          "redis:6379",
		"REDIS_PASSWORD":      "secret",
		"REDIS_DB":            "2",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		ServerPort:        "9090",
		IPLimit:           5,
		TokenLimits:       map[string]int64{"abc123": 100, "def456": 50},
		BlockDuration:     30 * time.Second,
		TrustProxyHeaders: true,
		Storage:           StorageMemory,
		RedisAddr:         "redis:6379",
		RedisPassword:     "secret",
		RedisDB:           2,
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
}

func TestFromEnv_Invalid(t *testing.T) {
	tests := map[string]map[string]string{
		"ip limit not a number": {"RATE_LIMIT_IP": "ten"},
		"ip limit zero":         {"RATE_LIMIT_IP": "0"},
		"token without limit":   {"RATE_LIMIT_TOKENS": "abc123"},
		"token negative limit":  {"RATE_LIMIT_TOKENS": "abc123:-1"},
		"token empty name":      {"RATE_LIMIT_TOKENS": ":10"},
		"token duplicated":      {"RATE_LIMIT_TOKENS": "a:1,a:2"},
		"block duration":        {"BLOCK_DURATION": "5"},
		"block duration zero":   {"BLOCK_DURATION": "0s"},
		"trust proxy":           {"TRUST_PROXY_HEADERS": "maybe"},
		"storage":               {"STORAGE_STRATEGY": "postgres"},
		"redis db":              {"REDIS_DB": "-1"},
	}
	for name, env := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := FromEnv(envFrom(env)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestLoad_DotEnvDoesNotOverrideEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("RATE_LIMIT_IP=7\nBLOCK_DURATION=1m\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RATE_LIMIT_IP", "3")
	t.Setenv("BLOCK_DURATION", "") // ensure restored after the test
	os.Unsetenv("BLOCK_DURATION")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IPLimit != 3 {
		t.Fatalf("IPLimit = %d, want 3 from environment", cfg.IPLimit)
	}
	if cfg.BlockDuration != time.Minute {
		t.Fatalf("BlockDuration = %v, want 1m from .env", cfg.BlockDuration)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.env")); err != nil {
		t.Fatalf("missing .env must be ignored, got %v", err)
	}
}
