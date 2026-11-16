package db

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/opensourceprj/datawarden/internal/testutil"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	testutil.SkipIfShort(t)
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestMigrate_IsIdempotent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("second migrate should be idempotent, got: %v", err)
	}

	var count int
	err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables
		 WHERE table_schema='public'
		   AND table_name IN ('app_user','datasource','grant_record','audit_log')`,
	).Scan(&count)
	if err != nil {
		t.Fatalf("query tables: %v", err)
	}
	if count != 4 {
		t.Fatalf("expected 4 core tables, got %d", count)
	}
}

// TestMigrate_ConcurrentIsSafe 模拟多个实例同时启动：
// advisory lock 应让迁移串行执行，不出现 DDL 死锁。
func TestMigrate_ConcurrentIsSafe(t *testing.T) {
	testutil.SkipIfShort(t)
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()

	const n = 5
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			pool, err := Open(ctx, dsn)
			if err != nil {
				errs <- err
				return
			}
			defer pool.Close()
			errs <- Migrate(ctx, pool)
		}()
	}

	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent migrate %d/%d failed: %v", i+1, n, err)
		}
	}
}

func TestMigrate_AuditLogIsAppendOnly(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// 插入一条审计记录
	var id int64
	err := pool.QueryRow(ctx,
		`INSERT INTO audit_log (action, object) VALUES ('test.trigger', 'x') RETURNING id`,
	).Scan(&id)
	if err != nil {
		t.Fatalf("insert audit: %v", err)
	}

	// UPDATE 必须被触发器拒绝
	if _, err := pool.Exec(ctx, `UPDATE audit_log SET action = 'hacked' WHERE id = $1`, id); err == nil {
		t.Fatal("UPDATE on audit_log must be rejected by trigger")
	}

	// DELETE 必须被触发器拒绝
	if _, err := pool.Exec(ctx, `DELETE FROM audit_log WHERE id = $1`, id); err == nil {
		t.Fatal("DELETE on audit_log must be rejected by trigger")
	}

	// 原记录仍在且未被修改
	var action string
	if err := pool.QueryRow(ctx, `SELECT action FROM audit_log WHERE id = $1`, id).Scan(&action); err != nil {
		t.Fatalf("select audit: %v", err)
	}
	if action != "test.trigger" {
		t.Errorf("action should be unchanged, got %q", action)
	}
}
