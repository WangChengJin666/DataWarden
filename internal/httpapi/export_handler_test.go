package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/opensourceprj/datawarden/internal/adapter"
	"github.com/opensourceprj/datawarden/internal/model"
)

// TestCreateGrantRequest_PersistsCanExport 锁住「申请页勾了导出就落库」。
//
// 这行赋值没有任何校验逻辑保护，重构时最容易被顺手删掉；
// 丢了的表现是「用户勾了导出、审批通过后仍然导不了」，
// 而两张表里都看不出哪里错了。
func TestCreateGrantRequest_PersistsCanExport(t *testing.T) {
	cases := []struct {
		name  string
		field any // 不传 / true / false
		want  bool
	}{
		{"不传（旧客户端）", nil, false},
		{"勾选导出", true, true},
		{"明确不勾", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := &captureRequests{}
			h := &GrantRequestHandler{
				Requests: store,
				Datasrc: &applyableStub{items: []model.Datasource{{
					ID: 1, Name: "shop", Type: model.TypePostgreSQL,
					ApplyEnabled: true, MaxApplyDays: 7,
				}}},
			}
			body := map[string]any{"datasource_id": 1, "days": 3, "reason": "排查"}
			if c.field != nil {
				body["can_export"] = c.field
			}
			raw, _ := json.Marshal(body)
			if w := doCreate(t, h, raw); w.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201, body=%s", w.Code, w.Body.String())
			}
			if len(store.got) != 1 {
				t.Fatalf("应当创建 1 条申请，实际 %d 条", len(store.got))
			}
			if got := store.got[0].CanExport; got != c.want {
				t.Errorf("落库 can_export = %v, want %v", got, c.want)
			}
		})
	}
}

// fakeExportGrants 是导出闸门的替身。
type fakeExportGrants struct {
	// allowed 按数据源 ID 给出结论，用于验证「A 源能导、B 源不能」。
	allowed map[int64]bool
	called  []int64
	err     error
}

func (f *fakeExportGrants) CanExport(_ context.Context, _ int64, datasourceID int64) (bool, error) {
	f.called = append(f.called, datasourceID)
	if f.err != nil {
		return false, f.err
	}
	return f.allowed[datasourceID], nil
}

// fakeEnforcer 固定返回「有查询权限」，用于确认拒绝原因是"没批导出"
// 而不是"没查询权限"。
type fakeEnforcer struct{ allow bool }

func (f fakeEnforcer) Enforce(string, string, string) bool { return f.allow }

// doExport 用指定角色调一次导出接口。
//
// 默认带上 Service 与 Factory 的「装配缺失」替身：真正的放行与否
// 由闸门决定，而闸门放行之后需要的执行链路不在本文件的关注范围内。
// 需要验证 503 的用例自己传 nil 的 Service。
func doExport(t *testing.T, h *ExportHandler, role string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(ctxUserID, int64(7))
		c.Set(ctxUsername, "dev")
		c.Set(ctxRole, role)
		c.Next()
	})
	r.POST("/query/export", h.Export)

	raw, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/query/export", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

// fakeFactory 提供一个「能连上、能执行」的 Adapter，让请求越过闸门
// 一路走到 xlsx 生成。真正的查询语义由 querysvc 自己的测试覆盖，
// 这里只需要一条能拿到若干行结果的链路。
func fakeFactory(int64) (adapter.Adapter, error) { return emptyResultAdapter{}, nil }

type emptyResultAdapter struct{}

func (emptyResultAdapter) Ping(context.Context) error                        { return nil }
func (emptyResultAdapter) ListDatabases(context.Context) ([]string, error)   { return nil, nil }
func (emptyResultAdapter) ListTables(context.Context, string) ([]string, error) { return nil, nil }
func (emptyResultAdapter) Query(context.Context, string, int) (*adapter.QueryResult, error) {
	return &adapter.QueryResult{
		Columns: []string{"id", "name"},
		Rows:    [][]any{{1, "订单A"}, {2, nil}},
		RowCount: 2,
	}, nil
}
func (emptyResultAdapter) QueryWithArgs(context.Context, string, []any, int) (*adapter.QueryResult, error) {
	return nil, nil
}
func (emptyResultAdapter) Close() error { return nil }

// newExportHandler 装配一个闸门可控、执行链路可用的导出 handler。
// 鉴权交给 allowAll 的替身：本文件验证的是导出闸门，不是库级鉴权。
func newExportHandler(grants ExportStore) *ExportHandler {
	return &ExportHandler{
		Service:  newTestQueryServiceWithEnforcer(&recordingAuditor{}, fakeEnforcer{allow: true}),
		Factory:  fakeFactory,
		Grants:   grants,
		Enforcer: fakeEnforcer{allow: true},
	}
}

// TestExport_DeveloperNeedsCanExportOnThatDatasource 是这条需求的核心用例：
//
//	同一个开发者，A 数据源批了导出、B 数据源没批 —— A 能导，B 不能。
//
// 判定必须按「数据源」而不是按「人」：放在用户维度上，就会出现
// 「A 源批过导出、B 源也能导」的越权，而两个源本来是一次一审批。
func TestExport_DeveloperNeedsCanExportOnThatDatasource(t *testing.T) {
	grants := &fakeExportGrants{allowed: map[int64]bool{1: true, 2: false}}
	h := newExportHandler(grants)

	t.Run("已批导出的数据源放行", func(t *testing.T) {
		w := doExport(t, h, string(model.RoleDeveloper), map[string]any{
			"datasource_id": 1, "type": "sql", "database": "shop", "sql": "select 1",
		})
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
		}
		// 必须是真正的 xlsx：前两个字节是 zip 魔数 PK。
		// 只断言 200 会把「返回了一段错误 JSON」也当成通过。
		body := w.Body.Bytes()
		if len(body) < 4 || body[0] != 'P' || body[1] != 'K' {
			t.Fatalf("响应不是 zip/xlsx，前几个字节 % x", body[:min(8, len(body))])
		}
		if ct := w.Header().Get("Content-Type"); ct !=
			"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
			t.Errorf("Content-Type = %q", ct)
		}
		if cd := w.Header().Get("Content-Disposition"); cd == "" {
			t.Error("缺少 Content-Disposition，浏览器无法按名字保存")
		}
	})

	t.Run("未批导出的数据源拒绝", func(t *testing.T) {
		w := doExport(t, h, string(model.RoleDeveloper), map[string]any{
			"datasource_id": 2, "type": "sql", "database": "shop", "sql": "select 1",
		})
		if w.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403, body=%s", w.Code, w.Body.String())
		}
		var resp struct{ Error string }
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		if resp.Error == "" {
			t.Error("403 必须带提示，否则用户不知道要去申请导出")
		}
	})

	if len(grants.called) != 2 {
		t.Errorf("闸门应被调用 2 次（每个数据源一次），实际 %d 次", len(grants.called))
	}
}

// 有查询权限但没批导出时，提示必须与"根本没权限查这个源"区分开：
// 前者用户能自己解决（去申请导出），后者是另一回事。
func TestExport_DistinguishesNoGrantFromNoExport(t *testing.T) {
	noExport := newExportHandler(&fakeExportGrants{allowed: map[int64]bool{}})
	w := doExport(t, noExport, string(model.RoleDeveloper), map[string]any{
		"datasource_id": 3, "type": "sql", "database": "shop", "sql": "select 1",
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	var resp struct{ Error string }
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Error == "no permission on this datasource" {
		t.Error("有查询权限但没批导出，不应提示成「没有查询权限」")
	}

	// 连查询权限都没有：不能再说"去申请导出"，那会误导用户以为
	// 自己有查询权限，只是差一个勾选。
	noGrant := &ExportHandler{
		Service:  newTestQueryServiceWithEnforcer(&recordingAuditor{}, fakeEnforcer{allow: true}),
		Factory:  fakeFactory,
		Grants:   &fakeExportGrants{allowed: map[int64]bool{}},
		Enforcer: fakeEnforcer{allow: false},
	}
	w = doExport(t, noGrant, string(model.RoleDeveloper), map[string]any{
		"datasource_id": 3, "type": "sql", "database": "shop", "sql": "select 1",
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Error != "no permission on this datasource" {
		t.Errorf("error = %q，want no permission on this datasource", resp.Error)
	}
}

// 管理角色免授权，导出也不该拦——他们本来就能查任意数据源，
// 拦导出只是让自己用不了功能，不减少任何泄露面。
func TestExport_PrivilegedSkipsExportGate(t *testing.T) {
	grants := &fakeExportGrants{allowed: map[int64]bool{}}
	h := newExportHandler(grants)
	for _, role := range []model.Role{model.RoleDBA, model.RoleSuperAdmin} {
		w := doExport(t, h, string(role), map[string]any{
			"datasource_id": 9, "type": "sql", "database": "shop", "sql": "select 1",
		})
		if w.Code != http.StatusOK {
			t.Errorf("[%s] status = %d, want 200, body=%s", role, w.Code, w.Body.String())
		}
	}
	if len(grants.called) != 0 {
		t.Errorf("管理角色不应查询 grant_record，实际调用 %d 次", len(grants.called))
	}
}

// 闸门查不出来时必须拒绝（fail-closed）：导出是数据外带动作，
// 权限不明的正确方向是不导，而不是照导。
func TestExport_FailsClosedWhenGateErrors(t *testing.T) {
	h := newExportHandler(&fakeExportGrants{err: contextError{}})
	w := doExport(t, h, string(model.RoleDeveloper), map[string]any{
		"datasource_id": 1, "type": "sql", "database": "shop", "sql": "select 1",
	})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

// Redis 结果不做导出：它的形态是 key→值，摊平成二维表没有合理语义。
// 明确回 400 而不是生成一个乱七八糟的文件。
func TestExport_RejectsRedis(t *testing.T) {
	h := newExportHandler(&fakeExportGrants{allowed: map[int64]bool{1: true}})
	w := doExport(t, h, string(model.RoleDeveloper), map[string]any{
		"datasource_id": 1, "type": "redis", "key": "user:1",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", w.Code, w.Body.String())
	}
}

// 导出文件名会被塞进 Content-Disposition，必须挡住路径与头注入字符；
// 中文要保留（用户看得懂），扩展名统一由后端补。
func TestExportFilename_Sanitizes(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "query_result.xlsx"},
		{"订单查询", "订单查询.xlsx"},
		{"订单 查询（近3天）", "订单 查询（近3天）.xlsx"},
		{"../etc/passwd", "_etc_passwd.xlsx"},
		{"a\r\nSet-Cookie: x", "a__Set-Cookie_ x.xlsx"},
		{"already.xlsx", "already.xlsx"},
		{"  .hidden  ", "hidden.xlsx"},
	}
	for _, c := range cases {
		if got := exportFilename(c.in); got != c.want {
			t.Errorf("exportFilename(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// 全为危险字符时不能返回空文件名（浏览器会退化成 URL 最后一段）。
func TestExportFilename_NeverEmpty(t *testing.T) {
	if got := exportFilename("///"); got != "query_result.xlsx" && got != "_.xlsx" {
		t.Logf("exportFilename(%q) = %q", "///", got)
	}
	if got := exportFilename("///"); got == "" || got == ".xlsx" {
		t.Fatalf("exportFilename(///) = %q，不能是空文件名", got)
	}
}

// 行数上限：请求里的 0 / 负数按默认值处理（用户清空输入框时会传 0），
// 超大值被夹到上限——不夹的话一条 SELECT 就能把整库拉进内存。
func TestClampExportRows(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, exportDefaultRowLimit},
		{-5, exportDefaultRowLimit},
		{100, 100},
		{exportMaxRowLimit + 1, exportMaxRowLimit},
	}
	for _, c := range cases {
		if got := clampExportRows(c.in); got != c.want {
			t.Errorf("clampExportRows(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

type contextError struct{}

func (contextError) Error() string { return "db down" }
