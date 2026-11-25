package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/opensourceprj/datawarden/internal/model"
	"github.com/opensourceprj/datawarden/internal/store"
)

// fakeUserStore 是账号管理的内存实现。
//
// 记录调用过哪些方法，并允许注入错误，用来验证 handler 的
// 参数校验与状态码映射——这些是 SQL 层测不到的部分。
type fakeUserStore struct {
	items        []model.UserView
	nextID       int64
	createCalled bool
	deleteCalled bool
	lastFilter   store.UserFilter
	lastStatus   model.UserStatus
	lastHash     string
	lastRole     model.Role
	err          error
}

func (f *fakeUserStore) List(_ context.Context, filter store.UserFilter) ([]model.UserView, error) {
	f.lastFilter = filter
	if f.err != nil {
		return nil, f.err
	}
	if f.items == nil {
		return []model.UserView{}, nil
	}
	return f.items, nil
}

func (f *fakeUserStore) Get(context.Context, int64) (*model.UserView, error) {
	return &model.UserView{ID: 1, Username: "alice"}, nil
}

func (f *fakeUserStore) Create(_ context.Context, u *model.User) (int64, error) {
	f.createCalled = true
	f.lastRole = u.Role
	if f.err != nil {
		return 0, f.err
	}
	f.nextID++
	return f.nextID, nil
}

func (f *fakeUserStore) UpdateProfile(_ context.Context, _ int64, _ string, role model.Role, _ *int64) error {
	f.lastRole = role
	return f.err
}

func (f *fakeUserStore) SetStatus(_ context.Context, _ int64, status model.UserStatus) error {
	f.lastStatus = status
	return f.err
}

func (f *fakeUserStore) ResetPassword(_ context.Context, _ int64, hash string) error {
	f.lastHash = hash
	return f.err
}

func (f *fakeUserStore) SoftDelete(context.Context, int64) error {
	f.deleteCalled = true
	return f.err
}

type fakeGroupStore struct {
	items    []model.UserGroup
	nextID   int64
	failWith error
}

func (f *fakeGroupStore) List(context.Context) ([]model.UserGroup, error) {
	if f.items == nil {
		return []model.UserGroup{}, nil
	}
	return f.items, nil
}

func (f *fakeGroupStore) Create(_ context.Context, g *model.UserGroup) (int64, error) {
	if f.failWith != nil {
		return 0, f.failWith
	}
	f.nextID++
	f.items = append(f.items, model.UserGroup{ID: f.nextID, Name: g.Name, Description: g.Description})
	return f.nextID, nil
}

func (f *fakeGroupStore) Update(_ context.Context, id int64, name, desc string) error {
	if f.failWith != nil {
		return f.failWith
	}
	for i := range f.items {
		if f.items[i].ID == id {
			f.items[i].Name, f.items[i].Description = name, desc
			return nil
		}
	}
	return store.ErrGroupNotFound
}

func (f *fakeGroupStore) Delete(_ context.Context, id int64) error {
	if f.failWith != nil {
		return f.failWith
	}
	for i := range f.items {
		if f.items[i].ID == id {
			f.items = append(f.items[:i], f.items[i+1:]...)
			return nil
		}
	}
	return store.ErrGroupNotFound
}

// buildAccountRouter 装配一个挂了账号管理路由的路由。
//
// 不用现有 buildRouter：它在 Deps 里塞的是空 QueryHandler，
// 这里只覆盖测试需要的部分，其余保持 nil（NewRouter 对 nil 各项跳过注册）。
func buildAccountRouter(t *testing.T, role string, us UserStore, gs GroupStore) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	issuer := authIssuerForTest()
	deps := Deps{
		Issuer:      issuer,
		AuthHandler: &AuthHandler{Users: &memUsers{passwords: map[string]string{"alice": "correct"}, role: role}, Issuer: issuer},
	}
	if us != nil {
		deps.User = &UserHandler{Store: us}
	}
	if gs != nil {
		deps.Group = &GroupHandler{Store: gs}
	}
	return NewRouter(deps)
}

// 账号管理接口对非超管必须 403。这里把每一条路由都过一遍：
// 只要有一条漏挂中间件，就等于把账号体系交给了所有人。
func TestAccountRoutes_RejectNonSuperAdmin(t *testing.T) {
	routes := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/groups"},
		{http.MethodPost, "/api/v1/groups"},
		{http.MethodPut, "/api/v1/groups/1"},
		{http.MethodDelete, "/api/v1/groups/1"},
		{http.MethodGet, "/api/v1/users"},
		{http.MethodPost, "/api/v1/users"},
		{http.MethodPut, "/api/v1/users/1"},
		{http.MethodDelete, "/api/v1/users/1"},
		{http.MethodPost, "/api/v1/users/1/status"},
		{http.MethodPost, "/api/v1/users/1/reset-password"},
	}
	for _, role := range []string{"developer", "dba"} {
		r := buildAccountRouter(t, role, &fakeUserStore{}, &fakeGroupStore{})
		tok := loginAndGetToken(t, r, "alice", "correct")
		for _, rt := range routes {
			req := httptest.NewRequest(rt.method, rt.path, nil)
			req.Header.Set("Authorization", "Bearer "+tok)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusForbidden {
				t.Errorf("%s %s as %s: status = %d, want 403", rt.method, rt.path, role, w.Code)
			}
		}
	}
}

// 未登录访问账号管理接口必须 401。
func TestAccountRoutes_RequireAuth(t *testing.T) {
	r := buildAccountRouter(t, "superadmin", &fakeUserStore{}, &fakeGroupStore{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestAccountRoutes_AllowSuperAdmin(t *testing.T) {
	r := buildAccountRouter(t, "superadmin", &fakeUserStore{}, &fakeGroupStore{})
	tok := loginAndGetToken(t, r, "alice", "correct")

	for _, path := range []string{"/api/v1/users", "/api/v1/groups"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200, body=%s", path, w.Code, w.Body.String())
		}
	}
}

func TestCreateUser_RejectsInvalidRole(t *testing.T) {
	us := &fakeUserStore{}
	r := buildAccountRouter(t, "superadmin", us, nil)
	w := callAsSuperAdmin(t, r, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "newbie", "role": "*", "status": "active",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", w.Code, w.Body.String())
	}
	if us.createCalled {
		t.Fatal("must not reach the store with an invalid role")
	}
}

func TestCreateUser_RejectsInvalidUsername(t *testing.T) {
	for _, name := range []string{"", "  ", "有中文", "has space", "a#b", "x%y"} {
		us := &fakeUserStore{}
		r := buildAccountRouter(t, "superadmin", us, nil)
		w := callAsSuperAdmin(t, r, http.MethodPost, "/api/v1/users", map[string]any{
			"username": name, "role": "developer", "status": "active",
		})
		if w.Code != http.StatusBadRequest {
			t.Errorf("username %q: status = %d, want 400, body=%s", name, w.Code, w.Body.String())
		}
	}
}

func TestCreateUser_RejectsDeletedStatus(t *testing.T) {
	us := &fakeUserStore{}
	r := buildAccountRouter(t, "superadmin", us, nil)
	w := callAsSuperAdmin(t, r, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "ghost", "role": "developer", "status": "deleted",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (deleted cannot be assigned)", w.Code)
	}
}

// 新建成功必须回传一次性密码。
func TestCreateUser_ReturnsGeneratedPassword(t *testing.T) {
	us := &fakeUserStore{}
	r := buildAccountRouter(t, "superadmin", us, nil)
	w := callAsSuperAdmin(t, r, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "newbie", "email": "n@example.com", "role": "developer",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		ID       int64  `json:"id"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.ID == 0 {
		t.Error("id must be returned")
	}
	if len(resp.Password) != 12 {
		t.Errorf("password length = %d, want 12", len(resp.Password))
	}
}

// 响应里的明文不能等于库里存的哈希：否则等于把密码直接存明文。
func TestCreateUser_ResponseCarriesPlainButStoresHash(t *testing.T) {
	us := &fakeUserStore{}
	r := buildAccountRouter(t, "superadmin", us, nil)
	w := callAsSuperAdmin(t, r, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "hashcheck", "role": "developer",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Password string `json:"password"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Password == "" {
		t.Fatal("plain password must be returned once")
	}
}

func TestCreateUser_DuplicateReturns409(t *testing.T) {
	us := &fakeUserStore{err: store.ErrUsernameTaken}
	r := buildAccountRouter(t, "superadmin", us, nil)
	w := callAsSuperAdmin(t, r, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "dup", "role": "developer",
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
}

func TestCreateUser_MissingGroupReturns409(t *testing.T) {
	us := &fakeUserStore{err: store.ErrGroupMissing}
	r := buildAccountRouter(t, "superadmin", us, nil)
	w := callAsSuperAdmin(t, r, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "nogroup", "role": "developer", "group_id": 999,
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
}

// group_id 传 0 视为未分组，不能拿去查「组 0」导致「组不存在」。
func TestCreateUser_ZeroGroupMeansUngrouped(t *testing.T) {
	us := &fakeUserStore{}
	r := buildAccountRouter(t, "superadmin", us, nil)
	w := callAsSuperAdmin(t, r, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "zerogroup", "role": "developer", "group_id": 0,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
}

func TestSetStatus_RejectsDeletedValue(t *testing.T) {
	us := &fakeUserStore{}
	r := buildAccountRouter(t, "superadmin", us, nil)
	w := callAsSuperAdmin(t, r, http.MethodPost, "/api/v1/users/2/status", map[string]string{"status": "deleted"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", w.Code, w.Body.String())
	}
	if us.lastStatus != "" {
		t.Fatalf("store must not be called, got %q", us.lastStatus)
	}
}

func TestSetStatus_LastSuperAdminReturns409(t *testing.T) {
	us := &fakeUserStore{err: store.ErrLastSuperAdmin}
	r := buildAccountRouter(t, "superadmin", us, nil)
	w := callAsSuperAdmin(t, r, http.MethodPost, "/api/v1/users/2/status", map[string]string{"status": "disabled"})
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body=%s", w.Code, w.Body.String())
	}
}

// 不允许停用自己：停用后这个 token 立刻查不了数据，
// 而请求本身已经通过鉴权、会「成功」执行，操作者当场被自己锁在门外。
// memUsers 固定返回 id=1，所以 /users/1 就是「自己」。
func TestSetStatus_SelfDisableRejected(t *testing.T) {
	us := &fakeUserStore{}
	r := buildAccountRouter(t, "superadmin", us, nil)
	w := callAsSuperAdmin(t, r, http.MethodPost, "/api/v1/users/1/status", map[string]string{"status": "disabled"})
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body=%s", w.Code, w.Body.String())
	}
	if us.lastStatus != "" {
		t.Fatal("store must not be called when disabling self")
	}
}

// 启用自己没问题：不会把自己关在外面。
func TestSetStatus_SelfEnableAllowed(t *testing.T) {
	us := &fakeUserStore{}
	r := buildAccountRouter(t, "superadmin", us, nil)
	w := callAsSuperAdmin(t, r, http.MethodPost, "/api/v1/users/1/status", map[string]string{"status": "active"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
}

func TestDeleteUser_CallsSoftDelete(t *testing.T) {
	us := &fakeUserStore{}
	r := buildAccountRouter(t, "superadmin", us, nil)
	w := callAsSuperAdmin(t, r, http.MethodDelete, "/api/v1/users/2", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if !us.deleteCalled {
		t.Fatal("SoftDelete must be called")
	}
	var resp struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Status != "deleted" {
		t.Errorf("status = %q, want deleted", resp.Status)
	}
}

func TestDeleteUser_SelfDeleteRejected(t *testing.T) {
	us := &fakeUserStore{}
	r := buildAccountRouter(t, "superadmin", us, nil)
	w := callAsSuperAdmin(t, r, http.MethodDelete, "/api/v1/users/1", nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body=%s", w.Code, w.Body.String())
	}
	if us.deleteCalled {
		t.Fatal("must not soft-delete yourself")
	}
}

func TestDeleteUser_DeletedAccountReturns409(t *testing.T) {
	us := &fakeUserStore{err: store.ErrUserDeleted}
	r := buildAccountRouter(t, "superadmin", us, nil)
	w := callAsSuperAdmin(t, r, http.MethodDelete, "/api/v1/users/2", nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
}

func TestResetPassword_ReturnsPassword(t *testing.T) {
	us := &fakeUserStore{}
	r := buildAccountRouter(t, "superadmin", us, nil)
	w := callAsSuperAdmin(t, r, http.MethodPost, "/api/v1/users/2/reset-password", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Password string `json:"password"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Password) != 12 {
		t.Errorf("password = %q", resp.Password)
	}
	// 落库的是 bcrypt 哈希，绝不是明文。
	if us.lastHash == resp.Password || us.lastHash == "" {
		t.Error("store must receive a bcrypt hash, not the plaintext")
	}
}

func TestListUsers_DefaultStatusFilter(t *testing.T) {
	us := &fakeUserStore{}
	r := buildAccountRouter(t, "superadmin", us, nil)
	w := callAsSuperAdmin(t, r, http.MethodGet, "/api/v1/users", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	// handler 不填默认值，交给 store 决定：这样「默认口径」只有一处定义。
	if len(us.lastFilter.Statuses) != 0 {
		t.Errorf("handler should leave Statuses empty so the store applies its default, got %v", us.lastFilter.Statuses)
	}
}

func TestListUsers_DeletedFilterPassedThrough(t *testing.T) {
	us := &fakeUserStore{}
	r := buildAccountRouter(t, "superadmin", us, nil)
	callAsSuperAdmin(t, r, http.MethodGet, "/api/v1/users?status=deleted", nil)
	if len(us.lastFilter.Statuses) != 1 || us.lastFilter.Statuses[0] != "deleted" {
		t.Fatalf("statuses = %v, want [deleted]", us.lastFilter.Statuses)
	}
}

func TestListUsers_MultiStatusParam(t *testing.T) {
	us := &fakeUserStore{}
	r := buildAccountRouter(t, "superadmin", us, nil)
	callAsSuperAdmin(t, r, http.MethodGet, "/api/v1/users?status=active,disabled", nil)
	if len(us.lastFilter.Statuses) != 2 {
		t.Fatalf("statuses = %v, want 2 values", us.lastFilter.Statuses)
	}
}

func TestListUsers_RejectsUnknownStatusParam(t *testing.T) {
	us := &fakeUserStore{}
	r := buildAccountRouter(t, "superadmin", us, nil)
	w := callAsSuperAdmin(t, r, http.MethodGet, "/api/v1/users?status=whatever", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestListUsers_RejectsUnknownRoleParam(t *testing.T) {
	us := &fakeUserStore{}
	r := buildAccountRouter(t, "superadmin", us, nil)
	w := callAsSuperAdmin(t, r, http.MethodGet, "/api/v1/users?role=nope", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestListUsers_UngroupedFlag(t *testing.T) {
	us := &fakeUserStore{}
	r := buildAccountRouter(t, "superadmin", us, nil)
	callAsSuperAdmin(t, r, http.MethodGet, "/api/v1/users?ungrouped=1", nil)
	if !us.lastFilter.Ungrouped {
		t.Fatal("ungrouped flag must be passed to the store")
	}
}

func TestUpdateUser_RejectsInvalidRole(t *testing.T) {
	us := &fakeUserStore{}
	r := buildAccountRouter(t, "superadmin", us, nil)
	w := callAsSuperAdmin(t, r, http.MethodPut, "/api/v1/users/2", map[string]any{"role": "boss"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", w.Code, w.Body.String())
	}
}

func TestUpdateUser_DeletedReturns409(t *testing.T) {
	us := &fakeUserStore{err: store.ErrUserDeleted}
	r := buildAccountRouter(t, "superadmin", us, nil)
	w := callAsSuperAdmin(t, r, http.MethodPut, "/api/v1/users/2", map[string]any{"role": "developer"})
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
}
