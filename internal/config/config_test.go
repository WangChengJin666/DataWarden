package config

import "testing"

func TestLoad_RequiresDatabaseURL(t *testing.T) {
	_, err := Load(func(key string) string { return "" })
	if err == nil {
		t.Fatal("expected error when DATABASE_URL is empty")
	}
	if err.Error() != "DATABASE_URL is required" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoad_AppliesDefaults(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL": "postgres://localhost/datawarden",
	}
	cfg, err := Load(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want :8080", cfg.HTTPAddr)
	}
	if cfg.JWTSecret != "" {
		t.Errorf("JWTSecret should be empty when unset, got %q", cfg.JWTSecret)
	}
	if cfg.QueryTimeoutSec != 30 {
		t.Errorf("QueryTimeoutSec = %d, want 30", cfg.QueryTimeoutSec)
	}
	if cfg.RedisScanMaxKeys != 200 {
		t.Errorf("RedisScanMaxKeys = %d, want 200", cfg.RedisScanMaxKeys)
	}
	if cfg.QueryRowLimit != 500 {
		t.Errorf("QueryRowLimit = %d, want 500", cfg.QueryRowLimit)
	}
}

func TestLoad_OverridesFromEnv(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL":        "postgres://localhost/datawarden",
		"HTTP_ADDR":           ":9000",
		"JWT_SECRET":          "s3cret",
		"QUERY_TIMEOUT_SEC":   "15",
		"REDIS_SCAN_MAX_KEYS": "50",
	}
	cfg, err := Load(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.HTTPAddr != ":9000" {
		t.Errorf("HTTPAddr = %q, want :9000", cfg.HTTPAddr)
	}
	if cfg.JWTSecret != "s3cret" {
		t.Errorf("JWTSecret = %q, want s3cret", cfg.JWTSecret)
	}
	if cfg.QueryTimeoutSec != 15 {
		t.Errorf("QueryTimeoutSec = %d, want 15", cfg.QueryTimeoutSec)
	}
	if cfg.RedisScanMaxKeys != 50 {
		t.Errorf("RedisScanMaxKeys = %d, want 50", cfg.RedisScanMaxKeys)
	}
}

func TestLoad_InvalidIntFallsBackToDefault(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL":      "postgres://localhost/datawarden",
		"QUERY_TIMEOUT_SEC": "not-a-number",
	}
	cfg, err := Load(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.QueryTimeoutSec != 30 {
		t.Errorf("invalid int should fall back to 30, got %d", cfg.QueryTimeoutSec)
	}
}
