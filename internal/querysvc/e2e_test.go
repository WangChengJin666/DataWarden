package querysvc

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/opensourceprj/datawarden/internal/adapter"
	"github.com/opensourceprj/datawarden/internal/db"
	"github.com/opensourceprj/datawarden/internal/testutil"
)

// realEnforcer 按对象精确放行。
type realEnforcer struct {
	allow map[string]bool
}

func (r *realEnforcer) Enforce(subject, object, action string) bool { return r.allow[object] }

// 端到端验证：真实 PG + 真实鉴权 + 真实脱敏 + 真实审计
func TestExecuteSQL_EndToEndWithRealPostgres(t *testing.T) {
	testutil.SkipIfShort(t)
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	ctx := context.Background()
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer pool.Close()

	// 准备探针表
	if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS dw_e2e_probe`); err != nil {
		t.Fatalf("drop probe: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE dw_e2e_probe(id int, phone text)`); err != nil {
		t.Fatalf("create probe: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO dw_e2e_probe VALUES (1,'13812345678'),(2,'13900001111')`); err != nil {
		t.Fatalf("insert probe: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS dw_e2e_probe`)
	})

	// 构造真实 adapter，连接参数从同一个 DSN 推导
	ad, err := adapter.New(connFromPoolConfig(pool.Config()))
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	defer func() {
		if c, ok := ad.(interface{ Close() error }); ok {
			_ = c.Close()
		}
	}()

	auditor := &recordingAuditor{}
	svc := &Service{
		Enforcer: &realEnforcer{allow: map[string]bool{
			"ds:1:public:dw_e2e_probe": true,
		}},
		Auditor: auditor,
		Rules:   DefaultRules(),
		Timeout: 10 * time.Second,
	}

	res, err := svc.ExecuteSQL(ctx, SQLRequest{
		Subject:      "u:1",
		ActorID:      1,
		ActorName:    "e2e",
		DatasourceID: 1,
		Database:     "public",
		Tables:       []string{"dw_e2e_probe"},
		SQL:          "SELECT id, phone FROM dw_e2e_probe ORDER BY id",
		RowLimit:     10,
	}, ad)

	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.RowCount != 2 {
		t.Fatalf("expected 2 rows, got %d", res.RowCount)
	}
	if got := res.Rows[0][1]; got != "138****5678" {
		t.Errorf("phone should be masked, got %v", got)
	}
	if len(auditor.events) != 1 || auditor.events[0].Action != "query.execute" {
		t.Errorf("audit not recorded properly: %+v", auditor.events)
	}

	// 越权表必须被拒绝
	_, err = svc.ExecuteSQL(ctx, SQLRequest{
		Subject:      "u:1",
		DatasourceID: 1,
		Database:     "public",
		Tables:       []string{"dw_e2e_probe", "pg_shadow"},
		SQL:          "SELECT 1",
	}, ad)
	if err == nil {
		t.Error("query including an unauthorized table must be rejected")
	}
}

func connFromPoolConfig(cfg *pgxpool.Config) adapter.ConnConfig {
	return adapter.ConnConfig{
		Type:     "postgresql",
		Host:     cfg.ConnConfig.Host,
		Port:     int(cfg.ConnConfig.Port),
		Database: cfg.ConnConfig.Database,
		User:     cfg.ConnConfig.User,
		Password: cfg.ConnConfig.Password,
	}
}
