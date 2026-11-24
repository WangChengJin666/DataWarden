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
	// applied 模拟「当前用户在这些数据源上已有待审批申请或生效授权」，
	// 用于验证重复申请被拦、以及列表上的 applied 标记。
	applied map[int64]bool
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
func (c *captureRequests) AppliedDatasourceIDs(ctx context.Context, userID int64) (map[int64]bool, error) {
	if c.applied == nil {
		return map[int64]bool{}, nil
	}
	return c.applied, nil
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

// 天数与理由必须原样落库。
//
// 这两个字段没有校验逻辑可依赖，全靠一行赋值，所以最容易在重构
// （尤其是本次移除 scope 字段）时被顺手删掉而无人发现：
// Days 决定授权到期时间，丢了会落成 0 天，审批通过即过期；
// Reason 是审批人唯一的判断依据。
func TestCreateGrantRequest_PersistsDaysAndReason(t *testing.T) {
	store := &captureRequests{}
	h := &GrantRequestHandler{
		Requests: store,
		Datasrc: &applyableStub{items: []model.Datasource{{
			ID: 1, Name: "shop", Type: model.TypePostgreSQL,
			ApplyEnabled: true, MaxApplyDays: 14,
		}}},
	}
	raw, _ := json.Marshal(map[string]any{
		"datasource_id": 1, "days": 5, "reason": "排查订单超时，需要看近 3 天数据",
	})
	if w := doCreate(t, h, raw); w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body=%s", w.Code, w.Body.String())
	}
	if len(store.got) != 1 {
		t.Fatalf("应当创建 1 条申请，实际 %d 条", len(store.got))
	}
	got := store.got[0]
	if got.Days != 5 {
		t.Errorf("落库 days = %d, want 5（该值决定授权到期时间）", got.Days)
	}
	if got.Reason != "排查订单超时，需要看近 3 天数据" {
		t.Errorf("落库 reason = %q，与请求不一致", got.Reason)
	}
	if got.DatasourceID != 1 || got.UserID != 7 {
		t.Errorf("落库 datasource_id=%d user_id=%d，want 1 / 7",
			got.DatasourceID, got.UserID)
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

// TestCreateGrantRequest_RejectsDuplicatePending 锁住「同一数据源不重复排队」。
//
// 前端会把已申请的行标灰，但那是展示层：手工调接口、或者两个标签页
// 同时点提交，都能塞进第二条。审批人看到两条一模一样的单子，批掉一条
// 另一条还挂着，用户会以为申请没成功——所以判定必须在服务端。
func TestCreateGrantRequest_RejectsDuplicatePending(t *testing.T) {
	// 情形一：还有待审批的申请单；情形二：已有生效中的授权。
	// 后者是真实踩过的坑——审批通过后按钮还写着「申请授权」，
	// 因为判据只认了 pending。
	cases := []struct {
		name string
	}{
		{"待审批申请单"},
		{"生效中的授权"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := &captureRequests{applied: map[int64]bool{1: true}}
			h := &GrantRequestHandler{
				Requests: store,
				Datasrc: &applyableStub{items: []model.Datasource{{
					ID: 1, Name: "shop", Type: model.TypePostgreSQL,
					ApplyEnabled: true, MaxApplyDays: 7,
				}}},
			}
			raw, _ := json.Marshal(map[string]any{
				"datasource_id": 1, "days": 3, "reason": "再次申请",
			})
			w := doCreate(t, h, raw)
			if w.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409, body=%s", w.Code, w.Body.String())
			}
			if len(store.got) != 0 {
				t.Errorf("已申请时不应再落库，实际落了 %d 条", len(store.got))
			}
		})
	}
}

// TestListApplyable_MarksApplied 验证「已申请」标记随列表下发。
//
// 前端靠这个字段决定按钮显示「申请授权」还是「已申请」，
// 漏标会让用户以为还能再提交一次（提交后才发现被 409 拒绝）。
func TestListApplyable_MarksApplied(t *testing.T) {
	store := &captureRequests{applied: map[int64]bool{2: true}}
	h := &GrantRequestHandler{
		Requests: store,
		Datasrc: &applyableStub{items: []model.Datasource{
			{ID: 1, Name: "shop", Type: model.TypePostgreSQL, ApplyEnabled: true},
			{ID: 2, Name: "order", Type: model.TypeMySQL, ApplyEnabled: true},
		}},
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(ctxUserID, int64(7))
		c.Set(ctxUsername, "dev")
		c.Set(ctxRole, string(model.RoleDeveloper))
		c.Next()
	})
	r.GET("/grant/applyable", h.ListApplyable)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/grant/applyable", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []struct {
			ID      int64 `json:"id"`
			Applied bool  `json:"applied"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("应当返回 2 个数据源，实际 %d 个", len(resp.Items))
	}
	got := map[int64]bool{}
	for _, it := range resp.Items {
		got[it.ID] = it.Applied
	}
	if got[1] {
		t.Error("数据源 1 没有待审批申请，applied 应为 false")
	}
	if !got[2] {
		t.Error("数据源 2 有待审批申请，applied 应为 true")
	}
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
