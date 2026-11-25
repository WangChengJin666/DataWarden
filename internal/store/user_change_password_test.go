package store

import (
	"context"
	"errors"
	"testing"

	"github.com/opensourceprj/datawarden/internal/auth"
	"github.com/opensourceprj/datawarden/internal/model"
)

// findHash 直接读库里的密码哈希，用于断言「确实写进去了什么」。
//
// 不走 FindByUsername：它只返回 active 用户，而删除用例需要的正是
// 已删账号的行为，用它会把「查不到」和「不该改」混成同一个现象。
func findHash(t *testing.T, s *UserStore, id int64) string {
	t.Helper()
	var h string
	if err := s.pool.QueryRow(context.Background(),
		`SELECT password_hash FROM app_user WHERE id = $1`, id).Scan(&h); err != nil {
		t.Fatalf("read hash: %v", err)
	}
	return h
}

func TestUserStore_ChangePassword(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	s := NewUserStore(pool)
	id := newUser(t, s, "alice", model.RoleDeveloper)

	oldHash := findHash(t, s, id)
	newHash, err := auth.HashPassword("brand-new-pw")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}

	// 旧密码正确：应写入新哈希。
	err = s.ChangePassword(ctx, id, func(cur string) bool {
		return auth.VerifyPassword(cur, "P@ssw0rd!")
	}, newHash)
	if err != nil {
		t.Fatalf("change password: %v", err)
	}
	if got := findHash(t, s, id); got != newHash {
		t.Fatal("密码哈希未被更新")
	}
	if oldHash == newHash {
		t.Fatal("测试用例无效：新旧哈希相同")
	}

	// 旧密码错误：返回哨兵错误，且库里的哈希保持不变。
	// 这条是关键——校验与写入在同一个事务里，失败必须整体回滚。
	another, err := auth.HashPassword("should-not-be-written")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	err = s.ChangePassword(ctx, id, func(cur string) bool {
		return auth.VerifyPassword(cur, "wrong-old-pw")
	}, another)
	if !errors.Is(err, ErrOldPasswordMismatch) {
		t.Fatalf("err = %v, want ErrOldPasswordMismatch", err)
	}
	if got := findHash(t, s, id); got != newHash {
		t.Fatal("校验失败后哈希被改动了")
	}
}

// 已删除账号不可改密码：删除是不可恢复的，能改密码就等于能把它用起来。
func TestUserStore_ChangePassword_RejectDeleted(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	s := NewUserStore(pool)
	id := newUser(t, s, "alice", model.RoleDeveloper)
	if err := s.SoftDelete(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}

	newHash, err := auth.HashPassword("brand-new-pw")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	err = s.ChangePassword(ctx, id, func(string) bool { return true }, newHash)
	if !errors.Is(err, ErrUserDeleted) {
		t.Fatalf("err = %v, want ErrUserDeleted", err)
	}
}

// 不存在的用户返回 ErrUserNotFound，而不是一个「校验失败」的假象。
func TestUserStore_ChangePassword_NotFound(t *testing.T) {
	pool := setupPool(t)
	s := NewUserStore(pool)
	newHash, err := auth.HashPassword("brand-new-pw")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	err = s.ChangePassword(context.Background(), 987654, func(string) bool { return true }, newHash)
	if !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("err = %v, want ErrUserNotFound", err)
	}
}
