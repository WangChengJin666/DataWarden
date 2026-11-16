package adapter

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// ProbeConfig 是连接探测所需的信息，与 ConnConfig 字段一致，
// 单独定义是为了让 redis 类型也能走同一入口。
type ProbeConfig struct {
	Type     string
	Host     string
	Port     int
	Database string
	User     string
	Password string
}

// ProbeResult 是探测结果。
type ProbeResult struct {
	OK        bool
	Message   string
	ElapsedMs int64
}

// Probe 实际连一次目标库确认配置可用。
//
// 设计取舍：这里不走 Adapter 接口，因为 Redis 不是 SQL 数据源，
// 而「测试连接」需要覆盖全部四种类型。所以按类型分别探测，
// 但统一返回口径一致的结果。
func Probe(ctx context.Context, cfg ProbeConfig) (*ProbeResult, error) {
	start := time.Now()

	switch cfg.Type {
	case "redis":
		if err := probeRedis(ctx, cfg); err != nil {
			return nil, err
		}
		return &ProbeResult{
			OK:        true,
			Message:   "连接成功",
			ElapsedMs: time.Since(start).Milliseconds(),
		}, nil

	case "mysql", "postgresql":
		a, err := New(ConnConfig{
			Type:     cfg.Type,
			Host:     cfg.Host,
			Port:     cfg.Port,
			Database: cfg.Database,
			User:     cfg.User,
			Password: cfg.Password,
		})
		if err != nil {
			return nil, err
		}
		defer a.Close()

		if err := a.Ping(ctx); err != nil {
			return nil, err
		}
		return &ProbeResult{
			OK:        true,
			Message:   "连接成功",
			ElapsedMs: time.Since(start).Milliseconds(),
		}, nil

	case "mongodb":
		// MongoDB 适配器还没实现，这里明确拒绝而不是假装成功。
		// 假装成功会让用户以为配好了，实际查询时才失败，更难排查。
		return nil, fmt.Errorf("mongodb 暂不支持连接测试（该类型的查询适配器尚未实现）")

	default:
		return nil, fmt.Errorf("unsupported datasource type: %s", cfg.Type)
	}
}

// probeRedis 连接并 PING 一次，同时校验 db 号。
func probeRedis(ctx context.Context, cfg ProbeConfig) error {
	dbIndex := 0
	if cfg.Database != "" {
		n, err := strconv.Atoi(cfg.Database)
		if err != nil {
			return fmt.Errorf("redis db 号必须是整数，当前为 %q", cfg.Database)
		}
		if n < 0 {
			return fmt.Errorf("redis db 号不能为负数")
		}
		dbIndex = n
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Password: cfg.Password,
		DB:       dbIndex,
	})
	defer rdb.Close()

	if err := rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis ping: %w", err)
	}
	return nil
}
