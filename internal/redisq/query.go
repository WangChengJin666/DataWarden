package redisq

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// KeyValue 是单个 key 的查询结果。
type KeyValue struct {
	Key    string            `json:"key"`
	Type   string            `json:"type"`
	Value  string            `json:"value,omitempty"`
	Fields map[string]string `json:"fields,omitempty"`
	Items  []string          `json:"items,omitempty"`
	TTL    int64             `json:"ttl"`
}

// maxItemsPerCollection 限制 list/set/zset 单次返回的元素数，防止大集合撑爆响应。
const maxItemsPerCollection = 1000

// Client 是 Redis 查询通道。它只暴露只读查询方法，
// 不包含任何写命令包装，也不存在转发任意命令的入口。
//
// 重要：本包禁止使用 KEYS 命令（O(N) 且阻塞 Redis 主线程）。
// 该约束由 safety_test.go 的 AST 扫描护栏保证。
type Client struct {
	addr    string
	rdb     *redis.Client
	maxKeys int
}

// NewClient 建立连接并探活。maxKeys 控制模糊查询单次最多返回的 key 数量。
func NewClient(addr, password string, db, maxKeys int) (*Client, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("redis ping: %w", err)
	}

	if maxKeys <= 0 {
		maxKeys = 200
	}
	return &Client{addr: addr, rdb: rdb, maxKeys: maxKeys}, nil
}

// Addr 返回连接地址，便于日志与诊断。
func (c *Client) Addr() string { return c.addr }

func (c *Client) Close() error { return c.rdb.Close() }

// Fetch 对精确 key 按类型取值。拒绝通配符模式。
func (c *Client) Fetch(ctx context.Context, key string) (*KeyValue, error) {
	if !IsExactKey(key) {
		return nil, fmt.Errorf("Fetch requires an exact key, got pattern %q", key)
	}

	typ, err := c.rdb.Type(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("type %s: %w", key, err)
	}

	kv := &KeyValue{Key: key, Type: typ}
	if typ == "none" {
		return kv, nil
	}

	ttl, _ := c.rdb.TTL(ctx, key).Result()
	kv.TTL = int64(ttl.Seconds())

	switch typ {
	case "string":
		v, err := c.rdb.Get(ctx, key).Result()
		if err != nil && err != redis.Nil {
			return nil, fmt.Errorf("get %s: %w", key, err)
		}
		kv.Value = v
	case "hash":
		v, err := c.rdb.HGetAll(ctx, key).Result()
		if err != nil {
			return nil, fmt.Errorf("hgetall %s: %w", key, err)
		}
		kv.Fields = v
	case "list":
		v, err := c.rdb.LRange(ctx, key, 0, maxItemsPerCollection-1).Result()
		if err != nil {
			return nil, fmt.Errorf("lrange %s: %w", key, err)
		}
		kv.Items = v
	case "set":
		v, err := c.rdb.SMembers(ctx, key).Result()
		if err != nil {
			return nil, fmt.Errorf("smembers %s: %w", key, err)
		}
		if len(v) > maxItemsPerCollection {
			v = v[:maxItemsPerCollection]
		}
		kv.Items = v
	case "zset":
		v, err := c.rdb.ZRangeWithScores(ctx, key, 0, maxItemsPerCollection-1).Result()
		if err != nil {
			return nil, fmt.Errorf("zrange %s: %w", key, err)
		}
		for _, z := range v {
			kv.Items = append(kv.Items, fmt.Sprintf("%v:%v", z.Member, z.Score))
		}
	default:
		return nil, fmt.Errorf("unsupported redis type %q for key %s", typ, key)
	}
	return kv, nil
}

// Scan 用 SCAN 游标模糊匹配 key，达到上限即截断。
// 绝不使用 KEYS 命令。
func (c *Client) Scan(ctx context.Context, pattern string) (keys []string, truncated bool, err error) {
	if err := ValidatePattern(pattern); err != nil {
		return nil, false, err
	}

	var cursor uint64
	for {
		batch, next, err := c.rdb.Scan(ctx, cursor, pattern, 500).Result()
		if err != nil {
			return nil, false, fmt.Errorf("scan %s: %w", pattern, err)
		}
		for _, k := range batch {
			if len(keys) >= c.maxKeys {
				return keys, true, nil
			}
			keys = append(keys, k)
		}
		cursor = next
		if cursor == 0 {
			return keys, false, nil
		}
	}
}

// --- 以下方法仅供测试播种数据使用，不对外暴露为业务能力 ---

// SetRaw 写一个字符串 key，仅测试使用。
func (c *Client) SetRaw(ctx context.Context, key, val string, ttlSeconds int) error {
	var exp time.Duration
	if ttlSeconds > 0 {
		exp = time.Duration(ttlSeconds) * time.Second
	}
	return c.rdb.Set(ctx, key, val, exp).Err()
}

// HSetRaw 写一个 hash key，仅测试使用。
func (c *Client) HSetRaw(ctx context.Context, key string, fields map[string]string) error {
	return c.rdb.HSet(ctx, key, fields).Err()
}
