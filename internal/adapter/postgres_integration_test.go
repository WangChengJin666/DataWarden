package adapter

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/opensourceprj/datawarden/internal/testutil"
)

// pgConnFromEnv 从 TEST_DATABASE_URL 解析出连接参数。
func pgConnFromEnv(t *testing.T) ConnConfig {
	t.Helper()
	testutil.SkipIfShort(t)
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	// 形如 postgres://user:pass@host:port/db?sslmode=disable
	s := strings.TrimPrefix(dsn, "postgres://")
	s = strings.TrimPrefix(s, "postgresql://")

	at := strings.LastIndex(s, "@")
	if at < 0 {
		t.Fatalf("cannot parse TEST_DATABASE_URL")
	}
	userInfo := s[:at]
	rest := s[at+1:]

	colon := strings.Index(userInfo, ":")
	user, pass := userInfo, ""
	if colon > 0 {
		user = userInfo[:colon]
		pass = userInfo[colon+1:]
	}

	if idx := strings.Index(rest, "?"); idx > 0 {
		rest = rest[:idx]
	}
	slash := strings.Index(rest, "/")
	if slash < 0 {
		t.Fatalf("cannot parse database name")
	}
	hostPort := rest[:slash]
	dbName := rest[slash+1:]

	host, port := hostPort, 5432
	if c := strings.LastIndex(hostPort, ":"); c > 0 {
		host = hostPort[:c]
		// 端口必须真的解析出来。这里曾写成 switch + 两个都赋值 5432 的分支，
		// 效果等于忽略 URL 里的端口：TEST_DATABASE_URL 指向非默认端口的
		// 环境（例如容器映射的 31567）会一直连 5432，报
		// "actively refused" 并让人误判为数据库没起。
		raw := hostPort[c+1:]
		p, err := strconv.Atoi(raw)
		if err != nil || p <= 0 || p > 65535 {
			t.Fatalf("TEST_DATABASE_URL 里的端口 %q 非法", raw)
		}
		port = p
	}

	return ConnConfig{
		Type:     "postgresql",
		Host:     host,
		Port:     port,
		Database: dbName,
		User:     user,
		Password: pass,
	}
}

func TestPostgresAdapter_Integration(t *testing.T) {
	cfg := pgConnFromEnv(t)
	ad, err := New(cfg)
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	defer ad.(*pgAdapter).Close()

	ctx := context.Background()

	if err := ad.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	dbs, err := ad.ListDatabases(ctx)
	if err != nil {
		t.Fatalf("list databases: %v", err)
	}
	if len(dbs) == 0 {
		t.Error("expected at least one database")
	}

	// 执行一个简单查询，验证列名与行数据
	res, err := ad.Query(ctx, "SELECT 1 AS a, 'hello' AS b", 10)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(res.Columns) != 2 {
		t.Fatalf("expected 2 columns, got %v", res.Columns)
	}
	if res.RowCount != 1 {
		t.Fatalf("expected 1 row, got %d", res.RowCount)
	}
	// pgx 对 SELECT 1 返回 int32
	if v := res.Rows[0][0]; v != int32(1) && v != int64(1) {
		t.Errorf("first col = %v (%T), want 1", v, v)
	}
	if v := res.Rows[0][1]; v != "hello" {
		t.Errorf("second col = %v, want hello", v)
	}
}

func TestPostgresAdapter_RowLimitApplied(t *testing.T) {
	cfg := pgConnFromEnv(t)
	ad, err := New(cfg)
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	defer ad.(*pgAdapter).Close()

	ctx := context.Background()

	// 生成 100 行，限制取 10 行
	res, err := ad.Query(ctx,
		"SELECT generate_series(1, 100) AS n", 10)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !res.Truncated {
		t.Error("expected Truncated=true when result exceeds rowLimit")
	}
	if res.RowCount != 10 {
		t.Errorf("expected 10 rows, got %d", res.RowCount)
	}
}

func TestPostgresAdapter_QueryTimeout(t *testing.T) {
	cfg := pgConnFromEnv(t)
	ad, err := New(cfg)
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	defer ad.(*pgAdapter).Close()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	// pg_sleep 5 秒，1 秒超时必须中断
	_, err = ad.Query(ctx, "SELECT pg_sleep(5)", 10)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}
