package store

import (
	"context"
	"errors"
	"testing"

	"github.com/opensourceprj/datawarden/internal/auth"
	"github.com/opensourceprj/datawarden/internal/model"
)

// newUser 建一个测试用户，返回 id。密码固定，便于需要时验证登录。
func newUser(t *testing.T, s *UserStore, username string, role model.Role) int64 {
	t.Helper()
	hash, err := auth.HashPassword("P@ssw0rd!")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	id, err := s.Create(context.Background(), &model.User{
		Username: username, PasswordHash: hash, Role: role,
	})
	if err != nil {
		t.Fatalf("create %s: %v", username, err)
	}
	return id
}

func TestUserStore_ListWithFilter(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	s := NewUserStore(pool)

	newUser(t, s, "alice", model.RoleDeveloper)
	newUser(t, s, "bob", model.RoleDBA)

	all, err := s.List(ctx, UserFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("list all = %d, want 2", len(all))
	}

	filtered, err := s.List(ctx, UserFilter{Keyword: "ali"})
	if err != nil {
		t.Fatalf("list keyword: %v", err)
	}
	if len(filtered) != 1 || filtered[0].Username != "alice" {
		t.Fatalf("keyword filter = %+v", filtered)
	}

	byRole, err := s.List(ctx, UserFilter{Role: string(model.RoleDBA)})
	if err != nil {
		t.Fatalf("list role: %v", err)
	}
	if len(byRole) != 1 || byRole[0].Username != "bob" {
		t.Fatalf("role filter = %+v", byRole)
	}
}

// 搜索里的 % 与 _ 必须按字面匹配，否则用户搜 "%" 会命中全部记录，
// 看起来像「搜索没生效」。
func TestUserStore_ListEscapesLikeWildcards(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	s := NewUserStore(pool)

	newUser(t, s, "plainname", model.RoleDeveloper)

	got, err := s.List(ctx, UserFilter{Keyword: "%"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("searching %% must match nothing, got %+v", got)
	}

	got, err = s.List(ctx, UserFilter{Keyword: "_"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("searching _ must match nothing, got %+v", got)
	}
}

// 默认列表不能包含已删除账号：他们要出现在「已删除」筛选里。
func TestUserStore_ListExcludesDeletedByDefault(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	s := NewUserStore(pool)

	id := newUser(t, s, "gone", model.RoleDeveloper)
	if err := s.SoftDelete(ctx, id); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	all, err := s.List(ctx, UserFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("deleted user must be excluded, got %+v", all)
	}

	deleted, err := s.List(ctx, UserFilter{Statuses: []string{string(model.StatusDeleted)}})
	if err != nil {
		t.Fatalf("list deleted: %v", err)
	}
	if len(deleted) != 1 {
		t.Fatalf("deleted filter = %+v", deleted)
	}
}

func TestUserStore_ListUngrouped(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	us := NewUserStore(pool)
	gs := NewGroupStore(pool)
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM user_group") })

	newUser(t, us, "nogroupuser", model.RoleDeveloper)

	gid, err := gs.Create(ctx, &model.UserGroup{Name: "有组的"})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	hash, _ := auth.HashPassword("x")
	if _, err := us.Create(ctx, &model.User{
		Username: "groupeduser", PasswordHash: hash, Role: model.RoleDeveloper, GroupID: &gid,
	}); err != nil {
		t.Fatalf("create grouped user: %v", err)
	}

	got, err := us.List(ctx, UserFilter{Ungrouped: true})
	if err != nil {
		t.Fatalf("list ungrouped: %v", err)
	}
	if len(got) != 1 || got[0].Username != "nogroupuser" {
		t.Fatalf("ungrouped filter = %+v", got)
	}

	byGroup, err := us.List(ctx, UserFilter{GroupID: &gid})
	if err != nil {
		t.Fatalf("list by group: %v", err)
	}
	if len(byGroup) != 1 || byGroup[0].Username != "groupeduser" {
		t.Fatalf("group filter = %+v", byGroup)
	}
}

func TestUserStore_GetIncludesGroupName(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	us := NewUserStore(pool)
	gs := NewGroupStore(pool)
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM user_group") })

	gid, err := gs.Create(ctx, &model.UserGroup{Name: "研发"})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	hash, _ := auth.HashPassword("x")
	uid, err := us.Create(ctx, &model.User{
		Username: "gina", PasswordHash: hash, Role: model.RoleDeveloper, GroupID: &gid,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	got, err := us.Get(ctx, uid)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.GroupName != "研发" {
		t.Errorf("group_name = %q, want 研发", got.GroupName)
	}
	if got.GroupID == nil || *got.GroupID != gid {
		t.Errorf("group_id = %v, want %d", got.GroupID, gid)
	}

	if _, err := us.Get(ctx, 999999); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("err = %v, want ErrUserNotFound", err)
	}
}

func TestUserStore_CreateRejectsDuplicateUsername(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	s := NewUserStore(pool)

	newUser(t, s, "dup", model.RoleDeveloper)
	hash, _ := auth.HashPassword("x")
	_, err := s.Create(ctx, &model.User{Username: "dup", PasswordHash: hash, Role: model.RoleDeveloper})
	if !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("err = %v, want ErrUsernameTaken", err)
	}
}

func TestUserStore_CreateRejectsMissingGroup(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	s := NewUserStore(pool)

	missing := int64(999999)
	hash, _ := auth.HashPassword("x")
	_, err := s.Create(ctx, &model.User{
		Username: "nogroup", PasswordHash: hash, Role: model.RoleDeveloper, GroupID: &missing,
	})
	if !errors.Is(err, ErrGroupMissing) {
		t.Fatalf("err = %v, want ErrGroupMissing", err)
	}
}

func TestUserStore_UpdateProfile(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	us := NewUserStore(pool)
	gs := NewGroupStore(pool)
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM user_group") })

	uid := newUser(t, us, "upd", model.RoleDeveloper)
	gid, _ := gs.Create(ctx, &model.UserGroup{Name: "运维"})

	if err := us.UpdateProfile(ctx, uid, "upd@example.com", model.RoleDBA, &gid); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := us.Get(ctx, uid)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Email != "upd@example.com" || got.Role != model.RoleDBA {
		t.Fatalf("not updated: %+v", got)
	}
	if got.GroupID == nil || *got.GroupID != gid {
		t.Fatalf("group not updated: %+v", got.GroupID)
	}

	// 传 nil 表示移到「未分组」。
	if err := us.UpdateProfile(ctx, uid, "", model.RoleDBA, nil); err != nil {
		t.Fatalf("update to ungrouped: %v", err)
	}
	got, _ = us.Get(ctx, uid)
	if got.GroupID != nil {
		t.Errorf("group_id = %v, want nil", *got.GroupID)
	}
}

func TestUserStore_UpdateProfileMissingGroup(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	us := NewUserStore(pool)

	uid := newUser(t, us, "upd2", model.RoleDeveloper)
	missing := int64(999999)
	if err := us.UpdateProfile(ctx, uid, "", model.RoleDeveloper, &missing); !errors.Is(err, ErrGroupMissing) {
		t.Fatalf("err = %v, want ErrGroupMissing", err)
	}
}

func TestUserStore_UpdateProfileMissingUser(t *testing.T) {
	pool := setupPool(t)
	us := NewUserStore(pool)
	err := us.UpdateProfile(context.Background(), 999999, "", model.RoleDeveloper, nil)
	if !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("err = %v, want ErrUserNotFound", err)
	}
}

// 组不存在时不能先写再把用户的组清空：UpdateProfile 是「先校验、后写入」，
// 校验失败必须整体回滚。
func TestUserStore_UpdateProfileRollsBackOnMissingGroup(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	us := NewUserStore(pool)
	gs := NewGroupStore(pool)
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM user_group") })

	gid, _ := gs.Create(ctx, &model.UserGroup{Name: "原组"})
	hash, _ := auth.HashPassword("x")
	uid, err := us.Create(ctx, &model.User{Username: "rb", PasswordHash: hash, Role: model.RoleDeveloper, GroupID: &gid})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	missing := int64(999999)
	_ = us.UpdateProfile(ctx, uid, "x@y.z", model.RoleDBA, &missing)

	got, _ := us.Get(ctx, uid)
	if got.Email != "" || got.Role != model.RoleDeveloper {
		t.Fatalf("failed update must not change anything, got %+v", got)
	}
	if got.GroupID == nil || *got.GroupID != gid {
		t.Fatalf("group must stay unchanged, got %v", got.GroupID)
	}
}
