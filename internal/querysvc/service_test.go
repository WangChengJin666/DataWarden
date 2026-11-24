package querysvc

import (
	"context"
	"errors"
	"testing"

	"github.com/opensourceprj/datawarden/internal/adapter"
)

// stubAdapter 返回固定结果，用于隔离数据库依赖。
type stubAdapter struct {
	result *adapter.QueryResult
	err    error
	called bool
	gotSQL string
	gotLim int
}

func (s *stubAdapter) Ping(ctx context.Context) error { return nil }

// Close 是 adapter.Adapter 接口的一部分。替身也要实现，
// 否则接口不匹配——真实适配器会释放连接池，替身没有资源可释放。
func (s *stubAdapter) Close() error { return nil }
func (s *stubAdapter) ListDatabases(ctx context.Context) ([]string, error) {
	return []string{"shop"}, nil
}
func (s *stubAdapter) ListTables(ctx context.Context, db string) ([]string, error) {
	return []string{"orders"}, nil
}
func (s *stubAdapter) Query(ctx context.Context, sql string, limit int) (*adapter.QueryResult, error) {
	s.called = true
	s.gotSQL = sql
	s.gotLim = limit
	// 模拟真实 adapter 的行为：负责计算 RowCount
	if s.result != nil && s.err == nil {
		s.result.RowCount = len(s.result.Rows)
	}
	return s.result, s.err
}

// QueryWithArgs 只给元数据接口用，业务查询永远不会走到这里。
// 实现成同样的行为，保证替身完整实现 adapter.Adapter。
func (s *stubAdapter) QueryWithArgs(ctx context.Context, sql string, args []any, limit int) (*adapter.QueryResult, error) {
	return s.Query(ctx, sql, limit)
}

type recordingEnforcer struct {
	allowed bool
	lastObj string
}

func (r *recordingEnforcer) Enforce(subject, object, action string) bool {
	r.lastObj = object
	return r.allowed
}

// tableScopedEnforcer 按对象精确判定，用于多表场景测试。
type tableScopedEnforcer struct {
	allowed map[string]bool
	checked []string
}

func (t *tableScopedEnforcer) Enforce(subject, object, action string) bool {
	t.checked = append(t.checked, object)
	return t.allowed[object]
}

type recordingAuditor struct {
	events []AuditEvent
}

func (r *recordingAuditor) Record(ctx context.Context, e AuditEvent) error {
	r.events = append(r.events, e)
	return nil
}

func TestExecuteSQL_DeniedWhenNoGrant(t *testing.T) {
	enforcer := &recordingEnforcer{allowed: false}
	auditor := &recordingAuditor{}
	svc := &Service{Enforcer: enforcer, Auditor: auditor}

	_, err := svc.ExecuteSQL(context.Background(), SQLRequest{
		Subject:      "u:1",
		DatasourceID: 7,
		Database:     "shop",
		Tables:       []string{"orders"},
		SQL:          "SELECT * FROM orders",
	}, &stubAdapter{result: &adapter.QueryResult{}})

	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
	if len(auditor.events) != 1 || auditor.events[0].Action != "query.denied" {
		t.Errorf("denied query must be audited, got %+v", auditor.events)
	}
}

func TestExecuteSQL_DeniedWhenAnyTableMissing(t *testing.T) {
	// 只有 orders 有权限，users 没有 → 整条拒绝
	enforcer := &tableScopedEnforcer{allowed: map[string]bool{"ds:7:shop:orders": true}}
	svc := &Service{Enforcer: enforcer, Auditor: &recordingAuditor{}}
	stub := &stubAdapter{result: &adapter.QueryResult{}}

	_, err := svc.ExecuteSQL(context.Background(), SQLRequest{
		Subject:      "u:1",
		DatasourceID: 7,
		Database:     "shop",
		Tables:       []string{"orders", "users"},
		SQL:          "SELECT * FROM orders JOIN users ON 1=1",
	}, stub)

	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden when any table lacks grant, got %v", err)
	}
	if stub.called {
		t.Error("adapter must not be called when authorization fails")
	}
}

func TestExecuteSQL_EmptyTablesRejected(t *testing.T) {
	svc := &Service{Enforcer: &recordingEnforcer{allowed: true}, Auditor: &recordingAuditor{}}
	_, err := svc.ExecuteSQL(context.Background(), SQLRequest{
		Subject:      "u:1",
		DatasourceID: 7,
		Database:     "shop",
		Tables:       nil,
		SQL:          "SELECT * FROM orders",
	}, &stubAdapter{result: &adapter.QueryResult{}})

	if !errors.Is(err, ErrNoTables) {
		t.Fatalf("expected ErrNoTables, got %v", err)
	}
}

func TestExecuteSQL_RejectsWriteStatement(t *testing.T) {
	svc := &Service{Enforcer: &recordingEnforcer{allowed: true}, Auditor: &recordingAuditor{}}
	stub := &stubAdapter{result: &adapter.QueryResult{}}

	_, err := svc.ExecuteSQL(context.Background(), SQLRequest{
		Subject:      "u:1",
		DatasourceID: 7,
		Database:     "shop",
		Tables:       []string{"orders"},
		SQL:          "UPDATE orders SET a=1",
	}, stub)

	if !errors.Is(err, ErrNotReadOnly) {
		t.Fatalf("expected ErrNotReadOnly, got %v", err)
	}
	if stub.called {
		t.Error("adapter must not be called for write statement")
	}
}

// databaseAwareAdapter 实现 DatabaseQuerier，记录被调用时拿到的库名。
//
// 这是 No database selected 的回归测试用的替身：MySQL 的连接串只带
// 数据源默认库，用户在查询页选的库必须通过 QueryInDatabase 传下去，
// 否则裸表名查询永远报 1046。
type databaseAwareAdapter struct {
	stubAdapter
	gotDB       string
	dbQueryCall int
}

// 不内嵌 stubAdapter 的 Query：内嵌后 QueryInDatabase 里调 a.Query
// 会走回这个计数方法，测出来的「调用了普通 Query」是假象。
func (a *databaseAwareAdapter) QueryInDatabase(ctx context.Context, db, sql string, limit int) (*adapter.QueryResult, error) {
	a.dbQueryCall++
	a.gotDB = db
	return a.stubAdapter.Query(ctx, sql, limit)
}

// 若这条路被走到，说明 querysvc 没有选 QueryInDatabase。
func (a *databaseAwareAdapter) Query(ctx context.Context, sql string, limit int) (*adapter.QueryResult, error) {
	return nil, errors.New("plain Query must not be used for database-aware adapters")
}

// 请求里声明的库必须传导到适配器，而不是被连接串里的默认库顶掉。
func TestExecuteSQL_PassesDatabaseToAdapter(t *testing.T) {
	svc := &Service{Enforcer: &recordingEnforcer{allowed: true}, Auditor: &recordingAuditor{}}
	ad := &databaseAwareAdapter{stubAdapter: stubAdapter{result: &adapter.QueryResult{}}}

	_, err := svc.ExecuteSQL(context.Background(), SQLRequest{
		Subject:      "u:1",
		DatasourceID: 7,
		Database:     "shop",
		Tables:       []string{"orders"},
		SQL:          "SELECT * FROM orders",
	}, ad)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ad.dbQueryCall != 1 {
		t.Fatalf("QueryInDatabase called %d times, want 1", ad.dbQueryCall)
	}
	if ad.gotDB != "shop" {
		t.Errorf("database passed to adapter = %q, want shop", ad.gotDB)
	}
}

// 不支持 QueryInDatabase 的适配器（PG / Mongo / 测试替身）走原路径，
// 不能被这次改动带崩。
func TestExecuteSQL_FallsBackToPlainQuery(t *testing.T) {
	svc := &Service{Enforcer: &recordingEnforcer{allowed: true}, Auditor: &recordingAuditor{}}
	stub := &stubAdapter{result: &adapter.QueryResult{}}

	_, err := svc.ExecuteSQL(context.Background(), SQLRequest{
		Subject:      "u:1",
		DatasourceID: 7,
		Database:     "public",
		Tables:       []string{"orders"},
		SQL:          "SELECT 1",
	}, stub)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !stub.called {
		t.Error("plain Query should be used when adapter has no QueryInDatabase")
	}
}

func TestExecuteSQL_SuccessPathMasksAndAudits(t *testing.T) {
	enforcer := &recordingEnforcer{allowed: true}
	auditor := &recordingAuditor{}
	svc := &Service{Enforcer: enforcer, Auditor: auditor, Rules: DefaultRules()}

	stub := &stubAdapter{result: &adapter.QueryResult{
		Columns: []string{"id", "phone"},
		Rows:    [][]interface{}{{int64(1), "13812345678"}},
	}}

	res, err := svc.ExecuteSQL(context.Background(), SQLRequest{
		Subject:      "u:1",
		DatasourceID: 7,
		Database:     "shop",
		Tables:       []string{"orders"},
		SQL:          "SELECT id, phone FROM orders",
		RowLimit:     50,
	}, stub)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := res.Rows[0][1]; got != "138****5678" {
		t.Errorf("phone should be masked, got %v", got)
	}
	if stub.gotLim != 50 {
		t.Errorf("adapter limit = %d, want 50", stub.gotLim)
	}
	if len(auditor.events) != 1 {
		t.Fatalf("expected 1 audit event, got %d", len(auditor.events))
	}
	if auditor.events[0].Action != "query.execute" {
		t.Errorf("audit action = %q", auditor.events[0].Action)
	}
	if enforcer.lastObj != "ds:7:shop:orders" {
		t.Errorf("enforce object = %q", enforcer.lastObj)
	}
}

func TestExecuteSQL_DefaultRowLimitApplied(t *testing.T) {
	svc := &Service{Enforcer: &recordingEnforcer{allowed: true}, Auditor: &recordingAuditor{}}
	stub := &stubAdapter{result: &adapter.QueryResult{}}

	_, err := svc.ExecuteSQL(context.Background(), SQLRequest{
		Subject:      "u:1",
		DatasourceID: 7,
		Database:     "shop",
		Tables:       []string{"orders"},
		SQL:          "SELECT 1",
	}, stub)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stub.gotLim != 500 {
		t.Errorf("default limit = %d, want 500", stub.gotLim)
	}
}

func TestExecuteSQL_AuditsOnFailure(t *testing.T) {
	auditor := &recordingAuditor{}
	svc := &Service{
		Enforcer: &recordingEnforcer{allowed: true},
		Auditor:  auditor,
		Rules:    DefaultRules(),
	}
	stub := &stubAdapter{err: errors.New("table not found")}

	_, err := svc.ExecuteSQL(context.Background(), SQLRequest{
		Subject:      "u:1",
		DatasourceID: 7,
		Database:     "shop",
		Tables:       []string{"orders"},
		SQL:          "SELECT * FROM orders",
	}, stub)

	if err == nil {
		t.Fatal("expected error from adapter")
	}
	if len(auditor.events) != 1 {
		t.Fatalf("failure must also be audited, got %d events", len(auditor.events))
	}
	if auditor.events[0].Action != "query.failed" {
		t.Errorf("audit action = %q", auditor.events[0].Action)
	}
}

// 审计失败不能导致查询结果丢失，但必须可感知：Service 返回结果，同时把审计错误暴露。
func TestExecuteSQL_AuditErrorDoesNotBlockQuery(t *testing.T) {
	svc := &Service{
		Enforcer: &recordingEnforcer{allowed: true},
		Auditor:  failingAuditor{},
		Rules:    DefaultRules(),
	}
	stub := &stubAdapter{result: &adapter.QueryResult{
		Columns: []string{"id"},
		Rows:    [][]interface{}{{int64(1)}},
	}}

	res, err := svc.ExecuteSQL(context.Background(), SQLRequest{
		Subject:      "u:1",
		DatasourceID: 7,
		Database:     "shop",
		Tables:       []string{"orders"},
		SQL:          "SELECT id FROM orders",
	}, stub)

	if err != nil {
		t.Fatalf("audit failure should not block query: %v", err)
	}
	if res.RowCount != 1 {
		t.Errorf("result should be returned, got %d rows", res.RowCount)
	}
}

type failingAuditor struct{}

func (failingAuditor) Record(ctx context.Context, e AuditEvent) error {
	return errors.New("audit db down")
}
