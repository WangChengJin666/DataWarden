package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/opensourceprj/datawarden/internal/model"
)

type UserStore struct {
	pool *pgxpool.Pool
}

func NewUserStore(pool *pgxpool.Pool) *UserStore {
	return &UserStore{pool: pool}
}

// Create 插入用户并返回自增 ID。
//
// 组存在性先查一次：外键错误虽然也能兜住，但报出来的是
// 「violates foreign key constraint」这种面向 DBA 的文本，
// 直接映射成「组不存在」对调用方更清楚。
func (s *UserStore) Create(ctx context.Context, u *model.User) (int64, error) {
	if u.GroupID != nil {
		exists, err := s.groupExists(ctx, *u.GroupID)
		if err != nil {
			return 0, err
		}
		if !exists {
			return 0, ErrGroupMissing
		}
	}

	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO app_user (username, email, password_hash, role, status, group_id)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		u.Username, u.Email, u.PasswordHash, string(u.Role), orActive(string(u.Status)), u.GroupID,
	).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, ErrUsernameTaken
		}
		return 0, fmt.Errorf("insert user: %w", err)
	}
	return id, nil
}

// groupExists 判断组是否存在。
func (s *UserStore) groupExists(ctx context.Context, id int64) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM user_group WHERE id = $1)`, id).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check group: %w", err)
	}
	return exists, nil
}

// FindByUsername 按用户名取 ID、密码哈希与角色，供登录使用。
// 只返回 status='active' 的用户，禁用账号无法登录。
func (s *UserStore) FindByUsername(ctx context.Context, username string) (int64, string, string, error) {
	var (
		id   int64
		hash string
		role string
	)
	err := s.pool.QueryRow(ctx,
		`SELECT id, password_hash, role FROM app_user
		 WHERE username = $1 AND status = 'active'`,
		username,
	).Scan(&id, &hash, &role)
	if err != nil {
		if err == pgx.ErrNoRows {
			return 0, "", "", fmt.Errorf("user %q not found", username)
		}
		return 0, "", "", fmt.Errorf("query user: %w", err)
	}
	return id, hash, role, nil
}

// AllRoles 返回 userID→role 映射，用于重建 Casbin 角色绑定。
func (s *UserStore) AllRoles(ctx context.Context) (map[int64]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, role FROM app_user WHERE status = 'active'`)
	if err != nil {
		return nil, fmt.Errorf("query roles: %w", err)
	}
	defer rows.Close()

	out := make(map[int64]string)
	for rows.Next() {
		var (
			id   int64
			role string
		)
		if err := rows.Scan(&id, &role); err != nil {
			return nil, fmt.Errorf("scan role: %w", err)
		}
		out[id] = role
	}
	return out, rows.Err()
}

func orActive(status string) string {
	if status == "" {
		return string(model.StatusActive)
	}
	return status
}
