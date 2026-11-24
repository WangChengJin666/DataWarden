package redisq

import (
	"context"
	"fmt"
	"strconv"
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

	// Encoding 仅对 string 类型有意义，标明内容是真文本还是二进制。
	// 缺省（空串）等价于 text，兼容老版本前端的调用。
	Encoding Encoding `json:"encoding,omitempty"`
	// Hex 是二进制内容的十六进制预览，仅 Encoding=binary 时填充。
	Hex string `json:"hex,omitempty"`
	// Size 是二进制内容的原始字节数，用于提示「已截断」。
	Size int `json:"size,omitempty"`
	// BitCount / SetBits 是「疑似位图」的参考信息：置位总数与前若干个置位下标。
	// 位图与普通二进制无法可靠区分，所以这两个值只在长度较短时给出，
	// 由用户自行判断这段二进制是不是 SETBIT 写出来的。
	BitCount int    `json:"bit_count,omitempty"`
	SetBits  string `json:"set_bits,omitempty"`
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
func NewClient(addr, username, password string, db, maxKeys int) (*Client, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr: addr,
		// Redis 6+ 的 ACL 需要用户名。留空时客户端发 AUTH <password>，
		// 兼容只有 requirepass 的老实例；填了则发 AUTH <user> <password>。
		Username: username,
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
		// 用 Bytes 而不是 String：JSON 序列化会替换非法 UTF-8 序列，
		// bitmap / HLL 这类二进制内容走 String 会变成 \ufffd 乱码且无法还原。
		raw, err := c.rdb.Get(ctx, key).Bytes()
		if err != nil && err != redis.Nil {
			return nil, fmt.Errorf("get %s: %w", key, err)
		}
		kv.Encoding = detectStringEncoding(raw)
		if kv.Encoding == EncodingText {
			kv.Value = string(raw)
			break
		}
		// 二进制：不返回原始内容（前端无法展示，还会污染响应体），
		// 改给十六进制预览与长度，前端据此渲染可读视图。
		kv.Size = len(raw)
		kv.Hex, _ = FormatBytes(raw, 256)
		// 位图参考值只给「未知二进制」：HLL 的字节是内部编码结构，
		// 把它按位统计出来只会误导用户以为这是位图。
		if kv.Encoding == EncodingBinary && len(raw) <= 32 {
			count, positions := bitmapSetBits(raw, 64)
			kv.BitCount = count
			kv.SetBits = joinInts(positions)
		}
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
		// 元素形态固定为 "member:score"。member 本身可能含冒号，
		// 前端应按**最后一个**冒号切分，不要按第一个切。
		// 分数用 -1 精度格式化：用 %v 会把 12 打成 "12"、12.5 打成 "12.5" 看着还行，
		// 但 0.1+0.2 这类浮点误差会露出长尾（0.30000000000000004）。
		for _, z := range v {
			kv.Items = append(kv.Items, fmt.Sprintf("%v:%s", z.Member, strconv.FormatFloat(z.Score, 'f', -1, 64)))
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
