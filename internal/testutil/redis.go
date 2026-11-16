package testutil

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
)

// TestKeyPrefix 是所有测试 key 的统一前缀，便于清理与隔离。
const TestKeyPrefix = "dwtest:"

// RedisAddr 从环境变量读取测试 Redis 地址，未配置则跳过测试。
// 支持 host:port/db 写法，返回值不含 /db 部分。
func RedisAddr(t *testing.T) string {
	t.Helper()
	raw := os.Getenv("TEST_REDIS_ADDR")
	if raw == "" {
		t.Skip("TEST_REDIS_ADDR not set; skipping redis integration test")
	}
	if idx := strings.LastIndex(raw, "/"); idx > 0 {
		return raw[:idx]
	}
	return raw
}

// RedisDB 从 TEST_REDIS_ADDR 的 /N 后缀解析出 db 号，默认 0。
func RedisDB(t *testing.T) int {
	t.Helper()
	raw := os.Getenv("TEST_REDIS_ADDR")
	idx := strings.LastIndex(raw, "/")
	if idx <= 0 {
		return 0
	}
	var db int
	if _, err := fmt.Sscanf(raw[idx+1:], "%d", &db); err != nil {
		return 0
	}
	return db
}

// RedisPassword 从环境变量读取 Redis 密码，无密码返回空串。
func RedisPassword() string {
	return os.Getenv("TEST_REDIS_PASSWORD")
}

// CleanTestKeys 删除所有 dwtest: 前缀的 key，测试前后各调一次保证幂等。
// 只删这个前缀，绝不触碰共享 Redis 上的其他数据。
func CleanTestKeys(t *testing.T, addr string) {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: RedisPassword(),
		DB:       RedisDB(t),
	})
	defer rdb.Close()

	ctx := t.Context()
	iter := rdb.Scan(ctx, 0, TestKeyPrefix+"*", 500).Iterator()
	for iter.Next(ctx) {
		_ = rdb.Del(ctx, iter.Val()).Err()
	}
}
