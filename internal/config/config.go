package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config 是全应用唯一的配置来源。所有模块只接受 Config，不直接读环境变量。
type Config struct {
	HTTPAddr         string
	DatabaseURL      string
	JWTSecret        string
	QueryTimeoutSec  int
	RedisScanMaxKeys int
	QueryRowLimit    int

	// 查询目标库时使用的账号。生产环境应改为按数据源从密钥管理读取。
	DBUserForQuery     string
	DBPasswordForQuery string
}

// Load 从 getenv 读取配置。getenv 可注入，便于测试。
func Load(getenv func(string) string) (*Config, error) {
	cfg := &Config{
		HTTPAddr:         orDefault(getenv("HTTP_ADDR"), ":8080"),
		DatabaseURL:      getenv("DATABASE_URL"),
		JWTSecret:        getenv("JWT_SECRET"),
		QueryTimeoutSec:  orDefaultInt(getenv("QUERY_TIMEOUT_SEC"), 30),
		RedisScanMaxKeys: orDefaultInt(getenv("REDIS_SCAN_MAX_KEYS"), 200),
		QueryRowLimit:    orDefaultInt(getenv("QUERY_ROW_LIMIT"), 500),

		DBUserForQuery:     getenv("QUERY_DB_USER"),
		DBPasswordForQuery: getenv("QUERY_DB_PASSWORD"),
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	return cfg, nil
}

// LoadFromOS 是 Load 的生产入口。
func LoadFromOS() (*Config, error) {
	return Load(os.Getenv)
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func orDefaultInt(v string, def int) int {
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
