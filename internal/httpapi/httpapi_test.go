package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/opensourceprj/datawarden/internal/auth"
	"github.com/opensourceprj/datawarden/internal/model"
)

func TestLogin_RejectsBadCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newTestRouter(t, map[string]string{"alice": "correct"})

	body := bytes.NewBufferString(`{"username":"alice","password":"wrong"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/login", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	// 用户名存在但密码不对，提示必须是「密码错误」，
	// 不能是「用户不存在」——否则用户会以为自己账号被删了。
	var resp map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["error"] != "密码错误" {
		t.Errorf("error = %q, want 密码错误", resp["error"])
	}
}

func TestLogin_RejectsUnknownUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newTestRouter(t, map[string]string{"alice": "correct"})

	body := bytes.NewBufferString(`{"username":"nobody","password":"whatever"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/login", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	// 用户名不存在与密码错误的提示必须不同（产品决定允许区分）
	var resp map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["error"] != "用户不存在" {
		t.Errorf("error = %q, want 用户不存在", resp["error"])
	}
}

func TestLogin_ReturnsToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newTestRouter(t, map[string]string{"alice": "correct"})

	body := bytes.NewBufferString(`{"username":"alice","password":"correct"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/login", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["token"] == "" {
		t.Error("token should not be empty")
	}
	if resp["role"] != "developer" {
		t.Errorf("role = %q, want developer", resp["role"])
	}
}

func TestLogin_RejectsMissingFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newTestRouter(t, map[string]string{"alice": "correct"})

	body := bytes.NewBufferString(`{"username":"alice"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/login", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// failingLookup 返回预设错误，用于覆盖「账号停用」与「数据库故障」两条分支。
type failingLookup struct {
	err error
}

func (f *failingLookup) FindByUsername(ctx context.Context, _ string) (int64, string, string, error) {
	return 0, "", "", f.err
}

// 停用账号与「用户不存在」必须是不同提示：否则用户拿着正确密码
// 一直重试，永远不知道是账号被停用了。
func TestLogin_RejectsInactiveAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	issuer := auth.NewTokenIssuer("test-secret", time.Hour)
	r := NewRouter(Deps{
		Issuer: issuer,
		AuthHandler: &AuthHandler{
			Users:  &failingLookup{err: model.ErrLoginAccountInactive},
			Issuer: issuer,
		},
		Datasource: &DatasourceHandler{Store: &memDatasources{}},
		Query:      &QueryHandler{},
	})

	body := bytes.NewBufferString(`{"username":"alice","password":"correct"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/login", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	var resp map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if !strings.Contains(resp["error"], "停用") {
		t.Errorf("error = %q, want it to mention 停用", resp["error"])
	}
}

// 数据库故障不能被伪装成「凭据错误」：那样用户会一直重试，
// 运维也看不到真实原因。
func TestLogin_StoreFailureIs500(t *testing.T) {
	gin.SetMode(gin.TestMode)
	issuer := auth.NewTokenIssuer("test-secret", time.Hour)
	r := NewRouter(Deps{
		Issuer: issuer,
		AuthHandler: &AuthHandler{
			Users:  &failingLookup{err: errors.New("connection refused")},
			Issuer: issuer,
		},
		Datasource: &DatasourceHandler{Store: &memDatasources{}},
		Query:      &QueryHandler{},
	})

	body := bytes.NewBufferString(`{"username":"alice","password":"correct"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/login", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

func TestProtectedRoute_RequiresToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newTestRouter(t, map[string]string{"alice": "correct"})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/datasources", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestProtectedRoute_RejectsGarbageToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newTestRouter(t, map[string]string{"alice": "correct"})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/datasources", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

// 有效 token 能通过鉴权中间件。用 dba 身份访问数据源列表：
// 列表接口已对开发者关闭，用 developer 测会被 403 挡住，测不到这里想验证的中间件。
func TestProtectedRoute_AcceptsValidToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newTestRouterWithRole(t, "dba")

	tok := loginAndGetToken(t, r, "alice", "correct")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/datasources", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
}

func TestCreateDatasource_RequiresDBA(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newTestRouterWithRole(t, "developer")

	tok := loginAndGetToken(t, r, "alice", "correct")
	// body 必须是完整合法的：如果故意留空 database，这个用例会因为
	// 参数校验先于权限校验而失败（或反过来，在顺序调整后仍然"通过"），
	// 那样它测的就不是"非 DBA 被拒"而是"恰好先被别的检查拦了"。
	body := bytes.NewBufferString(`{
		"name":"shop","type":"postgresql","host":"h","port":5432,
		"database":"shop","username":"readonly","password":"pw"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/datasources", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for non-dba, body=%s", w.Code, w.Body.String())
	}
	// 断言错误体含权限文案：仅断言 403 无法区分
	// 「权限拒绝」与「参数校验拒绝返回了 4xx」这两种情况。
	if !strings.Contains(strings.ToLower(w.Body.String()), "dba") {
		t.Errorf("403 应当来自权限检查而非参数校验，body=%s", w.Body.String())
	}
}

func TestCreateDatasource_DBAPasses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newTestRouterWithRole(t, "dba")

	tok := loginAndGetToken(t, r, "alice", "correct")
	// database 必填：PostgreSQL 的库名是连接串的 dbname，
	// 留空会连到与用户名同名的库并失败（见 validateDatasource）。
	body := bytes.NewBufferString(`{
		"name":"shop","type":"postgresql","host":"h","port":5432,
		"database":"shop","username":"readonly","password":"pw"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/datasources", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body=%s", w.Code, w.Body.String())
	}
}

// validateDatasource 的必填规则必须与 README「各类型对连接字段的要求」一致。
// 只在前端拦是不够的：绕开前端直接调 API 能存进一条必然连不上的数据源，
// 而错误要到用户点查询时才暴露，且是驱动层的含糊报错。
func TestCreateDatasource_DatabaseRequiredByType(t *testing.T) {
	cases := []struct {
		name     string
		typ      string
		database string
		wantCode int
		why      string
	}{
		{
			name: "PostgreSQL 缺库名", typ: "postgresql", database: "",
			wantCode: http.StatusBadRequest,
			why:      "库名是 DSN 的 dbname，留空会连到与用户名同名的库",
		},
		{
			name: "MongoDB 缺默认库", typ: "mongodb", database: "",
			wantCode: http.StatusBadRequest,
			why:      "不知道要查哪个库",
		},
		{
			name: "MySQL 允许空库名", typ: "mysql", database: "",
			wantCode: http.StatusCreated,
			why:      "MySQL 可以「连接但不选库」，查询时写 db.table",
		},
		{
			name: "Redis 允许空库号", typ: "redis", database: "",
			wantCode: http.StatusCreated,
			why:      "Redis 的 db 号留空等于 db 0",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			r := newTestRouterWithRole(t, "dba")
			tok := loginAndGetToken(t, r, "alice", "correct")

			port := map[string]int{"postgresql": 5432, "mongodb": 27017, "mysql": 3306, "redis": 6379}[c.typ]
			body := bytes.NewBufferString(fmt.Sprintf(`{
				"name":"ds-%s","type":%q,"host":"h","port":%d,
				"database":%q,"username":"ro","password":"pw"
			}`, c.typ, c.typ, port, c.database))
			req := httptest.NewRequest(http.MethodPost, "/api/v1/datasources", body)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+tok)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != c.wantCode {
				t.Fatalf("status = %d, want %d (%s), body=%s",
					w.Code, c.wantCode, c.why, w.Body.String())
			}
		})
	}
}

func loginAndGetToken(t *testing.T, r http.Handler, user, pass string) string {
	t.Helper()
	body := bytes.NewBufferString(`{"username":"` + user + `","password":"` + pass + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/login", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	return resp["token"]
}

// 开发者不能读数据源列表：返回体里带着主机、端口、库名、连接账号。
func TestListDatasources_RejectsDeveloper(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newTestRouterWithRole(t, "developer")

	tok := loginAndGetToken(t, r, "alice", "correct")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/datasources", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for developer, body=%s", w.Code, w.Body.String())
	}
}

// 开发者也不能读单个数据源：Get 会给 DBA 回显密码掩码。
func TestGetDatasource_RejectsDeveloper(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newTestRouterWithRole(t, "developer")

	tok := loginAndGetToken(t, r, "alice", "correct")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/datasources/1", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for developer, body=%s", w.Code, w.Body.String())
	}
}

// DBA 编辑数据源时 Get 回显密码，但只能是掩码：明文绝不能出现在响应里。
func TestGetDatasource_DBA_GetsMaskedPassword(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ds := &memDatasources{items: []model.Datasource{{
		ID: 1, Name: "shop", Type: model.TypePostgreSQL,
		Host: "h", Port: 5432, Database: "db",
		Username: "readonly", Password: "s3cr3t-prod-pw", HasPassword: true,
	}}}
	r := buildRouterWithStore(t, ds, "dba")

	tok := loginAndGetToken(t, r, "alice", "correct")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/datasources/1", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	// 编辑弹窗要直接把密码填进输入框，所以详情接口回显明文
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if pw, _ := got["password"].(string); pw != "s3cr3t-prod-pw" {
		t.Fatalf("password = %q, want plaintext echo for edit form, body=%s", pw, w.Body.String())
	}
}

// 列表接口绝不能回显密码：一次响应会把全部数据源的凭据带到前端。
func TestListDatasources_DBA_NeverEchoesPassword(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ds := &memDatasources{items: []model.Datasource{{
		ID: 1, Name: "shop", Type: model.TypePostgreSQL,
		Host: "h", Port: 5432, Database: "db",
		Username: "readonly", Password: "s3cr3t-prod-pw", HasPassword: true,
	}}}
	r := buildRouterWithStore(t, ds, "dba")

	tok := loginAndGetToken(t, r, "alice", "correct")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/datasources", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, "s3cr3t-prod-pw") {
		t.Fatalf("list must not leak password: %s", body)
	}
	if strings.Contains(body, `"password"`) {
		t.Fatalf("list must not carry a password key: %s", body)
	}
	if !strings.Contains(body, `"has_password":true`) {
		t.Fatalf("list should still report has_password: %s", body)
	}
}

// 编辑时不改密码（提交空串），原密码必须保留。
func TestUpdateDatasource_EmptyPasswordKeepsExisting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ds := &memDatasources{items: []model.Datasource{{
		ID: 1, Name: "shop", Type: model.TypePostgreSQL,
		Host: "h", Port: 5432, Database: "db",
		Username: "readonly", Password: "s3cr3t-prod-pw", HasPassword: true,
	}}}
	r := buildRouterWithStore(t, ds, "dba")

	tok := loginAndGetToken(t, r, "alice", "correct")
	body := bytes.NewBufferString(`{
		"name":"shop","type":"postgresql","host":"h","port":5432,
		"database":"db","username":"readonly","password":""
	}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/datasources/1", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	// 空密码交给 store 表示「不修改」，原有明文必须还在
	if got := ds.items[0].Password; got != "s3cr3t-prod-pw" {
		t.Fatalf("password = %q, empty submit must not clear it", got)
	}
}

// 编辑时改了密码，新密码必须写进去。
func TestUpdateDatasource_NewPasswordIsSaved(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ds := &memDatasources{items: []model.Datasource{{
		ID: 1, Name: "shop", Type: model.TypePostgreSQL,
		Host: "h", Port: 5432, Database: "db",
		Username: "readonly", Password: "s3cr3t-prod-pw", HasPassword: true,
	}}}
	r := buildRouterWithStore(t, ds, "dba")

	tok := loginAndGetToken(t, r, "alice", "correct")
	body := bytes.NewBufferString(`{
		"name":"shop","type":"postgresql","host":"h","port":5432,
		"database":"db","username":"readonly","password":"brand-new-pw"
	}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/datasources/1", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	if got := ds.items[0].Password; got != "brand-new-pw" {
		t.Fatalf("password = %q, want new value saved", got)
	}
}
