package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/opensourceprj/datawarden/internal/auth"
	"github.com/opensourceprj/datawarden/internal/model"
)

// stubTester 记录收到的配置并返回预设结果。
type stubTester struct {
	res  *TestConnResult
	err  error
	got  TestConnConfig
	call bool
}

func (s *stubTester) Test(ctx context.Context, cfg TestConnConfig) (*TestConnResult, error) {
	s.call = true
	s.got = cfg
	return s.res, s.err
}

func testConnRouter(t *testing.T, role string, tester ConnTester, store DatasourceStore) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	issuer := auth.NewTokenIssuer("test-secret", time.Hour)
	return NewRouter(Deps{
		Issuer:      issuer,
		AuthHandler: &AuthHandler{Users: &memUsers{passwords: map[string]string{"alice": "pw"}, role: role}, Issuer: issuer},
		Datasource: &DatasourceHandler{
			Store:  store,
			Tester: tester,
		},
		Query: &QueryHandler{},
	})
}

func postTestConn(t *testing.T, r http.Handler, token string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/datasources/test", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestTestConnection_RequiresAuth(t *testing.T) {
	r := testConnRouter(t, "dba", &stubTester{}, &memDatasources{})
	w := postTestConn(t, r, "", map[string]any{"type": "postgresql", "host": "h", "port": 5432})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

// 开发者不该能探测任意主机的连通性——这等于一个内网端口扫描器。
func TestTestConnection_RequiresDBA(t *testing.T) {
	st := &stubTester{}
	r := testConnRouter(t, "developer", st, &memDatasources{})

	tok := loginAndGetToken(t, r, "alice", "pw")
	w := postTestConn(t, r, tok, map[string]any{"type": "postgresql", "host": "h", "port": 5432})

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if st.call {
		t.Error("tester must not run for non-dba users")
	}
}

func TestTestConnection_Success(t *testing.T) {
	st := &stubTester{res: &TestConnResult{OK: true, Message: "连接成功", ElapsedMs: 12}}
	r := testConnRouter(t, "dba", st, &memDatasources{})

	tok := loginAndGetToken(t, r, "alice", "pw")
	w := postTestConn(t, r, tok, map[string]any{
		"type":     "postgresql",
		"host":     "10.0.0.5",
		"port":     5432,
		"database": "shop",
		"username": "readonly",
		"password": "pw",
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if !st.call {
		t.Fatal("tester should be called")
	}
	if st.got.Password != "pw" || st.got.User != "readonly" {
		t.Errorf("credentials not passed through: %+v", st.got)
	}
	if st.got.Database != "shop" || st.got.Port != 5432 {
		t.Errorf("connection fields wrong: %+v", st.got)
	}

	var out TestConnResult
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if !out.OK || out.Message != "连接成功" {
		t.Errorf("unexpected body: %s", w.Body.String())
	}
}

// 测试失败时返回 200 + ok:false，而不是 5xx。
// 「连不上」是用户要看的业务结果，不是服务端错误。
func TestTestConnection_FailureIsNotServerError(t *testing.T) {
	st := &stubTester{err: errors.New("password authentication failed for user \"x\"")}
	r := testConnRouter(t, "dba", st, &memDatasources{})

	tok := loginAndGetToken(t, r, "alice", "pw")
	w := postTestConn(t, r, tok, map[string]any{
		"type": "postgresql", "host": "h", "port": 5432, "username": "u", "password": "bad",
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["ok"] != false {
		t.Errorf("ok should be false, got %v", out["ok"])
	}
	msg, _ := out["message"].(string)
	if msg == "" {
		t.Error("message should explain the failure")
	}
	// 认证失败应转成可读提示，而不是把驱动原文直接抛给用户
	if msg != "认证失败，请检查账号和密码" {
		t.Errorf("message = %q, want friendly auth error", msg)
	}
}

// 编辑场景：请求不带密码时，必须用库里已存的密码解密后测试，
// 否则用户每次想测连接都得重新输入密码。
func TestTestConnection_UsesStoredPasswordWhenOmitted(t *testing.T) {
	store := &memDatasources{items: []model.Datasource{{
		ID: 7, Name: "shop", Type: model.TypePostgreSQL,
		Host: "h", Port: 5432, Database: "shop",
		Username: "readonly", Password: "stored-secret", HasPassword: true,
	}}}

	st := &stubTester{res: &TestConnResult{OK: true, Message: "连接成功"}}
	r := testConnRouter(t, "dba", st, store)

	tok := loginAndGetToken(t, r, "alice", "pw")
	w := postTestConn(t, r, tok, map[string]any{
		"type":          "postgresql",
		"host":          "h",
		"port":          5432,
		"database":      "shop",
		"username":      "readonly",
		"datasource_id": 7,
		// 故意不传 password
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if st.got.Password != "stored-secret" {
		t.Errorf("password = %q, want the stored password", st.got.Password)
	}
}

// 请求里显式填了密码时，优先用请求的，不要被库里旧值覆盖。
func TestTestConnection_RequestPasswordWins(t *testing.T) {
	store := &memDatasources{items: []model.Datasource{{
		ID: 7, Type: model.TypePostgreSQL, Password: "old-secret", HasPassword: true,
	}}}
	st := &stubTester{res: &TestConnResult{OK: true}}
	r := testConnRouter(t, "dba", st, store)

	tok := loginAndGetToken(t, r, "alice", "pw")
	postTestConn(t, r, tok, map[string]any{
		"type": "postgresql", "host": "h", "port": 5432,
		"password": "new-secret", "datasource_id": 7,
	})

	if st.got.Password != "new-secret" {
		t.Errorf("password = %q, want the one from request", st.got.Password)
	}
}

func TestTestConnection_RejectsUnknownType(t *testing.T) {
	r := testConnRouter(t, "dba", &stubTester{}, &memDatasources{})
	tok := loginAndGetToken(t, r, "alice", "pw")
	w := postTestConn(t, r, tok, map[string]any{"type": "oracle", "host": "h", "port": 1521})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestFriendlyConnError(t *testing.T) {
	cases := []struct {
		err  string
		want string
	}{
		{`failed SASL auth: FATAL: password authentication failed for user "x"`, "认证失败，请检查账号和密码"},
		{"dial tcp 1.2.3.4:5432: i/o timeout", "连接超时，请检查主机、端口和网络连通性"},
		{"dial tcp: connect: connection refused", "无法连接到目标主机，请检查主机和端口"},
		// Windows 的错误文案与 Unix 不同，实测是 "No connection could be made ... actively refused"
		{"dial error: dial tcp 10.0.0.5:5999: connectex: No connection could be made because the target machine actively refused it.", "无法连接到目标主机，请检查主机和端口"},
		{"unknown database 'foo'", "库不存在，请检查库名（Redis 请填 db 号）"},
		// MongoDB 的 Unauthorized 是「连上了但没权限」，与密码错不同，必须分开提示
		{"mongo permission check: (Unauthorized) command listCollections requires authentication", "权限不足：目标库已开启认证，请填写有查询权限的账号"},
	}
	for _, c := range cases {
		got := friendlyConnError(errors.New(c.err))
		if got != c.want {
			t.Errorf("err %q -> %q, want %q", c.err, got, c.want)
		}
	}
}
