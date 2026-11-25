package store

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opensourceprj/datawarden/internal/auth"
	"github.com/opensourceprj/datawarden/internal/model"
)

func TestUserStore_SetStatus(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	s := NewUserStore(pool)

	uid := newUser(t, s, "toggler", model.RoleDeveloper)
	if err := s.SetStatus(ctx, uid, model.StatusDisabled); err != nil {
		t.Fatalf("disable: %v", err)
	}
	got, _ := s.Get(ctx, uid)
	if got.Status != model.StatusDisabled {
		t.Fatalf("status = %q", got.Status)
	}

	// 停用后不能再登录：FindByUsername 只认 active。
	if _, _, _, err := s.FindByUsername(ctx, "toggler"); err == nil {
		t.Fatal("disabled user must not be found for login")
	}

	if err := s.SetStatus(ctx, uid, model.StatusActive); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if _, _, _, err := s.FindByUsername(ctx, "toggler"); err != nil {
		t.Fatalf("re-enabled user should log in: %v", err)
	}
}

func TestUserStore_SetStatusRejectsDeleted(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	s := NewUserStore(pool)

	uid := newUser(t, s, "goner", model.RoleDeveloper)
	if err := s.SoftDelete(ctx, uid); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	// 删除后不允许改回可用：否则「删除」只是一个名字吓人的停用。
	if err := s.SetStatus(ctx, uid, model.StatusActive); !errors.Is(err, ErrUserDeleted) {
		t.Fatalf("err = %v, want ErrUserDeleted", err)
	}
	// 也不接受直接设成 deleted（只能走 SoftDelete，它还会改写用户名）。
	if err := s.SetStatus(ctx, uid, model.StatusDeleted); err == nil {
		t.Fatal("deleted must not be assignable via SetStatus")
	}
}

func TestUserStore_SetStatusMissingUser(t *testing.T) {
	pool := setupPool(t)
	s := NewUserStore(pool)
	if err := s.SetStatus(context.Background(), 999999, model.StatusDisabled); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("err = %v, want ErrUserNotFound", err)
	}
}

func TestUserStore_ResetPassword(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	s := NewUserStore(pool)

	uid := newUser(t, s, "resetter", model.RoleDeveloper)
	hash, err := auth.HashPassword("NewP@ss1")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if err := s.ResetPassword(ctx, uid, hash); err != nil {
		t.Fatalf("reset: %v", err)
	}
	_, gotHash, _, err := s.FindByUsername(ctx, "resetter")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if !auth.VerifyPassword(gotHash, "NewP@ss1") {
		t.Fatal("new password should verify")
	}
	if auth.VerifyPassword(gotHash, "P@ssw0rd!") {
		t.Fatal("old password must stop working")
	}
}

// 删除的核心断言：状态变了、用户名被改写腾出来、历史记录一条不少。
func TestUserStore_SoftDelete_RenamesAndKeepsHistory(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	us := NewUserStore(pool)
	ds := NewDatasourceStore(pool)
	gr := NewGrantStore(pool)
	rr := NewGrantRequestStore(pool)

	uid := newUser(t, us, "leaver", model.RoleDeveloper)

	dsID, err := ds.Create(ctx, &model.Datasource{
		Name: "pg-del-test", Type: model.TypePostgreSQL, Host: "127.0.0.1", Port: 5432,
		Database: "shop", Username: "ro", Password: "pw",
	})
	if err != nil {
		t.Fatalf("create datasource: %v", err)
	}
	if _, err := gr.Create(ctx, &model.Grant{
		UserID: uid, DatasourceID: dsID, Scope: "*", Action: "read",
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("create grant: %v", err)
	}
	if _, err := rr.Create(ctx, &model.GrantRequest{
		UserID: uid, DatasourceID: dsID, Scope: "*", Days: 3, Reason: "排查线上问题",
	}); err != nil {
		t.Fatalf("create request: %v", err)
	}

	if err := us.SoftDelete(ctx, uid); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	// 状态与用户名
	var status, username string
	if err := pool.QueryRow(ctx, `SELECT status, username FROM app_user WHERE id = $1`, uid).
		Scan(&status, &username); err != nil {
		t.Fatalf("query user: %v", err)
	}
	if status != string(model.StatusDeleted) {
		t.Errorf("status = %q, want deleted", status)
	}
	wantName := "leaver#deleted#" + strconv.FormatInt(uid, 10)
	if username != wantName {
		t.Errorf("username = %q, want %q", username, wantName)
	}

	// 历史记录必须一条不少
	var grants, requests int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM grant_record WHERE user_id = $1`, uid).Scan(&grants); err != nil {
		t.Fatalf("count grants: %v", err)
	}
	if grants != 1 {
		t.Errorf("grants = %d, want 1 (must not be deleted)", grants)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM grant_request WHERE user_id = $1`, uid).Scan(&requests); err != nil {
		t.Fatalf("count requests: %v", err)
	}
	if requests != 1 {
		t.Errorf("requests = %d, want 1 (must not be deleted)", requests)
	}

	// 授权必须从生效列表里消失，否则已删账号仍在被统计为「有权限」
	active, err := gr.ActiveGrants(ctx)
	if err != nil {
		t.Fatalf("active grants: %v", err)
	}
	for _, g := range active {
		if g.UserID == uid {
			t.Error("deleted user's grant must not be active")
		}
	}

	// 授权管理页同样不能把已删账号显示成仍有权限
	list, err := gr.ListActive(ctx, 100)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	for _, g := range list {
		if g.UserID == uid {
			t.Error("deleted user must not appear in the active grant list")
		}
	}
}

// 这是「用户名释放」真正生效的证明：删除后立刻用原名建新账号要成功，
// 而且两条记录互不干扰（不同 id，旧记录仍指向旧 id）。
func TestUserStore_SoftDelete_FreesUsername(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	us := NewUserStore(pool)

	oldID := newUser(t, us, "zhangsan", model.RoleDeveloper)
	if err := us.SoftDelete(ctx, oldID); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	hash, _ := auth.HashPassword("Brand@New1")
	newID, err := us.Create(ctx, &model.User{
		Username: "zhangsan", PasswordHash: hash, Role: model.RoleDeveloper,
	})
	if err != nil {
		t.Fatalf("reuse username must succeed, got: %v", err)
	}
	if newID == oldID {
		t.Fatal("new user must have a different id")
	}

	// 登录找到的是新账号
	gotID, _, _, err := us.FindByUsername(ctx, "zhangsan")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if gotID != newID {
		t.Errorf("login resolves to id %d, want new id %d", gotID, newID)
	}

	// 旧账号仍在库里，可被「已删除」筛选查到。
	// 这里只断言「目标 id 在其中」，不断言总数：测试库会被反复跑，
	// 其它用例删掉的账号也落在这个筛选里，断言总数会让测试之间互相污染。
	deleted, err := us.List(ctx, UserFilter{Statuses: []string{string(model.StatusDeleted)}})
	if err != nil {
		t.Fatalf("list deleted: %v", err)
	}
	var found *model.UserView
	for i := range deleted {
		if deleted[i].ID == oldID {
			found = &deleted[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("old account must still exist as deleted, got %+v", deleted)
	}
	if !strings.HasSuffix(found.Username, "#deleted#"+strconv.FormatInt(oldID, 10)) {
		t.Errorf("deleted username = %q", found.Username)
	}

	// 反向确认：新账号没有被误标成删除
	live, err := us.List(ctx, UserFilter{Keyword: "zhangsan"})
	if err != nil {
		t.Fatalf("list live: %v", err)
	}
	if len(live) != 1 || live[0].ID != newID {
		t.Fatalf("live list = %+v", live)
	}
}

func TestUserStore_SoftDelete_IsIdempotent(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	s := NewUserStore(pool)

	uid := newUser(t, s, "twice", model.RoleDeveloper)
	if err := s.SoftDelete(ctx, uid); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := s.SoftDelete(ctx, uid); err != nil {
		t.Fatalf("second should be a no-op success, got: %v", err)
	}

	// 幂等重试不能把用户名再次改写（否则会变成 a#deleted#7#deleted#7）
	var username string
	if err := pool.QueryRow(ctx, `SELECT username FROM app_user WHERE id = $1`, uid).Scan(&username); err != nil {
		t.Fatalf("query: %v", err)
	}
	if strings.Count(username, "#deleted#") != 1 {
		t.Errorf("username rewritten twice: %q", username)
	}
}

func TestUserStore_SoftDelete_MissingReturnsNotFound(t *testing.T) {
	pool := setupPool(t)
	s := NewUserStore(pool)
	if err := s.SoftDelete(context.Background(), 999999); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("err = %v, want ErrUserNotFound", err)
	}
}

func TestUserStore_LastSuperAdminProtected(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	s := NewUserStore(pool)

	only := newUser(t, s, "root", model.RoleSuperAdmin)

	if err := s.SoftDelete(ctx, only); !errors.Is(err, ErrLastSuperAdmin) {
		t.Errorf("delete last superadmin: err = %v, want ErrLastSuperAdmin", err)
	}
	if err := s.SetStatus(ctx, only, model.StatusDisabled); !errors.Is(err, ErrLastSuperAdmin) {
		t.Errorf("disable last superadmin: err = %v, want ErrLastSuperAdmin", err)
	}
	if err := s.UpdateProfile(ctx, only, "", model.RoleDeveloper, nil); !errors.Is(err, ErrLastSuperAdmin) {
		t.Errorf("demote last superadmin: err = %v, want ErrLastSuperAdmin", err)
	}

	// 上面三次失败不能留下任何副作用
	got, err := s.Get(ctx, only)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Role != model.RoleSuperAdmin || got.Status != model.StatusActive {
		t.Fatalf("failed operations must not mutate the user: %+v", got)
	}

	// 再来一个超管后，删除就放行了。
	newUser(t, s, "root2", model.RoleSuperAdmin)
	if err := s.SoftDelete(ctx, only); err != nil {
		t.Errorf("should be allowed with another superadmin: %v", err)
	}
}

// 降级普通 dba 不该被「最后一名超管」检查误拦。
func TestUserStore_DemoteNonSuperAdminAllowed(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	s := NewUserStore(pool)

	newUser(t, s, "theroot", model.RoleSuperAdmin)
	dbaID := newUser(t, s, "somedba", model.RoleDBA)

	if err := s.UpdateProfile(ctx, dbaID, "", model.RoleDeveloper, nil); err != nil {
		t.Fatalf("demoting a dba must be allowed: %v", err)
	}
}

// 停用普通用户不该被误拦。
func TestUserStore_DisableNonSuperAdminAllowed(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	s := NewUserStore(pool)

	newUser(t, s, "theroot2", model.RoleSuperAdmin)
	devID := newUser(t, s, "somedev", model.RoleDeveloper)

	if err := s.SetStatus(ctx, devID, model.StatusDisabled); err != nil {
		t.Fatalf("disabling a developer must be allowed: %v", err)
	}
}

// 并发：两个超管同时想删掉对方（各自看到的都还有对方），
// 最终必须留下至少一个。
//
// 这条测试曾经被「只锁目标行」的实现骗过：单次跑往往能过，因为两个
// goroutine 恰好一前一后完成。实测 30 轮里有 29 轮双双成功、可用超管归零。
// 所以这里必须多轮重复，并在每轮内真正并发（sync.WaitGroup 同时起跑），
// 同时断言「双删都成功」的次数为 0——只断言「至少剩一个」会漏掉
// 「先删 A 成功、随后 B 也被删但状态已 deleted」之类的中途态。
func TestUserStore_LastSuperAdmin_Concurrent(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	s := NewUserStore(pool)

	const rounds = 25
	hash, _ := auth.HashPassword("x")

	for i := 0; i < rounds; i++ {
		// 每轮从干净状态开始，保证「恰好两个 active 超管」这个前提成立
		for _, stmt := range []string{
			"DELETE FROM grant_request", "DELETE FROM grant_record",
			"DELETE FROM datasource", "DELETE FROM app_user",
		} {
			if _, err := pool.Exec(ctx, stmt); err != nil {
				t.Fatalf("reset: %v", err)
			}
		}
		a, err := s.Create(ctx, &model.User{Username: "rootA", PasswordHash: hash, Role: model.RoleSuperAdmin})
		if err != nil {
			t.Fatalf("create a: %v", err)
		}
		b, err := s.Create(ctx, &model.User{Username: "rootB", PasswordHash: hash, Role: model.RoleSuperAdmin})
		if err != nil {
			t.Fatalf("create b: %v", err)
		}

		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Add(2)
		go func() { defer wg.Done(); errs[0] = s.SoftDelete(ctx, a) }()
		go func() { defer wg.Done(); errs[1] = s.SoftDelete(ctx, b) }()
		wg.Wait()

		if errs[0] == nil && errs[1] == nil {
			t.Fatalf("round %d: both superadmins deleted, platform lost account management", i+1)
		}

		var alive int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM app_user WHERE role = 'superadmin' AND status = 'active'`).Scan(&alive); err != nil {
			t.Fatalf("count: %v", err)
		}
		if alive < 1 {
			t.Fatalf("round %d: must keep at least one active superadmin, got %d", i+1, alive)
		}
	}
}

// 已停用的超管也不能随便降级：一旦降成 developer，超管身份就永久没了，
// 而用户名还被这个账号占着，无法再建一个同名超管补救。
// 只有「确实还有另一个 active 超管」时才允许。
func TestUserStore_DemoteDisabledSuperAdminNeedsAnotherActive(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	s := NewUserStore(pool)

	// 场景一：只有一个超管（先停用自己那一步会被拦，所以直接构造停用状态）
	only := newUser(t, s, "rootX", model.RoleSuperAdmin)
	if _, err := pool.Exec(ctx, `UPDATE app_user SET status = 'disabled' WHERE id = $1`, only); err != nil {
		t.Fatalf("seed disabled: %v", err)
	}

	err := s.UpdateProfile(ctx, only, "", model.RoleDeveloper, nil)
	if !errors.Is(err, ErrLastSuperAdmin) {
		t.Fatalf("降级已停用的唯一超管应被拦，得到 %v", err)
	}

	// 场景二：有另一个 active 超管时允许
	newUser(t, s, "rootY", model.RoleSuperAdmin)
	if err := s.UpdateProfile(ctx, only, "", model.RoleDeveloper, nil); err != nil {
		t.Fatalf("有兜底超管时应允许降级，得到 %v", err)
	}
}
