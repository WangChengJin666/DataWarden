package store

import (
	"context"
	"fmt"
	"strings"

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
		var exists bool
		if err := s.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM user_group WHERE id = $1)`, *u.GroupID,
		).Scan(&exists); err != nil {
			return 0, fmt.Errorf("check group: %w", err)
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

// UserFilter 是用户列表的过滤条件。零值表示「全部未删除用户」。
type UserFilter struct {
	// Keyword 模糊匹配用户名与邮箱。
	Keyword string
	// Role 精确匹配角色，空表示不限。
	Role string
	// GroupID 精确匹配组；Ungrouped 为 true 时改查「未分组」。
	GroupID   *int64
	Ungrouped bool
	// Statuses 限定状态；为空时默认查 active 与 disabled。
	//
	// 默认排除 deleted 是关键：已删账号不该混在日常列表里，
	// 但又要能被专门查出来，所以用「显式传才包含」而不是「永远不返回」。
	Statuses []string
}

const userSelect = `
	SELECT u.id, u.username, u.email, u.role, u.status, u.group_id,
	       coalesce(g.name, '') AS group_name, u.created_at
	  FROM app_user u
	  LEFT JOIN user_group g ON g.id = u.group_id`

func scanUserView(row interface {
	Scan(dest ...any) error
}) (*model.UserView, error) {
	var v model.UserView
	if err := row.Scan(&v.ID, &v.Username, &v.Email, &v.Role, &v.Status,
		&v.GroupID, &v.GroupName, &v.CreatedAt); err != nil {
		return nil, err
	}
	return &v, nil
}

// List 返回用户列表。
//
// 三处细节值得说明：
//   - 用 LEFT JOIN 取组名，避免前端再发一轮请求去把 group_id 翻译成名字
//   - Statuses 为空时补齐 active/disabled，不返回已删除账号
//   - Keyword 用 ILIKE 并转义 % 与 _，否则用户搜 "%" 会命中全部记录
func (s *UserStore) List(ctx context.Context, f UserFilter) ([]model.UserView, error) {
	var (
		conds []string
		args  []any
	)

	statuses := f.Statuses
	if len(statuses) == 0 {
		statuses = []string{string(model.StatusActive), string(model.StatusDisabled)}
	}
	args = append(args, statuses)
	conds = append(conds, fmt.Sprintf("u.status = ANY($%d)", len(args)))

	if kw := strings.TrimSpace(f.Keyword); kw != "" {
		args = append(args, "%"+escapeLike(kw)+"%")
		conds = append(conds, fmt.Sprintf(
			"(u.username ILIKE $%d OR u.email ILIKE $%d)", len(args), len(args)))
	}
	if f.Role != "" {
		args = append(args, f.Role)
		conds = append(conds, fmt.Sprintf("u.role = $%d", len(args)))
	}
	if f.Ungrouped {
		conds = append(conds, "u.group_id IS NULL")
	} else if f.GroupID != nil {
		args = append(args, *f.GroupID)
		conds = append(conds, fmt.Sprintf("u.group_id = $%d", len(args)))
	}

	q := userSelect + " WHERE " + strings.Join(conds, " AND ") + " ORDER BY u.id"

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query users: %w", err)
	}
	defer rows.Close()

	out := []model.UserView{}
	for rows.Next() {
		v, err := scanUserView(rows)
		if err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

// Get 取单个用户（含组名）。不存在返回 ErrUserNotFound。
func (s *UserStore) Get(ctx context.Context, id int64) (*model.UserView, error) {
	v, err := scanUserView(s.pool.QueryRow(ctx, userSelect+` WHERE u.id = $1`, id))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("query user: %w", err)
	}
	return v, nil
}

// UpdateProfile 改邮箱、角色、所属组。用户名不可改（它是登录凭据的一部分，
// 且历史审计里的 actor_name 已经按当时的名字落库，改名只会让两边对不上）。
//
// 已删除账号直接拒绝：允许改它等于给「删除」留了后门，
// 而删除的语义是「不可恢复」。
func (s *UserStore) UpdateProfile(ctx context.Context, id int64, email string, role model.Role, groupID *int64) error {
	return s.withUserTx(ctx, id, true, func(tx pgx.Tx, status model.UserStatus) error {
		if groupID != nil {
			var exists bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS(SELECT 1 FROM user_group WHERE id = $1)`, *groupID,
			).Scan(&exists); err != nil {
				return fmt.Errorf("check group: %w", err)
			}
			if !exists {
				return ErrGroupMissing
			}
		}

		// 先读旧角色再判断，最后才写。顺序不能颠倒：
		// 若先 UPDATE 再读旧角色，读到的已经是新角色，降级判断永远不触发。
		var prevRole model.Role
		if err := tx.QueryRow(ctx,
			`SELECT role FROM app_user WHERE id = $1`, id).Scan(&prevRole); err != nil {
			return fmt.Errorf("query previous role: %w", err)
		}

		// 降级检查。这里不复用 ensureNotLastSuperAdmin：它的语义是
		// 「别把这个账号弄成不可用」，而本函数只关心「改完之后还有没有超管」。
		// 条件是「此人是 active 的超管 → 改成非超管」，此时必须确认还有别的超管。
		// 已停用的超管不计入可用数量，降级他不会让平台失去管理能力。
		if status == model.StatusActive && prevRole == model.RoleSuperAdmin && role != model.RoleSuperAdmin {
			ok, err := hasOtherActiveSuperAdmin(ctx, tx, id)
			if err != nil {
				return err
			}
			if !ok {
				return ErrLastSuperAdmin
			}
		}

		if _, err := tx.Exec(ctx,
			`UPDATE app_user SET email = $1, role = $2, group_id = $3 WHERE id = $4`,
			email, string(role), groupID, id); err != nil {
			return fmt.Errorf("update user: %w", err)
		}
		return nil
	})
}

// escapeLike 转义 LIKE 的通配符，让用户输入的 % 与 _ 按字面匹配。
//
// 不转义时，搜 "%" 会命中全部记录，看起来像「搜索没生效」；
// 用户很难想到自己搜的字符被当成了通配符。
//
// 反斜杠必须先替换，否则会把后面新加的转义符再转一次。
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// withUserTx 在一个事务里锁住目标用户行后执行 fn。
//
// 锁行是必需的：所有「改状态、删、降级」的判断都依赖「当前还有几个超管」，
// 而两个管理员同时互相降级时，各自读到的都还是「对方仍是超管」，
// 于是一起通过，最后平台一个超管都不剩。SELECT ... FOR UPDATE
// 让这两个事务串行执行，第二个必然看到第一个的结果。
//
// rejectDeleted 为 true 时，目标已删除直接返回 ErrUserDeleted；
// 只有 SoftDelete 自己传 false（它需要能对已删除账号幂等返回成功）。
func (s *UserStore) withUserTx(
	ctx context.Context,
	id int64,
	rejectDeleted bool,
	fn func(tx pgx.Tx, status model.UserStatus) error,
) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status model.UserStatus
	err = tx.QueryRow(ctx, `SELECT status FROM app_user WHERE id = $1 FOR UPDATE`, id).Scan(&status)
	if err != nil {
		if err == pgx.ErrNoRows {
			return ErrUserNotFound
		}
		return fmt.Errorf("lock user: %w", err)
	}
	if rejectDeleted && status == model.StatusDeleted {
		return ErrUserDeleted
	}

	if err := fn(tx, status); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// hasOtherActiveSuperAdmin 判断除 excludeID 之外是否还有 active 的 superadmin。
//
// 必须在事务内调用（withUserTx 已持有目标行的锁），
// 否则判断与写入之间存在并发窗口。
func hasOtherActiveSuperAdmin(ctx context.Context, tx pgx.Tx, excludeID int64) (bool, error) {
	var n int
	err := tx.QueryRow(ctx,
		`SELECT count(*) FROM app_user
		  WHERE role = $1 AND status = $2 AND id <> $3`,
		string(model.RoleSuperAdmin), string(model.StatusActive), excludeID,
	).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("count superadmins: %w", err)
	}
	return n > 0, nil
}

// SetStatus 启用或停用账号。只接受 active / disabled，其余值返回错误。
//
// 为什么要拦 deleted：deleted 只能由 SoftDelete 写入，它还改写了用户名。
// 如果让这个接口也能设成 deleted，就会出现「状态是删除、但用户名还占着」
// 以及反过来的「把 deleted 改回 active」——后者等于给删除开了后悔药，
// 而用户的直觉是「删除后原名可以给别人用」，两者会打架。
func (s *UserStore) SetStatus(ctx context.Context, id int64, status model.UserStatus) error {
	if !status.Assignable() {
		return fmt.Errorf("status %q cannot be assigned directly", status)
	}
	return s.withUserTx(ctx, id, true, func(tx pgx.Tx, current model.UserStatus) error {
		// 停用与删除共用同一套「会不会弄丢最后一个超管」的检查，
		// 抽成 ensureNotLastSuperAdmin 是为了让三处调用点（停用、删除、降级）
		// 的判断口径完全一致——各自写一遍最容易出现「停用拦了、降级没拦」的分叉。
		if status == model.StatusDisabled {
			if err := ensureNotLastSuperAdmin(ctx, tx, id, current); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx,
			`UPDATE app_user SET status = $1 WHERE id = $2`, string(status), id); err != nil {
			return fmt.Errorf("update status: %w", err)
		}
		return nil
	})
}

// ResetPassword 重置密码。
//
// 已删除账号拒绝：删除是不可恢复的，能重置密码就等于能把它用起来。
func (s *UserStore) ResetPassword(ctx context.Context, id int64, passwordHash string) error {
	return s.withUserTx(ctx, id, true, func(tx pgx.Tx, _ model.UserStatus) error {
		if _, err := tx.Exec(ctx,
			`UPDATE app_user SET password_hash = $1 WHERE id = $2`, passwordHash, id); err != nil {
			return fmt.Errorf("update password: %w", err)
		}
		return nil
	})
}

// SoftDelete 逻辑删除：标记 status='deleted'，并把用户名改写成
// <原名>#deleted#<id> 以释放原名。
//
// 为什么是逻辑删除而不是 DELETE：
// grant_record 与 grant_request 都以 user_id 非空外键指向 app_user，
// 物理删除要么被外键拒绝，要么得连带删掉授权与申请单——
// 而那是「谁在什么时候拿到过什么权限」的事实，删掉等于篡改审计口径。
// 逻辑删除后行还在，所有历史记录原样保留，且只按 id 关联，不会认错人。
//
// 为什么改写用户名：username 上有唯一索引，行还在就占着名字。
// 只把 status 改成 deleted 的话，同名新账号（离职交接、岗位轮替）
// 会一直 409，而用户的直觉是「人都删了名字当然该让出来」。
// 后缀用 id 而不是时间戳：id 天然唯一，改写结果必然无冲突；
// # 不在允许的用户名字符集内，改写后的名字也不会被新账号占回去。
//
// 幂等：对已删除账号再调一次直接返回 nil（重复点删除不该报错）。
func (s *UserStore) SoftDelete(ctx context.Context, id int64) error {
	return s.withUserTx(ctx, id, false, func(tx pgx.Tx, status model.UserStatus) error {
		if status == model.StatusDeleted {
			return nil // 幂等：重复点删除不该报错
		}
		if err := ensureNotLastSuperAdmin(ctx, tx, id, status); err != nil {
			return err
		}

		if _, err := tx.Exec(ctx,
			`UPDATE app_user
			    SET status = $1,
			        username = username || $2 || id,
			        email = ''
			  WHERE id = $3`,
			string(model.StatusDeleted), "#deleted#", id); err != nil {
			return fmt.Errorf("soft delete user: %w", err)
		}
		return nil
	})
}

// ensureNotLastSuperAdmin 在「会让平台失去最后一个可用超管」时返回 ErrLastSuperAdmin。
//
// 两个调用点共用：停用与删除。它们各自判断时最容易出现
// 「停用拦了、删除忘了」这种分叉，而后果都一样——平台再没有任何人能管理账号，
// 只能去库里手改，属于不可接受的运维事故。
// （降级那个调用点的前置条件不同，单独写在 UpdateProfile 里，见那里的注释。）
//
// 必须在 withUserTx 已持有目标行锁的事务里调用；否则「数剩余超管」
// 与「写入」之间存在并发窗口，两个管理员同时互相降级会双双通过。
//
// 判定顺序：先看这个人当前是不是 active 的超管（不是就根本不涉及超管数量），
// 再看除他之外还有没有 active 的超管。反过来写（先数、发现为 0 再回头查角色）
// 会让每次停用普通用户都多一次无效查询，而且注释里得解释「为何数完还查一次」。
func ensureNotLastSuperAdmin(ctx context.Context, tx pgx.Tx, id int64, current model.UserStatus) error {
	if current != model.StatusActive {
		// 已经是停用或删除状态，本来就不在「可用超管」的计数里，
		// 再操作他不会减少可用超管数量。
		return nil
	}
	var role model.Role
	if err := tx.QueryRow(ctx, `SELECT role FROM app_user WHERE id = $1`, id).Scan(&role); err != nil {
		return fmt.Errorf("query role: %w", err)
	}
	if role != model.RoleSuperAdmin {
		return nil
	}
	ok, err := hasOtherActiveSuperAdmin(ctx, tx, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrLastSuperAdmin
	}
	return nil
}
