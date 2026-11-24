package querysvc

import (
	"context"
	"errors"
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
		// 鉴权粒度是「数据源 + 库」：执行时只判 ds:<id>:<库>:* 一次，
		// 不再逐表判定（见 Service.ExecuteSQL）。这里必须按库级对象放行，
		// 若仍写表级 ds:1:public:dw_e2e_probe，会直接 ERR_FORBIDDEN。
		Enforcer: &realEnforcer{allow: map[string]bool{
			"ds:1:public:*": true,
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

	// 越权边界：授权粒度是库，所以「同库内换一张表」是允许的，
	// 真正被拒绝的是**换库**。表清单不参与鉴权（只进审计），
	// 原先这里断言「带上 pg_shadow 就要被拒」已不成立——
	// 那条断言属于逐表鉴权时代，留着会让测试锁死一个已经被移除的语义。
	//
	// 用 errors.Is 而不是「err != nil」：换一个不存在的库时，
	// 若鉴权被错误地放行，执行阶段同样会因「库不存在」失败。
	// 只判 err != nil 就成了假阳性，测不出鉴权是否真的拦住了。
	// 这里要求错误链里必须有 ErrForbidden，才能证明是鉴权拦下的。
	_, err = svc.ExecuteSQL(ctx, SQLRequest{
		Subject:      "u:1",
		DatasourceID: 1,
		Database:     "no_such_granted_db",
		Tables:       []string{"dw_e2e_probe"},
		SQL:          "SELECT 1",
	}, ad)
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("未授权的库必须被鉴权拒绝（ErrForbidden），got %v", err)
	}
	// 反向确认：同库换成另一张不存在的表，必须能穿过鉴权、
	// 由数据库报「表不存在」——证明拦的是库不是表。
	_, err = svc.ExecuteSQL(ctx, SQLRequest{
		Subject:      "u:1",
		DatasourceID: 1,
		Database:     "public",
		Tables:       []string{"no_such_table"},
		SQL:          "SELECT 1 FROM no_such_table",
	}, ad)
	if errors.Is(err, ErrForbidden) {
		t.Error("同库内换表不应被鉴权拒绝：授权粒度是库，表清单不参与鉴权")
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
