package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/opensourceprj/datawarden/internal/adapter"
	"github.com/opensourceprj/datawarden/internal/authz"
	"github.com/opensourceprj/datawarden/internal/model"
)

// --- 测试替身 ---

// errAdapter 的所有元数据方法都失败，用于验证错误被如实报出。
type errAdapter struct {
	err error
}

func (e *errAdapter) Ping(ctx context.Context) error                        { return e.err }
func (e *errAdapter) ListDatabases(ctx context.Context) ([]string, error)   { return nil, e.err }
func (e *errAdapter) ListTables(ctx context.Context, db string) ([]string, error) {
	return nil, e.err
}
func (e *errAdapter) Query(ctx context.Context, sql string, limit int) (*adapter.QueryResult, error) {
	return nil, e.err
}
func (e *errAdapter) QueryWithArgs(ctx context.Context, sql string, args []any, limit int) (*adapter.QueryResult, error) {
	return nil, e.err
}
func (e *errAdapter) Close() error { return nil }

// okAdapter 返回固定元数据，用于验证成功路径。
type okAdapter struct {
	databases []string
	tables    []string
}

func (a *okAdapter) Ping(ctx context.Context) error                      { return nil }
func (a *okAdapter) ListDatabases(ctx context.Context) ([]string, error) { return a.databases, nil }
func (a *okAdapter) ListTables(ctx context.Context, db string) ([]string, error) {
	return a.tables, nil
}
func (a *okAdapter) Query(ctx context.Context, sql string, limit int) (*adapter.QueryResult, error) {
	return &adapter.QueryResult{}, nil
}
func (a *okAdapter) QueryWithArgs(ctx context.Context, sql string, args []any, limit int) (*adapter.QueryResult, error) {
	return &adapter.QueryResult{}, nil
}
func (a *okAdapter) Close() error { return nil }

// grantStorer 在 memDatasources 基础上实现 scopeLister。
type grantStorer struct {
	*memDatasources
	scopes []string
	err    error
}

func (g *grantStorer) GrantScopes(ctx context.Context, userID, dsID int64) ([]string, error) {
	return g.scopes, g.err
}

// plainStorer 故意不实现 scopeLister，用于验证断言失败时不再静默。
type plainStorer struct {
	*memDatasources
}

// allowAll 放行一切，便于单独验证错误处理路径。
type allowAll struct{}

func (allowAll) Enforce(subject, obj, action string) bool { return true }

// denyAll 拒绝一切，用于验证权限过滤。
type denyAll struct{}

func (denyAll) Enforce(subject, obj, action string) bool { return false }

// newMetadataRouter 构造只挂元数据路由的测试引擎。
func newMetadataRouter(t *testing.T, h *MetadataHandler) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// 直接注入身份，绕开 JWT：本文件只测 handler 的错误处理与权限过滤
	r.Use(func(c *gin.Context) {
		c.Set(ctxUserID, int64(1))
		c.Set(ctxUsername, "alice")
		c.Set(ctxRole, string(model.RoleDeveloper))
		c.Next()
	})
	g := r.Group("/datasources/:id")
	g.GET("/databases", h.ListDatabases)
	g.GET("/tables", h.ListTables)
	g.GET("/columns", h.Columns)
	g.GET("/schema", h.Schema)
	return r
}

func seedDS() *memDatasources {
	return &memDatasources{items: []model.Datasource{
		{ID: 1, Name: "pg", Type: model.TypePostgreSQL, Database: "shop"},
		{ID: 2, Name: "redis", Type: model.TypeRedis},
	}}
}

// TestMetadata_ListDatabases_ErrorIs502 是「元数据吞错」的回归测试。
//
// 之前所有错误都被吞成 200 + 空数组，导致「账号没权限」与
// 「库连不上」在接口层无法区分，用户看到空下拉却查不出原因。
func TestMetadata_ListDatabases_ErrorIs502(t *testing.T) {
	h := &MetadataHandler{
		Store:    seedDS(),
		Factory:  func(id int64) (adapter.Adapter, error) { return &errAdapter{err: errors.New("connection refused")}, nil },
		Enforcer: allowAll{},
	}
	r := newMetadataRouter(t, h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/datasources/1/databases", nil))

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502；错误必须报出而不是吞成空数组，body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "connection refused") {
		t.Errorf("响应应包含原始错误信息，body=%s", w.Body.String())
	}
}

// Factory 构造失败同样是故障，必须 502。
func TestMetadata_ListDatabases_FactoryErrorIs502(t *testing.T) {
	h := &MetadataHandler{
		Store:    seedDS(),
		Factory:  func(id int64) (adapter.Adapter, error) { return nil, errors.New("bad tls config") },
		Enforcer: allowAll{},
	}
	r := newMetadataRouter(t, h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/datasources/1/databases", nil))

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502, body=%s", w.Code, w.Body.String())
	}
}

// 成功但为空时必须是 200 + 空数组：
// 前端靠这个区分「确实没有可见的库」（可退回手填）与「故障」（该报错）。
func TestMetadata_ListDatabases_EmptyIs200(t *testing.T) {
	h := &MetadataHandler{
		Store:    seedDS(),
		Factory:  func(id int64) (adapter.Adapter, error) { return &okAdapter{databases: nil}, nil },
		Enforcer: allowAll{},
	}
	r := newMetadataRouter(t, h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/datasources/1/databases", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Errorf("空结果应序列化为 []，body=%s", w.Body.String())
	}
}

func TestMetadata_ListTables_ErrorIs502(t *testing.T) {
	h := &MetadataHandler{
		Store:    seedDS(),
		Factory:  func(id int64) (adapter.Adapter, error) { return &errAdapter{err: errors.New("timeout")}, nil },
		Enforcer: allowAll{},
	}
	r := newMetadataRouter(t, h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/datasources/1/tables?db=shop", nil))

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502, body=%s", w.Code, w.Body.String())
	}
}

// Redis 没有表的概念，直接 200 + 空数组，不去连库。
func TestMetadata_ListTables_RedisReturnsEmpty(t *testing.T) {
	called := false
	h := &MetadataHandler{
		Store: seedDS(),
		Factory: func(id int64) (adapter.Adapter, error) {
			called = true
			return &okAdapter{}, nil
		},
		Enforcer: allowAll{},
	}
	r := newMetadataRouter(t, h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/datasources/2/tables?db=0", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	if called {
		t.Error("Redis 不应构造 adapter")
	}
}

// 只授权部分表时走 GrantScopes 分支，不能去 ListTables
// （那会泄露用户无权看到的表存在性）。
func TestMetadata_ListTables_UsesGrantScopesWhenPartiallyAuthorized(t *testing.T) {
	store := &grantStorer{
		memDatasources: seedDS(),
		scopes:         []string{"shop:orders", "shop:users", "other:payments"},
	}
	called := false
	h := &MetadataHandler{
		Store: store,
		Factory: func(id int64) (adapter.Adapter, error) {
			called = true
			return &okAdapter{tables: []string{"secret_table"}}, nil
		},
		// 先拒绝 db 通配，走 scope 分支
		Enforcer: partialAuthz{allowObj: "ds:1:shop:orders"},
	}
	r := newMetadataRouter(t, h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/datasources/1/tables?db=shop", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	if called {
		t.Error("部分授权时不应调用 ListTables：会列出整个库的表名")
	}
	body := w.Body.String()
	if !strings.Contains(body, "orders") || !strings.Contains(body, "users") {
		t.Errorf("应返回授权范围内的表，body=%s", body)
	}
	if strings.Contains(body, "payments") {
		t.Errorf("不应包含其他库的表，body=%s", body)
	}
}

// partialAuthz 只放行指定 object，其余拒绝。
type partialAuthz struct{ allowObj string }

func (p partialAuthz) Enforce(subject, obj, action string) bool { return obj == p.allowObj }

// 整数据源授权（scope = "*"）的用户必须能列出表。
//
// 这条用**真实 Casbin**（而不用 allowAll / denyAll 替身）来验证，
// 因为整源授权能列出表，靠的不是 tablesFor 里的某个特判分支，
// 而是 `ds:<id>:*:*` 策略经 keyMatch 命中 `ds:<id>:<库>:*` 判定。
// 用替身伪造「只放行库级通配」会让测试歪打正着地通过，
// 却掩盖了真实的判定路径——第一版测试就是这样写错的：
// 它配 denyAll + scopes=["*"]，走的是后来被确认不可达的死分支。
//
// 若将来有人把策略拼错（例如落成 `ds:<id>:*` 少一段），
// 这里会直接失败。
func TestMetadata_ListTables_AllDatabasesScopeListsTables(t *testing.T) {
	enforcer, err := authz.NewEnforcer()
	if err != nil {
		t.Fatalf("new enforcer: %v", err)
	}
	// 与 SyncGrants 对 ScopeAllDatabases 的处理保持一致：拼成 ds:<id>:*:*
	if err := enforcer.SyncGrants([]authz.GrantPolicy{
		{UserID: 1, DatasourceID: 1, Scope: authz.ScopeAllDatabases, Action: "read"},
	}); err != nil {
		t.Fatalf("sync grants: %v", err)
	}

	store := &grantStorer{
		memDatasources: seedDS(),
		scopes:         []string{authz.ScopeAllDatabases},
	}
	h := &MetadataHandler{
		Store: store,
		Factory: func(id int64) (adapter.Adapter, error) {
			return &okAdapter{tables: []string{"users", "orders"}}, nil
		},
		Enforcer: enforcer,
	}
	r := newMetadataRouter(t, h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/datasources/1/tables?db=shop", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "orders") || !strings.Contains(body, "users") {
		t.Errorf("整数据源授权应能列出全部表，body=%s", body)
	}
}

// 整源授权下，换成另一个库也必须能列出表——这是「所有库」的实际含义。
func TestMetadata_ListTables_AllDatabasesScopeCoversEveryDatabase(t *testing.T) {
	enforcer, err := authz.NewEnforcer()
	if err != nil {
		t.Fatalf("new enforcer: %v", err)
	}
	if err := enforcer.SyncGrants([]authz.GrantPolicy{
		{UserID: 1, DatasourceID: 1, Scope: authz.ScopeAllDatabases, Action: "read"},
	}); err != nil {
		t.Fatalf("sync grants: %v", err)
	}
	h := &MetadataHandler{
		Store: &grantStorer{
			memDatasources: seedDS(),
			scopes:         []string{authz.ScopeAllDatabases},
		},
		Factory: func(id int64) (adapter.Adapter, error) {
			return &okAdapter{tables: []string{"trades"}}, nil
		},
		Enforcer: enforcer,
	}
	r := newMetadataRouter(t, h)

	// payment 库从未出现在任何 scope 里，整源授权下同样可列
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/datasources/1/tables?db=payment", nil))

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "trades") {
		t.Fatalf("整数据源授权应覆盖任意库，status=%d body=%s", w.Code, w.Body.String())
	}
}

// 别的数据源不能被整源授权覆盖：核对策略里数据源 ID 是钉死的。
func TestMetadata_ListTables_AllDatabasesScopeIsPerDatasource(t *testing.T) {
	enforcer, err := authz.NewEnforcer()
	if err != nil {
		t.Fatalf("new enforcer: %v", err)
	}
	if err := enforcer.SyncGrants([]authz.GrantPolicy{
		{UserID: 1, DatasourceID: 1, Scope: authz.ScopeAllDatabases, Action: "read"},
	}); err != nil {
		t.Fatalf("sync grants: %v", err)
	}
	h := &MetadataHandler{
		Store: &grantStorer{
			memDatasources: &memDatasources{items: []model.Datasource{
				{ID: 2, Name: "other", Type: model.TypePostgreSQL, Database: "shop"},
			}},
			scopes: []string{authz.ScopeAllDatabases},
		},
		Factory: func(id int64) (adapter.Adapter, error) {
			return &okAdapter{tables: []string{"should_not_leak"}}, nil
		},
		Enforcer: enforcer,
	}
	r := newMetadataRouter(t, h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/datasources/2/tables?db=shop", nil))

	if strings.Contains(w.Body.String(), "should_not_leak") {
		t.Errorf("数据源 1 的整源授权不能覆盖数据源 2，body=%s", w.Body.String())
	}
}

// 只授了部分表、且没有整源授权时，仍然只列授权表（不能因为放宽口径
// 就把所有有库权限的人当成本库全表可见）。
func TestMetadata_ListTables_PartialScopeStillFiltered(t *testing.T) {
	store := &grantStorer{
		memDatasources: seedDS(),
		scopes:         []string{"shop:orders"},
	}
	h := &MetadataHandler{
		Store: store,
		Factory: func(id int64) (adapter.Adapter, error) {
			return &okAdapter{tables: []string{"orders", "users", "secrets"}}, nil
		},
		Enforcer: denyAll{},
	}
	r := newMetadataRouter(t, h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/datasources/1/tables?db=shop", nil))

	body := w.Body.String()
	if !strings.Contains(body, "orders") {
		t.Errorf("应包含受权的 orders，body=%s", body)
	}
	if strings.Contains(body, "secrets") {
		t.Errorf("不应包含未受权的 secrets，body=%s", body)
	}
}

// scopeLister 断言失败必须报错而不是返回空数组：
// Store 没实现 GrantScopes 说明装配漏了，是部署缺陷。
func TestMetadata_ListTables_MissingScopeListerIs502(t *testing.T) {
	store := &plainStorer{memDatasources: seedDS()}
	h := &MetadataHandler{
		Store:    store,
		Factory:  func(id int64) (adapter.Adapter, error) { return &okAdapter{}, nil },
		Enforcer: denyAll{},
	}
	r := newMetadataRouter(t, h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/datasources/1/tables?db=shop", nil))

	if w.Code == http.StatusOK && strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatal("Store 未实现 GrantScopes 时不能静默返回空数组——那是部署缺陷，不是「没有表」")
	}
}

// Columns 的表级鉴权是唯一阻止列名泄露的关卡，必须有测试。
func TestMetadata_Columns_ForbiddenWithoutGrant(t *testing.T) {
	h := &MetadataHandler{
		Store:    seedDS(),
		Factory:  func(id int64) (adapter.Adapter, error) { return &okAdapter{}, nil },
		Enforcer: denyAll{},
	}
	r := newMetadataRouter(t, h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/datasources/1/columns?db=shop&table=orders", nil))

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403（无表权限不能拿列名）, body=%s", w.Code, w.Body.String())
	}
}

func TestMetadata_Columns_MissingParamsIs400(t *testing.T) {
	h := &MetadataHandler{
		Store:    seedDS(),
		Factory:  func(id int64) (adapter.Adapter, error) { return &okAdapter{}, nil },
		Enforcer: allowAll{},
	}
	r := newMetadataRouter(t, h)

	for _, path := range []string{
		"/datasources/1/columns",
		"/datasources/1/columns?db=shop",
		"/datasources/1/columns?table=orders",
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", path, w.Code)
		}
	}
}

// schemaAdapter 在 okAdapter 基础上实现 adapter.SchemaReader，
// 用于验证整库表结构的返回与按表裁剪。
type schemaAdapter struct {
	*okAdapter
	tables []adapter.TableColumns
	err    error
}

func (s *schemaAdapter) ListSchema(ctx context.Context, db string) ([]adapter.TableColumns, error) {
	return s.tables, s.err
}

// Schema 是把 `别名.` 补全所需的数据一次性交给前端的接口，
// 成功路径要保证字段顺序保持建表顺序（前端直接按顺序展示）。
func TestMetadata_Schema_ReturnsColumns(t *testing.T) {
	h := &MetadataHandler{
		Store: seedDS(),
		Factory: func(id int64) (adapter.Adapter, error) {
			return &schemaAdapter{
				okAdapter: &okAdapter{},
				tables: []adapter.TableColumns{
					{Table: "app_user", Columns: []string{"id", "name", "email"}},
				},
			}, nil
		},
		Enforcer: allowAll{},
	}
	r := newMetadataRouter(t, h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/datasources/1/schema?db=shop", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "app_user") || !strings.Contains(body, `"email"`) {
		t.Errorf("应返回表名与字段名，body=%s", body)
	}
}

// 不支持批量能力的适配器（Mongo / Redis）返回 200 + 空清单，
// 让前端的补全逻辑统一降级，不必为数据源类型写分支。
func TestMetadata_Schema_UnsupportedAdapterIsEmpty(t *testing.T) {
	h := &MetadataHandler{
		Store:    seedDS(),
		Factory:  func(id int64) (adapter.Adapter, error) { return &okAdapter{}, nil },
		Enforcer: allowAll{},
	}
	r := newMetadataRouter(t, h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/datasources/1/schema?db=shop", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Errorf("不支持时应返回空清单，body=%s", w.Body.String())
	}
}

// 没有该库权限时直接 403，不连库：能列出表结构本身就是信息泄露。
func TestMetadata_Schema_ForbiddenWithoutGrant(t *testing.T) {
	h := &MetadataHandler{
		Store: seedDS(),
		Factory: func(id int64) (adapter.Adapter, error) {
			return &schemaAdapter{okAdapter: &okAdapter{}}, nil
		},
		Enforcer: denyAll{},
	}
	r := newMetadataRouter(t, h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/datasources/1/schema?db=shop", nil))

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body=%s", w.Code, w.Body.String())
	}
}

// 数据源不存在时 404，不应穿透成 500。
func TestMetadata_UnknownDatasourceIs404(t *testing.T) {
	h := &MetadataHandler{
		Store:    seedDS(),
		Factory:  func(id int64) (adapter.Adapter, error) { return &okAdapter{}, nil },
		Enforcer: allowAll{},
	}
	r := newMetadataRouter(t, h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/datasources/999/databases", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404, body=%s", w.Code, w.Body.String())
	}
}
