package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/opensourceprj/datawarden/internal/authz"
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

// TestCreateGrantRequest_LandsAllDatabasesScope 锁住「申请粒度是数据源」这一口径。
//
// 申请页已不再收库名，审批通过即拥有该数据源下所有库表。
// 落库形态必须是 authz.ScopeAllDatabases，生成策略 ds:<id>:*:*；
// 若退回成某个库的 scope，用户会「审批通过但换个库就 403」，
// 而界面上看不出差别。
//
// 同时验证旧客户端仍然会传的 scope 字段被忽略：
// 客户端自述的范围不能决定权限，否则一个改了前端的旧版本
// 就能绕过新口径继续申请单库。
func TestCreateGrantRequest_LandsAllDatabasesScope(t *testing.T) {
	cases := []struct {
		name     string
		reqScope any // 不传 / 传库名 / 传通配
	}{
		{"不传 scope", nil},
		{"传库名（旧客户端）", "shop"},
		{"传整库写法", "shop:*"},
		{"传整数据源", "*"},
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
			body := map[string]any{
				"datasource_id": 1, "days": 3, "reason": "排查线上问题",
			}
			if c.reqScope != nil {
				body["scope"] = c.reqScope
			}
			raw, _ := json.Marshal(body)
			if w := doCreate(t, h, raw); w.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201, body=%s", w.Code, w.Body.String())
			}
			if len(store.got) != 1 {
				t.Fatalf("应当创建 1 条申请，实际 %d 条", len(store.got))
			}
			if got := store.got[0].Scope; got != authz.ScopeAllDatabases {
				t.Errorf("落库 scope = %q，want %q（申请粒度是数据源，"+
					"客户端传什么都不能改变这一点）", got, authz.ScopeAllDatabases)
			}
		})
	}
}

// 未开放申请的数据源、天数越界仍然必须拒绝——放开的是「范围」，不是审核门槛。
func TestCreateGrantRequest_StillValidatesApplyGate(t *testing.T) {
	newHandler := func(enabled bool, maxDays int) (*GrantRequestHandler, *captureRequests) {
		store := &captureRequests{}
		return &GrantRequestHandler{
			Requests: store,
			Datasrc: &applyableStub{items: []model.Datasource{{
				ID: 1, Name: "shop", Type: model.TypePostgreSQL,
				ApplyEnabled: enabled, MaxApplyDays: maxDays,
			}}},
		}, store
	}

	t.Run("未开放申请", func(t *testing.T) {
		h, store := newHandler(false, 7)
		raw, _ := json.Marshal(map[string]any{
			"datasource_id": 1, "days": 3, "reason": "x",
		})
		if w := doCreate(t, h, raw); w.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403, body=%s", w.Code, w.Body.String())
		}
		if len(store.got) != 0 {
			t.Error("未开放申请的数据源不应落库")
		}
	})

	t.Run("天数超上限", func(t *testing.T) {
		h, store := newHandler(true, 7)
		raw, _ := json.Marshal(map[string]any{
			"datasource_id": 1, "days": 8, "reason": "x",
		})
		if w := doCreate(t, h, raw); w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400, body=%s", w.Code, w.Body.String())
		}
		if len(store.got) != 0 {
			t.Error("天数越界不应落库")
		}
	})
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
