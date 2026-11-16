package config

import "testing"

const validMasterKey = "0123456789abcdef0123456789abcdef" // 32 字节

func TestLoad_RequiresDatabaseURL(t *testing.T) {
	_, err := Load(func(key string) string {
		if key == "DATAWARDEN_MASTER_KEY" {
			return validMasterKey
		}
		return ""
	})
	if err == nil {
		t.Fatal("expected error when DATABASE_URL is empty")
	}
	if err.Error() != "DATABASE_URL is required" {
		t.Fatalf("unexpected error: %v", err)
	}
}

// 主密钥是必需项：缺了它密码无法解密，服务起来也没法查数据。
// 这里宁可启动失败，也不要用一个默认密钥兜底——那等于没有加密。
func TestLoad_RequiresMasterKey(t *testing.T) {
	_, err := Load(func(key string) string {
		if key == "DATABASE_URL" {
			return "postgres://localhost/datawarden"
		}
		return ""
	})
	if err == nil {
		t.Fatal("expected error when DATAWARDEN_MASTER_KEY is empty")
	}
}

func TestLoad_AppliesDefaults(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL":          "postgres://localhost/datawarden",
		"DATAWARDEN_MASTER_KEY": validMasterKey,
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
	if cfg.MasterKey != validMasterKey {
		t.Errorf("MasterKey not loaded")
	}
}

func TestLoad_OverridesFromEnv(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL":          "postgres://localhost/datawarden",
		"DATAWARDEN_MASTER_KEY": validMasterKey,
		"HTTP_ADDR":             ":9000",
		"JWT_SECRET":            "s3cret",
		"QUERY_TIMEOUT_SEC":     "15",
		"REDIS_SCAN_MAX_KEYS":   "50",
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
		"DATABASE_URL":          "postgres://localhost/datawarden",
		"DATAWARDEN_MASTER_KEY": validMasterKey,
		"QUERY_TIMEOUT_SEC":     "not-a-number",
	}
	cfg, err := Load(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.QueryTimeoutSec != 30 {
		t.Errorf("invalid int should fall back to 30, got %d", cfg.QueryTimeoutSec)
	}
}
