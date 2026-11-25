package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/opensourceprj/datawarden/internal/model"
)

var (
	// ErrGroupNotFound 表示组不存在。
	ErrGroupNotFound = errors.New("group not found")
	// ErrGroupNameTaken 表示组名已被占用。
	ErrGroupNameTaken = errors.New("group name already taken")
)

// uniqueViolation 是 PostgreSQL 唯一约束冲突的 SQLSTATE。
const uniqueViolation = "23505"

// GroupStore 是用户组的持久化层。
//
// 组只做归类，不参与任何权限判定，所以这里没有任何和 authz 的交互。
type GroupStore struct {
	pool *pgxpool.Pool
}

func NewGroupStore(pool *pgxpool.Pool) *GroupStore {
	return &GroupStore{pool: pool}
}

// List 返回全部组，附带成员数。
//
// MemberCount 用 LEFT JOIN 一次算完，不给每组发一次 count 查询：
// 组数量会随部门增长，N+1 在本地看不出问题，上了生产就是列表页转圈。
// 统计口径排除 deleted：已删除账号不该让「组里有几个人」这个数字虚高。
func (s *GroupStore) List(ctx context.Context) ([]model.UserGroup, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT g.id, g.name, g.description, g.created_at,
		        count(u.id) FILTER (WHERE u.status <> $1) AS member_count
		   FROM user_group g
		   LEFT JOIN app_user u ON u.group_id = g.id
		  GROUP BY g.id
		  ORDER BY g.name`,
		string(model.StatusDeleted))
	if err != nil {
		return nil, fmt.Errorf("query groups: %w", err)
	}
	defer rows.Close()

	out := []model.UserGroup{}
	for rows.Next() {
		var g model.UserGroup
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.CreatedAt, &g.MemberCount); err != nil {
			return nil, fmt.Errorf("scan group: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Create 新建组。组名重复返回 ErrGroupNameTaken。
//
// 靠唯一索引判重而不是先 SELECT 再 INSERT：两步之间有人插入同样的名字时，
// 先查后插会让第二个请求以为成功，最后在 INSERT 处抛出一个原始数据库错误。
func (s *GroupStore) Create(ctx context.Context, g *model.UserGroup) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO user_group (name, description) VALUES ($1, $2) RETURNING id`,
		g.Name, g.Description,
	).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, ErrGroupNameTaken
		}
		return 0, fmt.Errorf("insert group: %w", err)
	}
	return id, nil
}

// Update 改名与改描述。组不存在返回 ErrGroupNotFound。
func (s *GroupStore) Update(ctx context.Context, id int64, name, description string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE user_group SET name = $1, description = $2 WHERE id = $3`,
		name, description, id)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrGroupNameTaken
		}
		return fmt.Errorf("update group: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrGroupNotFound
	}
	return nil
}

// Delete 删除组。组员不会被删，只会被置为未分组（靠外键 ON DELETE SET NULL）。
//
// 应用层不写 UPDATE app_user SET group_id = NULL：外键已经是这个行为，
// 再写一遍会出现两个事实来源；将来有人改了外键语义而忘了改这里，
// 就会出现「组删了、人也删了」这种最坏情况。
func (s *GroupStore) Delete(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM user_group WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete group: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrGroupNotFound
	}
	return nil
}

// isUniqueViolation 判断错误是否为唯一约束冲突。
//
// 用 SQLSTATE 而不是匹配错误文本：文本随 PG 版本与 locale 变化，
// 而 23505 是标准错误码，稳定。
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == uniqueViolation
	}
	return false
}
