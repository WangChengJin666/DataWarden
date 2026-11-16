package redisq

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ParseDBIndex 解析 Redis 的 db 号。
// datasource.database 字段对 Redis 存的是 db index（如 "37"），也兼容 "host:port/37" 的写法。
// 空值视为 db 0。
func ParseDBIndex(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	// 兼容 "0" 与 "x/3" 两种写法，取最后一段
	if idx := strings.LastIndex(raw, "/"); idx >= 0 {
		raw = raw[idx+1:]
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid redis db index %q: %w", raw, err)
	}
	// 不校验上限：Redis 的 databases 数量可配置（默认 16，但可以改大），
	// 硬编码 15 会误拒合法的 db 号。越界时 SELECT 命令本身会报错。
	if n < 0 {
		return 0, fmt.Errorf("redis db index must be non-negative, got %d", n)
	}
	return n, nil
}

// QueryRequest 是一次 Redis 查询请求。
// 精确查询只看 Key；模糊查询只看 Pattern。
type QueryRequest struct {
	// Key 非空表示精确查询单个 key
	Key string
	// Pattern 非空表示模糊查询，必须带足够长的前缀
	Pattern string
	// RowLimit 控制模糊查询最多返回多少个 key
	RowLimit int
}

// QueryResponse 是 Redis 查询结果。
type QueryResponse struct {
	// Exact 为 true 时 Items 只有一条
	Exact     bool        `json:"exact"`
	Items     []*KeyValue `json:"items"`
	Truncated bool        `json:"truncated"`
	ElapsedMs int64       `json:"elapsed_ms"`
}

// Queryer 是 Redis 查询的最小接口，便于在 HTTP 层注入替身。
type Queryer interface {
	Query(ctx context.Context, req QueryRequest) (*QueryResponse, error)
}

// QueryService 编排 Redis 查询：模式校验 → 执行 → 限制数量。
type QueryService struct {
	Client  *Client
	MaxKeys int
}

// NewQueryService 构造服务。maxKeys 为 0 时用 200。
func NewQueryService(c *Client, maxKeys int) *QueryService {
	if maxKeys <= 0 {
		maxKeys = 200
	}
	return &QueryService{Client: c, MaxKeys: maxKeys}
}

// Query 执行查询。精确与模糊二选一，都填或都不填都报错。
func (s *QueryService) Query(ctx context.Context, req QueryRequest) (*QueryResponse, error) {
	start := time.Now()

	key := strings.TrimSpace(req.Key)
	pattern := strings.TrimSpace(req.Pattern)

	if key != "" && pattern != "" {
		return nil, fmt.Errorf("key and pattern are mutually exclusive")
	}
	if key == "" && pattern == "" {
		return nil, fmt.Errorf("either key or pattern is required")
	}

	if key != "" {
		if !IsExactKey(key) {
			return nil, fmt.Errorf("exact query does not accept wildcards; use pattern instead")
		}
		kv, err := s.Client.Fetch(ctx, key)
		if err != nil {
			return nil, err
		}
		return &QueryResponse{Exact: true, Items: []*KeyValue{kv}, ElapsedMs: time.Since(start).Milliseconds()}, nil
	}

	// 模糊查询：先校验模式，避免宽泛扫描
	if err := ValidatePattern(pattern); err != nil {
		return nil, err
	}

	limit := req.RowLimit
	if limit <= 0 || limit > s.MaxKeys {
		limit = s.MaxKeys
	}

	keys, truncated, err := s.Client.Scan(ctx, pattern)
	if err != nil {
		return nil, err
	}

	if len(keys) > limit {
		keys = keys[:limit]
		truncated = true
	}

	items := make([]*KeyValue, 0, len(keys))
	for _, k := range keys {
		kv, err := s.Client.Fetch(ctx, k)
		if err != nil {
			// 单个 key 读取失败（并发删除等）不应让整批失败
			items = append(items, &KeyValue{Key: k, Type: "error"})
			continue
		}
		items = append(items, kv)
	}
	return &QueryResponse{
		Exact:     false,
		Items:     items,
		Truncated: truncated,
		ElapsedMs: time.Since(start).Milliseconds(),
	}, nil
}
