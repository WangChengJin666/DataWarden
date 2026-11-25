package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/opensourceprj/datawarden/internal/model"
	"github.com/opensourceprj/datawarden/internal/store"
)

// callAsSuperAdmin 以超管身份发一个请求。body 为 nil 时不带请求体。
func callAsSuperAdmin(t *testing.T, r *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	tok := loginAndGetToken(t, r, "alice", "correct")
	buf := bytes.NewBuffer(nil)
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		buf = bytes.NewBuffer(raw)
	}
	req := httptest.NewRequest(method, path, buf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestGroupList(t *testing.T) {
	gs := &fakeGroupStore{items: []model.UserGroup{{ID: 1, Name: "研发", MemberCount: 3}}}
	r := buildAccountRouter(t, "superadmin", nil, gs)

	w := callAsSuperAdmin(t, r, http.MethodGet, "/api/v1/groups", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Items []model.UserGroup `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Items) != 1 || resp.Items[0].Name != "研发" || resp.Items[0].MemberCount != 3 {
		t.Fatalf("items = %+v", resp.Items)
	}
}

func TestGroupCreateOK(t *testing.T) {
	r := buildAccountRouter(t, "superadmin", nil, &fakeGroupStore{})
	w := callAsSuperAdmin(t, r, http.MethodPost, "/api/v1/groups", map[string]string{
		"name": "数据平台", "description": "搞数仓的",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.ID != 1 {
		t.Errorf("id = %d, want 1", resp.ID)
	}
}

func TestGroupCreateRejectsBlankName(t *testing.T) {
	r := buildAccountRouter(t, "superadmin", nil, &fakeGroupStore{})
	w := callAsSuperAdmin(t, r, http.MethodPost, "/api/v1/groups", map[string]string{"name": "   "})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", w.Code, w.Body.String())
	}
}

func TestGroupCreateRejectsLongName(t *testing.T) {
	r := buildAccountRouter(t, "superadmin", nil, &fakeGroupStore{})
	w := callAsSuperAdmin(t, r, http.MethodPost, "/api/v1/groups", map[string]string{
		"name": strings.Repeat("a", 65),
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// 组名去掉首尾空白后入库：否则「研发」与「研发 」会变成两个看起来一样的组。
func TestGroupCreateTrimsName(t *testing.T) {
	gs := &fakeGroupStore{}
	r := buildAccountRouter(t, "superadmin", nil, gs)
	w := callAsSuperAdmin(t, r, http.MethodPost, "/api/v1/groups", map[string]string{"name": " 研发 "})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if gs.items[0].Name != "研发" {
		t.Errorf("name = %q, want 研发", gs.items[0].Name)
	}
}

func TestGroupCreateDuplicateReturns409(t *testing.T) {
	r := buildAccountRouter(t, "superadmin", nil, &fakeGroupStore{failWith: store.ErrGroupNameTaken})
	w := callAsSuperAdmin(t, r, http.MethodPost, "/api/v1/groups", map[string]string{"name": "研发"})
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body=%s", w.Code, w.Body.String())
	}
}

func TestGroupUpdateMissingReturns404(t *testing.T) {
	r := buildAccountRouter(t, "superadmin", nil, &fakeGroupStore{})
	w := callAsSuperAdmin(t, r, http.MethodPut, "/api/v1/groups/9", map[string]string{"name": "x"})
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", w.Code, w.Body.String())
	}
}

func TestGroupDeleteMissingReturns404(t *testing.T) {
	r := buildAccountRouter(t, "superadmin", nil, &fakeGroupStore{})
	w := callAsSuperAdmin(t, r, http.MethodDelete, "/api/v1/groups/9", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// 非法 id 要 400 而不是 500：路径参数写错是调用方问题。
func TestGroupRoutes_RejectBadID(t *testing.T) {
	r := buildAccountRouter(t, "superadmin", nil, &fakeGroupStore{})
	for _, path := range []string{"/api/v1/groups/abc", "/api/v1/groups/0", "/api/v1/groups/-1"} {
		w := callAsSuperAdmin(t, r, http.MethodDelete, path, nil)
		if w.Code != http.StatusBadRequest {
			t.Errorf("DELETE %s: status = %d, want 400", path, w.Code)
		}
	}
}
