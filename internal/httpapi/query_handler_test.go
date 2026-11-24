package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/opensourceprj/datawarden/internal/adapter"
	"github.com/opensourceprj/datawarden/internal/auth"
	"github.com/opensourceprj/datawarden/internal/querysvc"
)

// countingEnforcer 记录每次鉴权的 object，并按固定结果放行 / 拒绝。
//
// 与 testhelper_test.go 里的 recordingEnforcer 分开：那个只保留最后一个
// object，而这里要断言逐表鉴权调了几次、每次的对象是什么。
type countingEnforcer struct {
	allowed bool
	objects []string
}

func (c *countingEnforcer) Enforce(subject, object, action string) bool {
	c.objects = append(c.objects, object)
	return c.allowed
}

// failingQueryAdapter 鉴权通过后必然执行失败。
//
// 本测试只关心「有没有走到执行」，用必然失败的 adapter 可以确认
// 请求不是被鉴权 / 校验环节拦下的，同时不出网、无副作用。
type failingQueryAdapter struct{}

func (failingQueryAdapter) Ping(context.Context) error                { return nil }
func (failingQueryAdapter) ListDatabases(context.Context) ([]string, error) { return nil, nil }
func (failingQueryAdapter) ListTables(context.Context, string) ([]string, error) {
	return nil, nil
}
func (failingQueryAdapter) Query(context.Context, string, int) (*adapter.QueryResult, error) {
	return nil, context.DeadlineExceeded
}
func (failingQueryAdapter) QueryWithArgs(context.Context, string, []any, int) (*adapter.QueryResult, error) {
	return nil, context.DeadlineExceeded
}
func (failingQueryAdapter) Close() error { return nil }

// buildSQLRouter 装配一个只挂 SQL 查询的路由。
func buildSQLRouter(t *testing.T, role string, enf querysvc.Enforcer) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	issuer := auth.NewTokenIssuer("test-secret", time.Hour)
	return NewRouter(Deps{
		Issuer:      issuer,
		AuthHandler: &AuthHandler{Users: &memUsers{passwords: map[string]string{"alice": "pw"}, role: role}, Issuer: issuer},
		Query: &QueryHandler{
			Service: newTestQueryServiceWithEnforcer(&recordingAuditor{}, enf),
			Factory: func(int64) (adapter.Adapter, error) { return failingQueryAdapter{}, nil },
		},
	})
}

// 任何角色（含开发者）只选库、不选表都能查询。
//
// 这是当前产品口径的直接体现：授权粒度是「数据源 + 库」，
// 库下所有表都可查，用户不该为了执行一条 SQL 先去勾表。
//
// 期望的鉴权对象恒为 ds:<id>:<库>:*，一次就够。
func TestExecuteSQL_NoTablesRequired(t *testing.T) {
	for _, role := range []string{"developer", "dba", "superadmin"} {
		t.Run(role, func(t *testing.T) {
			enf := &countingEnforcer{allowed: true}
			engine := buildSQLRouter(t, role, enf)
			tok := loginAndGetToken(t, engine, "alice", "pw")

			w := postJSON(t, engine, tok, "/api/v1/query/sql", map[string]any{
				"datasource_id": 3,
				"database":      "shop",
				"sql":           "SELECT * FROM orders",
			})

			if len(enf.objects) != 1 {
				t.Fatalf("want exactly 1 authz check, got %v", enf.objects)
			}
			if want := "ds:3:shop:*"; enf.objects[0] != want {
				t.Errorf("auth object = %q, want %q", enf.objects[0], want)
			}
			// 鉴权通过后会走到执行并失败（500，替身必然报错）。
			// 只要不是 400，就说明没有在「没选表」这一步被拦下。
			if w.Code == http.StatusBadRequest {
				t.Errorf("query without tables must not be rejected as bad request: %s", w.Body.String())
			}
		})
	}
}

// 缺库仍然 400：库名是鉴权对象的一部分，没有它无法判定。
func TestExecuteSQL_MissingDatabaseRejected(t *testing.T) {
	enf := &countingEnforcer{allowed: true}
	engine := buildSQLRouter(t, "developer", enf)
	tok := loginAndGetToken(t, engine, "alice", "pw")

	w := postJSON(t, engine, tok, "/api/v1/query/sql", map[string]any{
		"datasource_id": 3,
		"sql":           "SELECT 1",
	})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", w.Code, w.Body.String())
	}
	if len(enf.objects) != 0 {
		t.Errorf("missing database must not reach authz, got %v", enf.objects)
	}
}

// 无库授权时 403。表清单里写了什么都不影响——鉴权只看库。
func TestExecuteSQL_NoDatabaseGrantIsForbidden(t *testing.T) {
	enf := &countingEnforcer{allowed: false}
	engine := buildSQLRouter(t, "developer", enf)
	tok := loginAndGetToken(t, engine, "alice", "pw")

	w := postJSON(t, engine, tok, "/api/v1/query/sql", map[string]any{
		"datasource_id": 3,
		"database":      "shop",
		// 即便声明了一张「应该有权限」的表，也不能凭它放行
		"tables": []string{"orders"},
		"sql":    "SELECT 1",
	})

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", w.Code, w.Body.String())
	}
	if len(enf.objects) != 1 || enf.objects[0] != "ds:3:shop:*" {
		t.Errorf("auth objects = %v, want a single database-level check", enf.objects)
	}
}
