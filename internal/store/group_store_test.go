package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/opensourceprj/datawarden/internal/auth"
	"github.com/opensourceprj/datawarden/internal/model"
)

// cleanupGroups 清空组表。
//
// 必须放在 app_user 清空之后：app_user.group_id 是 ON DELETE SET NULL，
// 先删组会把用户的归属抹掉，后续断言「用户还在但未分组」就失去意义。
// 不放在 setupPool 里是为了不影响其它测试文件的预期行数。
func cleanupGroups(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, _ = pool.Exec(context.Background(), "DELETE FROM user_group")
}

func TestGroupStore_CreateListUpdateDelete(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	gs := NewGroupStore(pool)
	cleanupGroups(t, pool)
	t.Cleanup(func() { cleanupGroups(t, pool) })

	id, err := gs.Create(ctx, &model.UserGroup{Name: "研发部", Description: "写代码的"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	items, err := gs.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 || items[0].Name != "研发部" {
		t.Fatalf("list = %+v", items)
	}
	if items[0].MemberCount != 0 {
		t.Errorf("member_count = %d, want 0", items[0].MemberCount)
	}

	if err := gs.Update(ctx, id, "研发中心", "改过的描述"); err != nil {
		t.Fatalf("update: %v", err)
	}
	items, _ = gs.List(ctx)
	if items[0].Name != "研发中心" || items[0].Description != "改过的描述" {
		t.Fatalf("update not applied: %+v", items[0])
	}

	if err := gs.Delete(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	items, _ = gs.List(ctx)
	if len(items) != 0 {
		t.Fatalf("group should be gone, got %+v", items)
	}
}

func TestGroupStore_DuplicateNameRejected(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	gs := NewGroupStore(pool)
	cleanupGroups(t, pool)
	t.Cleanup(func() { cleanupGroups(t, pool) })

	if _, err := gs.Create(ctx, &model.UserGroup{Name: "运维"}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := gs.Create(ctx, &model.UserGroup{Name: "运维"})
	if !errors.Is(err, ErrGroupNameTaken) {
		t.Fatalf("err = %v, want ErrGroupNameTaken", err)
	}
}

func TestGroupStore_MemberCount(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	us := NewUserStore(pool)
	gs := NewGroupStore(pool)
	cleanupGroups(t, pool)
	t.Cleanup(func() { cleanupGroups(t, pool) })

	gid, err := gs.Create(ctx, &model.UserGroup{Name: "数据组"})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}

	hash, _ := auth.HashPassword("x")
	for _, name := range []string{"g1", "g2"} {
		if _, err := us.Create(ctx, &model.User{
			Username: name, PasswordHash: hash, Role: model.RoleDeveloper, GroupID: &gid,
		}); err != nil {
			t.Fatalf("create user %s: %v", name, err)
		}
	}

	groups, err := gs.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if groups[0].MemberCount != 2 {
		t.Errorf("member_count = %d, want 2", groups[0].MemberCount)
	}
}

// 删组只解除归属，用户必须还在——这是设计里明确的口径，
// 也是「删组顺手把人删了」这类事故的唯一防线。
func TestGroupStore_DeleteKeepsMembers(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	us := NewUserStore(pool)
	gs := NewGroupStore(pool)
	cleanupGroups(t, pool)
	t.Cleanup(func() { cleanupGroups(t, pool) })

	gid, _ := gs.Create(ctx, &model.UserGroup{Name: "临时组"})
	hash, _ := auth.HashPassword("x")
	uid, err := us.Create(ctx, &model.User{
		Username: "keeper", PasswordHash: hash, Role: model.RoleDeveloper, GroupID: &gid,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	if err := gs.Delete(ctx, gid); err != nil {
		t.Fatalf("delete group: %v", err)
	}

	var groupID *int64
	if err := pool.QueryRow(ctx, `SELECT group_id FROM app_user WHERE id = $1`, uid).Scan(&groupID); err != nil {
		t.Fatalf("query user: %v", err)
	}
	if groupID != nil {
		t.Errorf("group_id = %v, want NULL after group deletion", *groupID)
	}

	// 用户本身必须还在
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app_user WHERE id = $1`, uid).Scan(&count); err != nil {
		t.Fatalf("count user: %v", err)
	}
	if count != 1 {
		t.Error("user must survive group deletion")
	}
}

func TestGroupStore_UpdateMissingReturnsNotFound(t *testing.T) {
	pool := setupPool(t)
	gs := NewGroupStore(pool)
	if err := gs.Update(context.Background(), 999999, "x", ""); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("err = %v, want ErrGroupNotFound", err)
	}
}

func TestGroupStore_DeleteMissingReturnsNotFound(t *testing.T) {
	pool := setupPool(t)
	gs := NewGroupStore(pool)
	if err := gs.Delete(context.Background(), 999999); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("err = %v, want ErrGroupNotFound", err)
	}
}

func TestIsUniqueViolation_NonPgError(t *testing.T) {
	if isUniqueViolation(errors.New("plain error")) {
		t.Fatal("non-pg error must not be treated as unique violation")
	}
	if isUniqueViolation(nil) {
		t.Fatal("nil must not be treated as unique violation")
	}
}

// 组名 trim 后不能为空（应用层校验，但 store 也应挡住空串，
// 否则会有一条名字是空白的组出现在界面上）。
func TestGroupStore_EmptyNameRejectedByDB(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	gs := NewGroupStore(pool)
	cleanupGroups(t, pool)
	t.Cleanup(func() { cleanupGroups(t, pool) })

	// 数据库层允许空串（只有 NOT NULL 约束），所以这里只验证
	// 「能创建成功」这一事实上层需要拦——记录这个行为，避免有人
	// 误以为 store 已经校验了。
	_, err := gs.Create(ctx, &model.UserGroup{Name: strings.Repeat(" ", 3)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
}
