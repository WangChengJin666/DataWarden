package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/opensourceprj/datawarden/internal/auth"
	"github.com/opensourceprj/datawarden/internal/redisq"
)

// stubRedisQueryer 返回固定结果，并记录被调用的请求。
type stubRedisQueryer struct {
	res    *redisq.QueryResponse
	err    error
	called bool
	got    redisq.QueryRequest
}

func (s *stubRedisQueryer) Query(ctx context.Context, req redisq.QueryRequest) (*redisq.QueryResponse, error) {
	s.called = true
	s.got = req
	return s.res, s.err
}

// stubRedisFactory 返回固定的 target。
func stubRedisFactory(q redisq.Queryer, db int) RedisTargetFactory {
	return func(ctx context.Context, id int64) (*RedisTarget, error) {
		return &RedisTarget{Query: q, Database: db}, nil
	}
}

func redisRouter(t *testing.T, enforcerAllow bool, q redisq.Queryer, db int) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	issuer := auth.NewTokenIssuer("test-secret", time.Hour)
	return NewRouter(Deps{
		Issuer:      issuer,
		AuthHandler: &AuthHandler{Users: &memUsers{passwords: map[string]string{"alice": "pw"}}, Issuer: issuer},
		Datasource:  &DatasourceHandler{Store: &memDatasources{}},
		Query:       &QueryHandler{},
		Redis: &RedisHandler{
			Enforcer: &recordingEnforcer{allowed: enforcerAllow},
			Factory:  stubRedisFactory(q, db),
		},
	})
}

func postRedis(t *testing.T, r http.Handler, token string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/query/redis", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestRedisQuery_RequiresAuth(t *testing.T) {
	r := redisRouter(t, true, &stubRedisQueryer{res: &redisq.QueryResponse{}}, 37)
	w := postRedis(t, r, "", map[string]any{"datasource_id": 1, "key": "a:b"})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestRedisQuery_DeniedWithoutPermission(t *testing.T) {
	q := &stubRedisQueryer{res: &redisq.QueryResponse{}}
	r := redisRouter(t, false, q, 37)

	tok := loginAndGetToken(t, r, "alice", "pw")
	w := postRedis(t, r, tok, map[string]any{
		"datasource_id": 1,
		"key":           "user:1:name",
	})

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body=%s", w.Code, w.Body.String())
	}
	if q.called {
		t.Error("queryer must not be called when authorization fails")
	}
}

func TestRedisQuery_ExactKeySuccess(t *testing.T) {
	q := &stubRedisQueryer{res: &redisq.QueryResponse{
		Exact: true,
		Items: []*redisq.KeyValue{{Key: "user:1:name", Type: "string", Value: "alice"}},
	}}
	r := redisRouter(t, true, q, 37)
	tok := loginAndGetToken(t, r, "alice", "pw")

	w := postRedis(t, r, tok, map[string]any{
		"datasource_id": 1,
		"key":           "user:1:name",
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	if !q.called {
		t.Fatal("queryer should be called")
	}
	if q.got.Key != "user:1:name" {
		t.Errorf("key = %q", q.got.Key)
	}
}

func TestRedisQuery_PatternSuccess(t *testing.T) {
	q := &stubRedisQueryer{res: &redisq.QueryResponse{
		Exact: false,
		Items: []*redisq.KeyValue{{Key: "order:1:a", Type: "string"}},
	}}
	r := redisRouter(t, true, q, 37)
	tok := loginAndGetToken(t, r, "alice", "pw")

	w := postRedis(t, r, tok, map[string]any{
		"datasource_id": 1,
		"pattern":       "order:1:*",
		"row_limit":     10,
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	if q.got.Pattern != "order:1:*" {
		t.Errorf("pattern = %q", q.got.Pattern)
	}
}

func TestRedisQuery_RejectsEmptySubject(t *testing.T) {
	r := redisRouter(t, true, &stubRedisQueryer{res: &redisq.QueryResponse{}}, 37)
	tok := loginAndGetToken(t, r, "alice", "pw")

	w := postRedis(t, r, tok, map[string]any{"datasource_id": 1})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestRedisQuery_BroadPatternIsBadRequest(t *testing.T) {
	q := &stubRedisQueryer{err: redisq.ErrPatternTooBroad}
	r := redisRouter(t, true, q, 37)
	tok := loginAndGetToken(t, r, "alice", "pw")

	w := postRedis(t, r, tok, map[string]any{
		"datasource_id": 1,
		"pattern":       "*",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", w.Code, w.Body.String())
	}
}

// 鉴权 object 必须包含 db 号，且与实际数据源配置一致
func TestRedisQuery_AuthObjectIncludesDB(t *testing.T) {
	enforcer := &recordingEnforcer{allowed: true}
	q := &stubRedisQueryer{res: &redisq.QueryResponse{}}

	gin.SetMode(gin.TestMode)
	issuer := auth.NewTokenIssuer("test-secret", time.Hour)
	r := NewRouter(Deps{
		Issuer:      issuer,
		AuthHandler: &AuthHandler{Users: &memUsers{passwords: map[string]string{"alice": "pw"}}, Issuer: issuer},
		Datasource:  &DatasourceHandler{Store: &memDatasources{}},
		Query:       &QueryHandler{},
		Redis: &RedisHandler{
			Enforcer: enforcer,
			Factory:  stubRedisFactory(q, 37),
		},
	})

	tok := loginAndGetToken(t, r, "alice", "pw")
	postRedis(t, r, tok, map[string]any{"datasource_id": 5, "key": "user:1:name"})

	want := "ds:5:37:user:1:name"
	if enforcer.lastObj != want {
		t.Errorf("auth object = %q, want %q", enforcer.lastObj, want)
	}
}

// KeyCandidates 必须由具体到宽泛展开，这是前缀授权能生效的前提。
func TestKeyCandidates(t *testing.T) {
	got := KeyCandidates("user:1001:name")
	want := []string{
		"user:1001:name",
		"user:1001:*",
		"user:1001",
		"user:*",
		"user",
		"*",
	}
	if len(got) != len(want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("candidates[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// 带通配符的 pattern 必须先截取字面前缀，否则 * 会被当成层级的一部分。
func TestKeyCandidates_WildcardPattern(t *testing.T) {
	got := KeyCandidates("order:1:*")
	if got[0] != "order:1" {
		t.Errorf("first candidate = %q, want %q", got[0], "order:1")
	}
}

// prefixEnforcer 只在 object 命中指定策略时放行，模拟真实 Casbin 行为。
type prefixEnforcer struct {
	policy  string
	lastObj string
	calls   []string
}

func (p *prefixEnforcer) Enforce(subject, object, action string) bool {
	p.lastObj = object
	p.calls = append(p.calls, object)
	return object == p.policy
}

// 授权 user:1001 后，查询 user:1001:name 必须放行。
func TestRedisQuery_PrefixGrantAllowsChildKey(t *testing.T) {
	enforcer := &prefixEnforcer{policy: "ds:5:37:user:1001:*"}
	q := &stubRedisQueryer{res: &redisq.QueryResponse{}}

	gin.SetMode(gin.TestMode)
	issuer := auth.NewTokenIssuer("test-secret", time.Hour)
	r := NewRouter(Deps{
		Issuer:      issuer,
		AuthHandler: &AuthHandler{Users: &memUsers{passwords: map[string]string{"alice": "pw"}}, Issuer: issuer},
		Datasource:  &DatasourceHandler{Store: &memDatasources{}},
		Query:       &QueryHandler{},
		Redis: &RedisHandler{
			Enforcer: enforcer,
			Factory:  stubRedisFactory(q, 37),
		},
	})

	tok := loginAndGetToken(t, r, "alice", "pw")
	w := postRedis(t, r, tok, map[string]any{"datasource_id": 5, "key": "user:1001:name"})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	if !q.called {
		t.Error("queryer should be called after prefix grant")
	}
}

// 授权 user:1001 后，查询 user:1002:name 必须拒绝，不能越界。
func TestRedisQuery_PrefixGrantDoesNotLeakSibling(t *testing.T) {
	enforcer := &prefixEnforcer{policy: "ds:5:37:user:1001:*"}
	q := &stubRedisQueryer{res: &redisq.QueryResponse{}}

	gin.SetMode(gin.TestMode)
	issuer := auth.NewTokenIssuer("test-secret", time.Hour)
	r := NewRouter(Deps{
		Issuer:      issuer,
		AuthHandler: &AuthHandler{Users: &memUsers{passwords: map[string]string{"alice": "pw"}}, Issuer: issuer},
		Datasource:  &DatasourceHandler{Store: &memDatasources{}},
		Query:       &QueryHandler{},
		Redis: &RedisHandler{
			Enforcer: enforcer,
			Factory:  stubRedisFactory(q, 37),
		},
	})

	tok := loginAndGetToken(t, r, "alice", "pw")
	w := postRedis(t, r, tok, map[string]any{"datasource_id": 5, "key": "user:1002:name"})

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body=%s", w.Code, w.Body.String())
	}
	if q.called {
		t.Error("queryer must not be called for unauthorized sibling key")
	}
}
