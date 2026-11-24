package httpapi

import (
	"context"
	"net/http"
	"strings"
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

// 管理角色不选表直接查询必须放行。
//
// 前端表下拉写的是「选填，管理角色可不填」。若后端拿着空清单去做逐表鉴权，
// querysvc 会返回 ErrNoTables，用户看到的就是 400
// "tables field is required for authorization"——与界面上的「可不填」正面冲突。
//
// 期望的鉴权对象是 ds:<id>:<库>:*，与「选了整库全部表」等价。
func TestExecuteSQL_PrivilegedRoleWithEmptyTablesPassesAuthz(t *testing.T) {
	for _, role := range []string{"dba", "superadmin"} {
		t.Run(role, func(t *testing.T) {
			enf := &countingEnforcer{allowed: true}
			engine := buildSQLRouter(t, role, enf)
			tok := loginAndGetToken(t, engine, "alice", "pw")

			w := postJSON(t, engine, tok, "/api/v1/query/sql", map[string]any{
				"datasource_id": 3,
				"database":      "shop",
				"sql":           "SELECT 1",
			})

			if len(enf.objects) != 1 {
				t.Fatalf("want exactly 1 authz check, got %v", enf.objects)
			}
			if want := "ds:3:shop:*"; enf.objects[0] != want {
				t.Errorf("auth object = %q, want %q", enf.objects[0], want)
			}
			// 鉴权通过后会走到执行并失败（500）。只要不是 400，
			// 就说明没有在「缺表」这一步被拦下。
			if w.Code == http.StatusBadRequest {
				t.Errorf("privileged empty-tables query must not be a bad request: %s", w.Body.String())
			}
		})
	}
}

// 开发者不选表仍然 400，且提示缺表：上面那条放宽不能顺带放开开发者。
func TestExecuteSQL_DeveloperWithEmptyTablesStillRejected(t *testing.T) {
	enf := &countingEnforcer{allowed: true}
	engine := buildSQLRouter(t, "developer", enf)
	tok := loginAndGetToken(t, engine, "alice", "pw")

	w := postJSON(t, engine, tok, "/api/v1/query/sql", map[string]any{
		"datasource_id": 3,
		"database":      "shop",
		"sql":           "SELECT 1",
	})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "请先选择") {
		t.Errorf("body = %s, want it to mention missing tables", w.Body.String())
	}
	if len(enf.objects) != 0 {
		t.Errorf("developer with empty tables must not reach authz, got %v", enf.objects)
	}
}

// 开发者显式选表时，鉴权对象必须按表逐个拼接，不能被折叠成整库通配。
//
// 逐表鉴权遇第一个无权限的表就整体返回 ErrForbidden（不暴露是哪张表），
// 所以这里只能断言第一次鉴权的对象是具体表名而非 shop:*。
func TestExecuteSQL_DeveloperTablesNotFolded(t *testing.T) {
	enf := &countingEnforcer{allowed: false}
	engine := buildSQLRouter(t, "developer", enf)
	tok := loginAndGetToken(t, engine, "alice", "pw")

	w := postJSON(t, engine, tok, "/api/v1/query/sql", map[string]any{
		"datasource_id": 3,
		"database":      "shop",
		"tables":        []string{"orders", "users"},
		"sql":           "SELECT 1",
	})

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", w.Code, w.Body.String())
	}
	if len(enf.objects) == 0 {
		t.Fatal("expected at least one authz check")
	}
	if want := "ds:3:shop:orders"; enf.objects[0] != want {
		t.Errorf("first auth object = %q, want %q (tables must not be folded into shop:*)",
			enf.objects[0], want)
	}
}
