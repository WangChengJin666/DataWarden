package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/opensourceprj/datawarden/internal/auth"
	"github.com/opensourceprj/datawarden/internal/model"
)

// fakeGrantStore 记录被调用了哪个查询方法，并返回固定结果。
//
// 这个测试关心的是「按角色走哪条分支」，不是 SQL 是否正确，
// 所以把两个方法的结果都记下来，断言调用的是哪一个。
type fakeGrantStore struct {
	mineCalled   bool
	allCalled    bool
	mineItems    []model.DatasourceBrief
	allItems     []model.DatasourceBrief
	listCalled   bool
	revokeCalled bool
	// exportArgs 记录 SetCanExport 收到的参数，用于断言导出开关接口。
	exportCalled bool
	exportHit    bool
	exportValue  bool
}

func (f *fakeGrantStore) ListActive(context.Context, int) ([]model.GrantView, error) {
	f.listCalled = true
	return []model.GrantView{}, nil
}

func (f *fakeGrantStore) Get(context.Context, int64) (*model.Grant, error) {
	return &model.Grant{}, nil
}

func (f *fakeGrantStore) Revoke(context.Context, int64) error {
	f.revokeCalled = true
	return nil
}

func (f *fakeGrantStore) SetCanExport(_ context.Context, _ int64, canExport bool) (bool, error) {
	f.exportCalled = true
	f.exportValue = canExport
	return f.exportHit, nil
}

func (f *fakeGrantStore) MyDatasources(context.Context, int64) ([]model.DatasourceBrief, error) {
	f.mineCalled = true
	return f.mineItems, nil
}

func (f *fakeGrantStore) AllDatasources(context.Context) ([]model.DatasourceBrief, error) {
	f.allCalled = true
	return f.allItems, nil
}

// listMineAs 用指定角色调一次 /grants/mine，返回解析后的结果。
func listMineAs(t *testing.T, role string, store *fakeGrantStore) []model.DatasourceBrief {
	t.Helper()
	engine := buildRouterWithGrantStore(t, role, store)

	token := loginAndGetToken(t, engine, "alice", "correct")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/grants/mine", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []model.DatasourceBrief `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return resp.Items
}

// buildRouterWithGrantStore 装配一个可注入授权存储的路由。
//
// 不用现有的 buildRouterWithStore：它在 Deps 里塞的是空 QueryHandler，
// 挂上 Grant 之后 /grants/mine 才存在。这里只覆盖测试需要的那部分依赖，
// 其余保持 nil（NewRouter 对 nil 的各项跳过注册）。
func buildRouterWithGrantStore(t *testing.T, role string, gs GrantStore) http.Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	issuer := auth.NewTokenIssuer("test-secret", time.Hour)
	return NewRouter(Deps{
		Issuer:      issuer,
		AuthHandler: &AuthHandler{Users: &memUsers{passwords: map[string]string{"alice": "correct"}, role: role}, Issuer: issuer},
		Grant:       &GrantHandler{Store: gs},
	})
}

// 管理角色走全量清单：他们不需要授权，下拉不能受 grant_record 限制。
//
// 这是「数据源管理有数据、查询页下拉为空」那个现象的修复点：
// 管理角色一条授权都没有，若仍走 MyDatasources，下拉必然是空的。
func TestListMine_PrivilegedRolesSeeAllDatasources(t *testing.T) {
	for _, role := range []string{"dba", "superadmin"} {
		t.Run(role, func(t *testing.T) {
			store := &fakeGrantStore{
				mineItems: []model.DatasourceBrief{},
				allItems: []model.DatasourceBrief{
					{ID: 1, Name: "pg开发库", Type: model.TypePostgreSQL},
					{ID: 2, Name: "redis", Type: model.TypeRedis},
				},
			}
			items := listMineAs(t, role, store)

			if !store.allCalled {
				t.Fatal("privileged role must use AllDatasources")
			}
			if store.mineCalled {
				t.Fatal("privileged role must not be filtered by grant_record")
			}
			if len(items) != 2 {
				t.Fatalf("want 2 datasources, got %d", len(items))
			}
		})
	}
}

// 开发者仍然只看得到自己拿到授权的数据源，不能因为这次改动被放开。
func TestListMine_DeveloperStillRestrictedToGrants(t *testing.T) {
	store := &fakeGrantStore{
		mineItems: []model.DatasourceBrief{{ID: 1, Name: "pg开发库", Type: model.TypePostgreSQL}},
		allItems:  []model.DatasourceBrief{{ID: 1}, {ID: 2}, {ID: 3}},
	}
	items := listMineAs(t, "developer", store)

	if store.allCalled {
		t.Fatal("developer must not reach AllDatasources")
	}
	if !store.mineCalled {
		t.Fatal("developer must use MyDatasources")
	}
	if len(items) != 1 {
		t.Fatalf("want 1 datasource, got %d", len(items))
	}
}
