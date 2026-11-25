package db

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Open 建立连接池并做一次探活。
func Open(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	cfg.MaxConns = 10
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.ConnConfig.ConnectTimeout = 10 * time.Second

	// lock_timeout 是服务端的锁等待上限，必须设在连接级别而不是只靠客户端超时。
	//
	// 原因（真实风险）：客户端 context 超时只是「不再等结果」，PG 那边的
	// backend 仍然阻塞在 advisory lock / 行锁上，连接不会立刻回到池里。
	// 生产上 MaxConns=10，只要十个请求同时卡在等锁，整个服务的数据库访问
	// 就会一起挂住——包括与锁毫无关系的查询页。
	// 设了 lock_timeout 之后，PG 自己会在超时后抛 55P03 (lock_not_available)，
	// backend 立刻结束等待、连接可以归还。
	//
	// 取值 5s：远大于正常持锁时间（毫秒级几条 SQL），又小于用户可忍受的等待。
	// 此处只影响「等待获取锁」的语句，不会打断正在执行的长查询
	// （那是 statement_timeout 的职责，本项目的查询超时由 querysvc 按业务
	// 单独控制，不改动它）。
	//
	// 注意：迁移用的 advisory lock 故意不受此限影响——Migrate 会在自己的
	// 连接上临时放开 lock_timeout，否则多实例同时启动时，等待方会因为
	// 5 秒等不到迁移锁而直接失败，启动就崩了。
	cfg.ConnConfig.RuntimeParams["lock_timeout"] = "5000"

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// migrationLockKey 是迁移用的 advisory lock 键。
// 多个实例同时启动时只有一个能执行迁移，其余等待，避免 DDL 死锁。
const migrationLockKey int64 = 0x64657761726477 // "dewardw" 的十六进制

// Migrate 按文件名顺序执行内嵌的迁移脚本。
// 脚本本身必须幂等（CREATE TABLE IF NOT EXISTS）。
//
// 整个迁移过程持有 advisory lock：多副本同时启动时串行执行，
// 而不是并发跑 DDL 导致 deadlock。
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire conn for migration: %w", err)
	}
	defer conn.Release()

	// 迁移锁必须能长时间等待，所以先在这条连接上放开 lock_timeout。
	//
	// 连接级 lock_timeout=5s（见 Open）是为了保护运行期的账号管理锁，
	// 但迁移是例外：多个实例同时启动时，等待方可能要等几十秒等前一个
	// 跑完全部 DDL。若不放开，第二个实例会在 5 秒后收到 55P03 直接启动失败。
	//
	// 只设本连接、不设全局；conn.Release 会把连接还给池子，
	// 池里该连接会带着这个参数，但连接池取得连接时不会重置 RuntimeParams，
	// 因此这里在 defer 里显式改回，避免把「放宽」泄漏给后续的业务查询。
	if _, err := conn.Exec(ctx, "SET lock_timeout = 0"); err != nil {
		return fmt.Errorf("relax lock_timeout for migration: %w", err)
	}
	defer func() {
		resetCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(resetCtx, "SET lock_timeout = '5000'")
	}()

	// 用独立连接持有锁，确保整个迁移期间锁不释放
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockKey); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		// 用 background ctx，避免主 ctx 已取消导致锁无法释放
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(releaseCtx, "SELECT pg_advisory_unlock($1)", migrationLockKey)
	}()

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		sqlBytes, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if _, err := conn.Exec(ctx, string(sqlBytes)); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
	}
	return nil
}
