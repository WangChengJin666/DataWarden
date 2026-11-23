package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/opensourceprj/datawarden/internal/model"
)

// captureRequests 记录 Create 收到的申请单，用于断言落库形态。
type captureRequests struct {
	got []model.GrantRequest
}

func (c *captureRequests) Create(ctx context.Context, r *model.GrantRequest) (int64, error) {
	c.got = append(c.got, *r)
	return 1, nil
}
func (c *captureRequests) Get(ctx context.Context, id int64) (*model.GrantRequest, error) {
	return nil, nil
}
func (c *captureRequests) ListByStatus(ctx context.Context, status string, limit int) ([]model.GrantRequest, error) {
	return nil, nil
}
func (c *captureRequests) ListByUser(ctx context.Context, userID int64, limit int) ([]model.GrantRequest, error) {
	return nil, nil
}
func (c *captureRequests) Approve(ctx context.Context, reqID, reviewerID int64, comment string) (*model.Grant, error) {
	return nil, nil
}
func (c *captureRequests) Reject(ctx context.Context, reqID, reviewerID int64, comment string) error {
	return nil
}
func (c *captureRequests) Cancel(ctx context.Context, reqID, userID int64) error { return nil }

// TestCreateGrantRequest_NormalizesBareDatabaseScope 是「整库授权失效」的入口回归测试。
//
// 前端提交整库时传裸库名 "shop"。若不在此处归一成 "shop:*"，
// 落库的 scope 只有库名，生成策略 ds:1:shop（两段），
// 而鉴权请求是 ds:1:shop:orders（四段），keyMatch 永不匹配——
// 用户走完申请→审批后一条表都查不了，且没有任何报错。
//
// 出口（SyncGrants）也有兜底，但那条兜底掩盖了这个入口的回归：
// 即使删掉 Create 里的 NormalizeScope，查询依然能用，
// 只是库里存的是脏形态。所以必须有这条测试锁住「落库形态」。
func TestCreateGrantRequest_NormalizesBareDatabaseScope(t *testing.T) {
	store := &captureRequests{}
	h := &GrantRequestHandler{
		Requests: store,
		Datasrc: &applyableStub{items: []model.Datasource{{
			ID: 1, Name: "shop", Type: model.TypePostgreSQL,
			ApplyEnabled: true, MaxApplyDays: 7,
			ApplyScopes:  []model.ApplyScope{{Database: "shop"}}, // 整库开放
		}}},
	}

	body, _ := json.Marshal(map[string]any{
		"datasource_id": 1,
		"scope":         "shop", // 前端传的是裸库名
		"days":          3,
		"reason":        "排查线上问题",
	})
	if w := doCreate(t, h, body); w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body=%s", w.Code, w.Body.String())
	}

	if len(store.got) != 1 {
		t.Fatalf("应当创建 1 条申请，实际 %d 条", len(store.got))
	}
	if got := store.got[0].Scope; got != "shop:*" {
		t.Errorf("落库 scope = %q，want %q（裸库名必须归一成 整库形态，"+
			"否则生成的策略是两段、永远匹配不上四段鉴权请求）", got, "shop:*")
	}
}

// 畸形 scope 必须在入口被拒绝，不能落库。
//
// 尤其 "*"：若放行，NormalizeScope 的防护被绕过后会拼出
// ds:<id>:*:*，等于本数据源全库全表。
func TestCreateGrantRequest_RejectsMalformedScope(t *testing.T) {
	cases := []struct{ scope, why string }{
		{"*", "纯通配 = 全数据源"},
		{":orders", "库名为空"},
		{"a:b:c", "多余冒号"},
		{"shop:ord*", "表名含通配符"},
		{"", "空 scope"},
	}
	for _, c := range cases {
		t.Run(c.scope, func(t *testing.T) {
			store := &captureRequests{}
			h := &GrantRequestHandler{
				Requests: store,
				Datasrc: &applyableStub{items: []model.Datasource{{
					ID: 1, Name: "shop", Type: model.TypePostgreSQL,
					ApplyEnabled: true, MaxApplyDays: 7,
					ApplyScopes:  []model.ApplyScope{{Database: "shop"}},
				}}},
			}
			body, _ := json.Marshal(map[string]any{
				"datasource_id": 1, "scope": c.scope, "days": 3, "reason": "x",
			})
			w := doCreate(t, h, body)
			if w.Code == http.StatusCreated {
				t.Fatalf("scope=%q 不应被接受（%s）", c.scope, c.why)
			}
			if len(store.got) != 0 {
				t.Errorf("非法 scope 不应落库，实际落了 %d 条", len(store.got))
			}
		})
	}
}

// 表级 scope 应原样保留，不被归一整库。
func TestCreateGrantRequest_KeepsTableLevelScope(t *testing.T) {
	store := &captureRequests{}
	h := &GrantRequestHandler{
		Requests: store,
		Datasrc: &applyableStub{items: []model.Datasource{{
			ID: 1, Name: "shop", Type: model.TypePostgreSQL,
			ApplyEnabled: true, MaxApplyDays: 7,
			ApplyScopes:  []model.ApplyScope{{Database: "shop", Tables: []string{"orders", "users"}}},
		}}},
	}
	body, _ := json.Marshal(map[string]any{
		"datasource_id": 1, "scope": "shop:orders", "days": 3, "reason": "x",
	})
	if w := doCreate(t, h, body); w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body=%s", w.Code, w.Body.String())
	}
	if got := store.got[0].Scope; got != "shop:orders" {
		t.Errorf("落库 scope = %q，want shop:orders", got)
	}
}

// applyableStub 提供 ListApplyable / Get。
type applyableStub struct{ items []model.Datasource }

func (a *applyableStub) ListApplyable(ctx context.Context) ([]model.Datasource, error) {
	return a.items, nil
}
func (a *applyableStub) Get(ctx context.Context, id int64) (*model.Datasource, error) {
	for i := range a.items {
		if a.items[i].ID == id {
			d := a.items[i]
			return &d, nil
		}
	}
	return nil, errNotFound{}
}

type errNotFound struct{}

func (errNotFound) Error() string { return "not found" }

// doCreate 直接对 Create handler 发请求（注入身份，绕开 JWT）。
func doCreate(t *testing.T, h *GrantRequestHandler, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(ctxUserID, int64(7))
		c.Set(ctxUsername, "dev")
		c.Set(ctxRole, string(model.RoleDeveloper))
		c.Next()
	})
	r.POST("/grant/requests", h.Create)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/grant/requests", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}
