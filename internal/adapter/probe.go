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
	// AuthSource 仅 MongoDB 使用：账号所在的认证库，留空回落 admin。
	AuthSource string
	// Options 连接高级参数（MongoDB 的 replicaSet/tls/readPreference 等），
	// 测试连接必须和正式查询用同一套参数，否则「测通了、用不了」。
	Options map[string]string
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

	case "mysql", "postgresql", "mongodb":
		// PostgreSQL 的库名是连接串的一部分，留空会连到与用户名同名的库并失败，
		// 与其抛出难懂的 "database does not exist"，不如提前给出准确提示。
		// MySQL 允许不选库（DSN 末尾为空），所以不作这个要求。
		// MongoDB 必须知道默认库才能确定连哪个库，同样必填。
		if cfg.Type == "postgresql" && cfg.Database == "" {
			return nil, fmt.Errorf("PostgreSQL 必须填写库名，否则无法建立连接")
		}
		if cfg.Type == "mongodb" && cfg.Database == "" {
			return nil, fmt.Errorf("MongoDB 必须填写默认库，否则无法确定要查哪个库")
		}

		a, err := New(ConnConfig{
			Type:       cfg.Type,
			Host:       cfg.Host,
			Port:       cfg.Port,
			Database:   cfg.Database,
			User:       cfg.User,
			Password:   cfg.Password,
			AuthSource: cfg.AuthSource,
			Options:    cfg.Options,
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
		Addr: fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		// Redis 6 之前的实例只有 requirepass，填了 password 但没填账号时
		// 会走 default 用户认证（这一层由服务端处理，客户端传空 Username 即可）。
		// Redis 6+ 的 ACL 与云厂商托管实例普遍要求用户名，
		// 之前这里不传 Username，填了账号也会被静默丢掉，
		// 现象是「测试连接失败，但报错和账号毫无关系」。
		Username: cfg.User,
		Password: cfg.Password,
		DB:       dbIndex,
	})
	defer rdb.Close()

	if err := rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis ping: %w", err)
	}
	return nil
}
