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

	// MasterKey 是加密数据源密码的主密钥（AES-128/192/256）。
	// 必须与数据库分开保管：数据库丢了但主密钥还在，密码不会泄露。
	MasterKey string

	// DBUserForQuery / DBPasswordForQuery 是旧版全局查询账号，现已废弃。
	// 数据源改为各自保存账号密码，这两个字段仅为兼容旧 .env 保留。
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

		MasterKey: getenv("DATAWARDEN_MASTER_KEY"),

		DBUserForQuery:     getenv("QUERY_DB_USER"),
		DBPasswordForQuery: getenv("QUERY_DB_PASSWORD"),
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.MasterKey == "" {
		return nil, fmt.Errorf("DATAWARDEN_MASTER_KEY is required (16/24/32 bytes); it encrypts datasource passwords")
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
