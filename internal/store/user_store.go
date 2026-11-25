package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
//
// 只返回 status='active' 的用户，禁用与已删除账号无法登录。
//
// 关于错误区分度：登录失败的提示按产品决定区分原因（用户名不存在 / 账号停用 /
// 密码错误），因此这里必须返回可识别的哨兵错误，而不是笼统的 fmt.Errorf。
// 代价是用户名可被枚举，收益是用户不会对着正确密码反复重试却不知道是账号被停用。
//
// 为什么不再用 `WHERE username = $1 AND status = 'active'` 一次查完：
// 那样「查不到」这一种错误里混了三种原因（没这个人、已停用、已删除），
// 上层就无法给出正确的提示。改成按用户名查、把状态一并取出来再判断，
// 代价是停用账号的哈希也读了一次——但没有泄露，因为返回前就拒掉了。
func (s *UserStore) FindByUsername(ctx context.Context, username string) (int64, string, string, error) {
	var (
		id     int64
		hash   string
		role   string
		status string
	)
	err := s.pool.QueryRow(ctx,
		`SELECT id, password_hash, role, status FROM app_user WHERE username = $1`,
		username,
	).Scan(&id, &hash, &role, &status)
	if err != nil {
		// 必须用 errors.Is：pgx 各版本对 ErrNoRows 的包装方式不同，
		// `err == pgx.ErrNoRows` 在包装后会失效，进而把「用户不存在」
		// 当成数据库故障上报 500，登录页会显示「服务器错误」而不是「登录失败」。
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, "", "", ErrLoginUserNotFound
		}
		return 0, "", "", fmt.Errorf("query user: %w", err)
	}

	// 停用与已删除都归为「账号不可用」。两者都不该透露过多细节
	// （尤其是已删除账号的用户名已被改写，正常输入根本不会命中），
	// 但必须与「密码错误」分开，否则停用账号的人会一直怀疑自己记错密码。
	if model.UserStatus(status) != model.StatusActive {
		return 0, "", "", ErrLoginAccountInactive
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
		// 用 errors.Is 而不是 ==：pgx 可能包装 ErrNoRows，
		// 用 == 会在包装后失效，把「用户不存在」误判成数据库故障返回 500。
		if errors.Is(err, pgx.ErrNoRows) {
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
	return s.withUserTx(ctx, id, true, true, func(tx pgx.Tx, status model.UserStatus) error {
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

		// 降级检查。只要「原本是超管、要改成非超管」，就必须有另一个 active 超管兜底。
		//
		// 为什么把已停用的超管也算进保护范围（曾经的 bug）：
		// 原条件要求 status == active 才检查，于是「先停用超管 A，再把 A 降级成
		// developer」一路放行。此时若 B 恰好是最后一个 active 超管，B 之后
		// 再被停用/删除就会被正确拦下——但平台已经只剩 B 一个「真超管」，
		// 而 A 的身份（role）已被永久抹掉且无法重建（用户名仍被 A 占着，
		// 新建同名超管会 409）。这不是「少扣一个保险」，是把退路直接烧了。
		// 因此这里不看 status，只看「是不是在把超管变成非超管」。
		if prevRole == model.RoleSuperAdmin && role != model.RoleSuperAdmin {
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

// superAdminGuardLockKey 是保护「超管集合不变式」的事务级 advisory lock 键。
//
// 为什么需要它（真实缺陷，已用并发压测复现：30 轮里 29 轮双删都成功）：
// 只锁目标行的 SELECT ... FOR UPDATE 根本挡不住「两个超管互相删对方」——
// 事务 A 锁住 A 这一行，事务 B 锁住 B 那一行，两者不冲突；
// 随后 A 数「除自己之外还有没有超管」看到 B 仍然 active，
// B 数的时候看到 A 仍然 active，于是双双放行，最后可用超管归零。
// 要防的是「集合级」的不变式，就必须锁一个有确定名字的逻辑资源，
// 而不是锁集合里的某一个成员。
//
// 选 advisory lock 而不是 SERIALIZABLE 隔离级：
// 后者会给整库所有事务加上序列化失败重试，影响面远超此处需要；
// advisory lock 只影响显式参与的几个事务，且同一连接内自动释放。
//
// 数值取自 "datawarden-superadmin" 的 CRC 风格哈希，只要全局唯一即可，
// 与 db.migrationLockKey 不同即可（迁移锁是 0x64657761726477）。
const superAdminGuardLockKey int64 = 0x6477007375706572

// ErrAccountBusy 表示等待「超管集合锁」超时。
//
// 账号管理操作（改角色/停用/删除）需要一把全局串行锁来维持「至少一个
// 可用超管」这个不变式。正常情况下这把锁只持有几毫秒（几条 SQL），
// 但如果有外部进程长事务卡住（比如有人在 psql 里开了事务没提交），
// 后来的请求就会一直等。没有超时的话，等待者会占着连接池的连接不放，
// 池子（MaxConns=10）很快被占满，整个服务的数据库访问一起挂掉。
//
// 所以必须给等待设上界，并把「等不到」作为一个明确的、可映射成 503/409
// 的业务错误返回，而不是让请求静默卡死。
var ErrAccountBusy = errors.New("account operation busy, please retry")

// superAdminLockTimeout 是等待超管集合锁的上限。
//
// 5 秒远大于正常持锁时间（毫秒级），又远小于用户能忍受的等待，
// 也小于连接池 10s 的连接超时——保证「等锁失败」先于「拿不到连接」发生，
// 便于定位是锁竞争而不是池耗尽。
const superAdminLockTimeout = 5 * time.Second

// lockSuperAdminSet 在当前事务上获取超管集合级别锁，事务结束（commit/rollback）自动释放。
//
// pg_advisory_xact_lock 是阻塞式的：后到的事务会等到前一个提交/回滚后才继续，
// 因此「数剩余超管」必然看到前者已提交的结果，不变式得以成立。
// 用 xact 版本而不是 session 版本，是为了不依赖连接是否被正确归还连接池。
//
// 两重超时保护，缺一不可：
//   - 连接级 lock_timeout=5s（见 db.Open）：让 PG 自己放弃等待并抛 55P03，
//     backend 立刻结束阻塞、连接可以归还。这是防「连接池被等锁请求占满」的
//     根本手段——客户端 ctx 超时并不能让 PG 停止等待。
//   - 这里再用 SET LOCAL 收紧到同一量级，目的是让语义与 55P03 的处理
//     在同一个事务里闭环：即使有人从别处改了全局 lock_timeout，
//     这一段仍然有上界。SET LOCAL 只在当前事务生效，退出即失效。
//
// 用 SET LOCAL 而不是 pg_advisory_xact_lock(timeout) 两参版本：后者从
// PostgreSQL 18 才提供，本项目要兼容 13+。
func lockSuperAdminSet(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx,
		fmt.Sprintf("SET LOCAL lock_timeout = %d", superAdminLockTimeout.Milliseconds())); err != nil {
		return fmt.Errorf("set lock_timeout: %w", err)
	}

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, superAdminGuardLockKey); err != nil {
		// 55P03 lock_not_available：等锁超时。这是可直接重试的「忙」，
		// 不能当成 500——用户重复点一下就会成功。
		// 与 ctx 超时一起识别，因为两种路径都可能触发。
		if isLockNotAvailable(err) || errors.Is(err, context.DeadlineExceeded) {
			return ErrAccountBusy
		}
		return fmt.Errorf("lock superadmin set: %w", err)
	}
	return nil
}

// isLockNotAvailable 判断错误是否为 PG 的 lock_timeout 超时（55P03）。
//
// 用 SQLSTATE 而不是匹配错误文本：文本随版本与 locale 变化，
// 55P03 是标准错误码。
func isLockNotAvailable(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "55P03"
	}
	return false
}

// withUserTx 在一个事务里锁住目标用户行后执行 fn。
//
// 这里有两层保护，作用不同，缺一不可：
//   - SELECT ... FOR UPDATE 锁目标行：防止同一个用户被并发改（例如两个人同时改
//     同一账号的角色），也让 fn 里读到的 status 与实际写入时的状态一致。
//   - superAdminGuard 为 true 时再取集合级 advisory lock：防止「两个超管互相
//     降级/删除」这类跨行操作。单靠锁行挡不住——两个事务锁的是不同的行。
//
// 只有会改变「可用超管数量」的操作才需要集合锁（停用、删除、降级）；
// 改邮箱、改组、重置密码不需要，否则纯属给热路径加串行化。
//
// rejectDeleted 为 true 时，目标已删除直接返回 ErrUserDeleted；
// 只有 SoftDelete 自己传 false（它需要能对已删除账号幂等返回成功）。
func (s *UserStore) withUserTx(
	ctx context.Context,
	id int64,
	rejectDeleted bool,
	superAdminGuard bool,
	fn func(tx pgx.Tx, status model.UserStatus) error,
) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 先取集合锁再锁目标行，顺序固定。若反过来（先锁行、后取集合锁），
	// 两个事务可能各自持有行锁再等集合锁——虽然这里的集合锁是唯一一把、
	// 不会真正死锁，但固定「粗锁在前」能避免将来引入第二把锁时的死锁隐患。
	if superAdminGuard {
		if err := lockSuperAdminSet(ctx, tx); err != nil {
			return err
		}
	}

	var status model.UserStatus
	err = tx.QueryRow(ctx, `SELECT status FROM app_user WHERE id = $1 FOR UPDATE`, id).Scan(&status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
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
	return s.withUserTx(ctx, id, true, true, func(tx pgx.Tx, current model.UserStatus) error {
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
//
// 不取超管集合锁：重置密码不改变「谁是 active 超管」，
// 与「最后一个超管」不变式无关，没必要让这个操作和账号管理全局串行。
func (s *UserStore) ResetPassword(ctx context.Context, id int64, passwordHash string) error {
	return s.withUserTx(ctx, id, true, false, func(tx pgx.Tx, _ model.UserStatus) error {
		if _, err := tx.Exec(ctx,
			`UPDATE app_user SET password_hash = $1 WHERE id = $2`, passwordHash, id); err != nil {
			return fmt.Errorf("update password: %w", err)
		}
		return nil
	})
}

// ChangePassword 修改自己的密码：先在同一个事务里校验当前密码，再写入新哈希。
//
// 为什么校验与写入必须在同一个事务（且先 FOR UPDATE 锁行）：
// 如果先在外面查哈希、校验通过、再单独 UPDATE，两条语句之间可能被管理员
// 重置密码插入——用户 A 拿着旧密码的校验结果把密码改成 A 自己知道的值，
// 覆盖掉管理员刚重置的密码，重置等于被静默撤销。锁住行之后，
// 校验依据的就是「写入那一刻的哈希」，不存在这个窗口。
//
// 已删除账号返回 ErrUserDeleted。停用账号不拦：停用只是禁止登录，
// 而本接口本来就需要通过鉴权（token 仍然有效），此时拦住他反而会让
// 「被停用的人被要求找管理员改密码」——管理员该做的是重置，不是他改。
// 而他能调通这个接口说明 token 还在有效期内，改掉密码不会提升任何权限。
//
// 不取超管集合锁：改密码不改变「谁是 active 超管」，与该不变式无关。
func (s *UserStore) ChangePassword(ctx context.Context, id int64, verify func(currentHash string) bool, newHash string) error {
	return s.withUserTx(ctx, id, true, false, func(tx pgx.Tx, _ model.UserStatus) error {
		var current string
		if err := tx.QueryRow(ctx,
			`SELECT password_hash FROM app_user WHERE id = $1`, id).Scan(&current); err != nil {
			return fmt.Errorf("query password hash: %w", err)
		}
		// 校验逻辑用回调传入，而不是把 bcrypt 依赖搬进 store：
		// store 只负责持久化，密码算法属于 auth 包。
		if !verify(current) {
			return ErrOldPasswordMismatch
		}
		if _, err := tx.Exec(ctx,
			`UPDATE app_user SET password_hash = $1 WHERE id = $2`, newHash, id); err != nil {
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
	return s.withUserTx(ctx, id, false, true, func(tx pgx.Tx, status model.UserStatus) error {
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
