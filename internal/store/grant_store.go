package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/opensourceprj/datawarden/internal/authz"
	"github.com/opensourceprj/datawarden/internal/model"
)

type GrantStore struct {
	pool *pgxpool.Pool
}

func NewGrantStore(pool *pgxpool.Pool) *GrantStore {
	return &GrantStore{pool: pool}
}

func (s *GrantStore) Create(ctx context.Context, g *model.Grant) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO grant_record (user_id, datasource_id, scope, action, can_export, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		g.UserID, g.DatasourceID, g.Scope, g.Action, g.CanExport, g.ExpiresAt,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert grant: %w", err)
	}
	return id, nil
}

// ActiveGrants 返回未撤销且未过期的授权，供 Casbin 重建策略。
//
// 必须排除已逻辑删除的账号：grant_record 的行在删除用户时是保留的
// （那是「谁在什么时候拿到过什么权限」的事实），若这里不加判断，
// 策略重建会把一个已删账号的授权重新加载进内存——等于「人删了、权限还在」。
// 排除放在查询里而不是调用方：策略重建是安全关键路径，
// 让它在数据源头上就只拿到该生效的行，比指望每个调用方都记得过滤可靠。
//
// 停用（disabled）不在此排除：停用的权限失效靠「AllRoles 只取 active
// → g 段没有这个人 → 策略不生效」，授权记录本身保留，便于恢复。
func (s *GrantStore) ActiveGrants(ctx context.Context) ([]model.Grant, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT g.id, g.user_id, g.datasource_id, g.scope, g.action, g.can_export,
		        g.expires_at, g.revoked_at, g.created_at
		 FROM grant_record g
		 WHERE g.revoked_at IS NULL
		   AND g.expires_at > $1
		   AND EXISTS (
		       SELECT 1 FROM app_user u
		        WHERE u.id = g.user_id AND u.status <> $2
		   )
		 ORDER BY g.id`, time.Now(), string(model.StatusDeleted))
	if err != nil {
		return nil, fmt.Errorf("query active grants: %w", err)
	}
	defer rows.Close()

	out := []model.Grant{}
	for rows.Next() {
		var g model.Grant
		if err := rows.Scan(&g.ID, &g.UserID, &g.DatasourceID, &g.Scope,
			&g.Action, &g.CanExport, &g.ExpiresAt, &g.RevokedAt, &g.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan grant: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Revoke 撤销一条授权。幂等：已撤销的再次调用不报错。
func (s *GrantStore) Revoke(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE grant_record SET revoked_at = now()
		 WHERE id = $1 AND revoked_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("revoke grant: %w", err)
	}
	return nil
}

// ListActive 列出所有生效中的授权（未撤销且未过期），供管理页展示与回收。
//
// 与 ActiveGrants 的区别是带了 username / datasource_name：
// 管理页要显示「谁能查哪个库」，只有 ID 没法用。
//
// 同样排除已逻辑删除的账号：行还在（审计需要），但它已经不代表任何可用权限。
// 显示在「授权管理」页会让 DBA 误判「这个人还能查库」而去点回收，
// 回收一条语义上已失效的记录纯属噪音。
func (s *GrantStore) ListActive(ctx context.Context, limit int) ([]model.GrantView, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx,
		`SELECT g.id, g.user_id, u.username, g.datasource_id, d.name,
		        g.scope, g.action, g.can_export, g.expires_at, g.created_at
		 FROM grant_record g
		 JOIN app_user u   ON u.id = g.user_id
		 JOIN datasource d ON d.id = g.datasource_id
		 WHERE g.revoked_at IS NULL AND g.expires_at > now()
		   AND u.status <> $2
		 ORDER BY g.expires_at, g.id
		 LIMIT $1`, limit, string(model.StatusDeleted))
	if err != nil {
		return nil, fmt.Errorf("list active grants: %w", err)
	}
	defer rows.Close()

	out := []model.GrantView{}
	for rows.Next() {
		var g model.GrantView
		if err := rows.Scan(&g.ID, &g.UserID, &g.Username, &g.DatasourceID,
			&g.DatasourceName, &g.Scope, &g.Action, &g.CanExport, &g.ExpiresAt, &g.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan grant view: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// MyDatasources 返回某用户当前有权查询的数据源，按数据源聚合授权 scope。
//
// 用途是查询页的数据源下拉：开发者不该调 DBA 专属的 GET /datasources
// （那条路由限权，且返回 host/port/username），但查询页又必须知道
// 「我能查哪些数据源」。所以这里按 user_id 反查生效授权，再联表取数据源。
//
// 只返回精简字段：host/port/username 一律不带出去，
// SELECT 里就是明确的列清单，避免以后有人图省事写成 SELECT *。
//
// 用 array_agg 一次性把同一数据源的多个 scope 收成数组，
// 避免在 Go 层循环里再查一次（N+1）。
//
// 导出权限用 bool_or 聚合成一个布尔值：同一数据源上可能同时存在多条
// 生效授权（续期没撤销旧的、或改口径前批过一条），只要其中一条批了导出，
// 这个人就能导出——权限取「或」而不是「最后一条」。用 SQL 聚合而不是
// 在 Go 里扫数组，是为了让「多条授权怎么合并」只有一处定义。
func (s *GrantStore) MyDatasources(ctx context.Context, userID int64) ([]model.DatasourceBrief, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT d.id, d.name, d.type, d.description, d.db_name,
		        array_agg(g.scope ORDER BY g.scope) AS scopes,
		        bool_or(g.can_export) AS can_export
		 FROM grant_record g
		 JOIN datasource d ON d.id = g.datasource_id
		 WHERE g.user_id = $1
		   AND g.revoked_at IS NULL
		   AND g.expires_at > now()
		 GROUP BY d.id, d.name, d.type, d.description, d.db_name
		 ORDER BY d.name`, userID)
	if err != nil {
		return nil, fmt.Errorf("query my datasources: %w", err)
	}
	defer rows.Close()

	out := []model.DatasourceBrief{}
	for rows.Next() {
		var b model.DatasourceBrief
		var typ string
		var rawScopes []string
		if err := rows.Scan(&b.ID, &b.Name, &typ, &b.Description,
			&b.Database, &rawScopes, &b.CanExport); err != nil {
			return nil, fmt.Errorf("scan my datasource: %w", err)
		}
		b.Type = model.DatasourceType(typ)
		b.Scopes = parseScopes(rawScopes)
		out = append(out, b)
	}
	return out, rows.Err()
}

// AllDatasources 返回全量数据源的精简视图，供 dba / superadmin 的查询页下拉使用。
//
// 与 MyDatasources 的区别只是「过滤条件」：管理角色不参与授权，
// 查询范围就是全部数据源，没有 grant_record 可 join。两者返回同一形态，
// 前端不需要为角色写两套渲染逻辑。
//
// 字段清单与 MyDatasources 严格一致（不含 host/port/username）：
// 即便是 DBA，查询页也没有展示内网拓扑的需求，多余字段只会随响应扩散出去。
// Scopes 固定为空数组——给管理角色构造「全库全表」的假授权记录会误导前端展示。
//
// CanExport 恒为 true：导出能力与查询能力绑定，管理角色既然免授权就能查
// 任意数据源，就没有理由拦着他们导出——况且免授权账号本来就已经能拿到
// 全部数据，闸门装在这里只是让自己用不了功能。真正需要卡的是开发者，
// 而他们的取值来自 grant_record.can_export。
func (s *GrantStore) AllDatasources(ctx context.Context) ([]model.DatasourceBrief, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, type, description, db_name
		 FROM datasource
		 ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("query all datasources: %w", err)
	}
	defer rows.Close()

	out := []model.DatasourceBrief{}
	for rows.Next() {
		var (
			b   model.DatasourceBrief
			typ string
		)
		if err := rows.Scan(&b.ID, &b.Name, &typ, &b.Description, &b.Database); err != nil {
			return nil, fmt.Errorf("scan datasource brief: %w", err)
		}
		b.Type = model.DatasourceType(typ)
		b.Scopes = []model.ScopeView{}
		b.CanExport = true
		out = append(out, b)
	}
	return out, rows.Err()
}

// normalizeForDisplay 是展示用的归一：在 authz.NormalizeScope 之上
// 额外接受 "*:*" 这种写法。
//
// 为什么需要这一层：NormalizeScope 只接受 "*"（它得保证写进 Casbin 的
// 策略形态唯一），所以 "*:*" 经它处理会变成空串被当成畸形数据丢弃。
// 但 "*:*" 在库里确实可能存在（手工改库、外部脚本、早期实验版本），
// 而它的语义毫无歧义——就是整个数据源。直接丢弃的后果是权限页上
// 凭空少一条记录，用户看到「我明明有授权」却查不到，比保留更糟。
//
// 反过来必须说清楚：这只是**展示**层放宽。鉴权侧 SyncGrants 仍然
// 跳过 "*:*"（不生成策略），所以这类记录是「显示得对、实际不生效」，
// 保留它正是为了让它在界面上可见、能被发现和清理。
func normalizeForDisplay(scope string) string {
	if strings.TrimSpace(scope) == authz.ScopeDatabaseAll {
		return authz.ScopeDatabaseAll
	}
	return authz.NormalizeScope(scope)
}

// parseScopes 把授权 scope 字符串拆成结构化形态。
// 两种整数据源写法都收成同一档：
//   - "*"                 → {Database: "*", Tables: nil}
//   - "*:*"               → 同上（NormalizeScope 拒绝它，见 normalizeForDisplay）
//   - "shop" 或 "shop:*"  → {Database: "shop", Tables: nil}（整库）
//   - "shop:orders,users" → {Database: "shop", Tables: [orders, users]}
//
// 注意 Database 对外统一是 "*"（authz.ScopeAllDatabases），不是 "*:*"：
// 前端 isAllDatabasesScope 两种都认，但内部只用一个值，
// 免得同一档权限在两个地方比较出不同结果。
//
// 不能把 "*" 当成一个表名塞进 Tables，否则前端会渲染出一张叫 * 的表，
// 也让「整库」和「全表清单」两种语义混在一起。
//
// 「整个数据源」是一档独立的等级，不是库名恰好叫 "*"：
// 同一数据源上既有整源授权又有历史库级授权时（改口径之前审批的记录），
// 只保留整源那一条——否则查询页会同时显示「所有库表」和「某个库」，
// 用户无法判断自己的权限边界到底在哪。
//
// 两件事分开做：
//  1. 归一（畸形返回空串，一律丢弃，见 normalizeForDisplay）
//  2. 再把合法的 "库:表,表" 拆成结构
//
// 为什么不自己解析：库里可能存在历史脏数据（早期版本写入的、或手工改库的）。
// 若这里自己宽松解析，"shop:*:*" 会变成 {shop, ["*:*"]} 显示到界面上，
// 用户看到一条语义不明的权限却无从自查。
func parseScopes(raw []string) []model.ScopeView {
	if len(raw) == 0 {
		return []model.ScopeView{}
	}
	out := make([]model.ScopeView, 0, len(raw))
	seen := map[string]int{} // 库名 → out 下标，用于合并同一个库的多条授权
	allIdx := -1             // 整数据源授权在 out 里的下标

	for _, sc := range raw {
		norm := normalizeForDisplay(sc)
		if norm == "" {
			// 非法/畸形 scope 不展示：它要么永远匹配不上（无意义），
			// 要么语义不明（误导）。跳过比显示一条看不懂的权限更好。
			continue
		}
		// 整数据源授权：单独一档，且一旦出现就是最高等级
		if norm == authz.ScopeDatabaseAll {
			if allIdx < 0 {
				allIdx = len(out)
				out = append(out, model.ScopeView{Database: authz.ScopeAllDatabases, Tables: nil})
			}
			continue
		}
		db, rest, _ := strings.Cut(norm, ":")
		var tables []string
		if rest != "*" {
			for _, t := range strings.Split(rest, ",") {
				if t = strings.TrimSpace(t); t != "" {
					tables = append(tables, t)
				}
			}
		}
		if db == "" {
			continue
		}

		if idx, ok := seen[db]; ok {
			// 同一个库的多次授权合并：任一为整库则整体是整库
			if out[idx].Tables == nil || tables == nil {
				out[idx].Tables = nil
			} else {
				out[idx].Tables = append(out[idx].Tables, tables...)
			}
			continue
		}
		seen[db] = len(out)
		out = append(out, model.ScopeView{Database: db, Tables: tables})
	}

	// 有整源授权时，库级明细全是冗余：权限已经被整源覆盖，
	// 留着只会让前端显示得比实际更窄。
	//
	// 判断用 allIdx >= 0 而不是 == 0：整源授权不一定排在第一项
	// （scopes 是按 SQL 的 ORDER BY 或 array_agg 顺序来的，"*" 排在
	// "shop:*" 之后就得到下标 1）。只判断 == 0 会让 ["shop:*", "*"]
	// 同时返回两档，前端显示出两个互不相容的权限等级。
	// 命中时不直接返回 out[:allIdx+1]，而是重建只含该项的切片，
	// 避免把前面已收集的库级明细一起带出去。
	if allIdx >= 0 {
		return []model.ScopeView{out[allIdx]}
	}

	// 合并后去重（同一张表可能被多条授权覆盖）
	for i := range out {
		if out[i].Tables == nil {
			continue
		}
		uniq := make([]string, 0, len(out[i].Tables))
		seenT := map[string]bool{}
		for _, t := range out[i].Tables {
			if !seenT[t] {
				seenT[t] = true
				uniq = append(uniq, t)
			}
		}
		sort.Strings(uniq)
		out[i].Tables = uniq
	}
	return out
}

// Get 读一条授权，用于吊销前确认存在性与归属。
func (s *GrantStore) Get(ctx context.Context, id int64) (*model.Grant, error) {
	var g model.Grant
	err := s.pool.QueryRow(ctx,
		`SELECT id, user_id, datasource_id, scope, action, can_export,
		        expires_at, revoked_at, created_at
		 FROM grant_record WHERE id = $1`, id,
	).Scan(&g.ID, &g.UserID, &g.DatasourceID, &g.Scope, &g.Action,
		&g.CanExport, &g.ExpiresAt, &g.RevokedAt, &g.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("query grant %d: %w", id, err)
	}
	return &g, nil
}

// SetCanExport 单独开关某条授权的导出能力，仅 DBA / superadmin 可调。
//
// 为什么需要这个入口：审批通过时导出是随申请单一起批的，
// 但 DBA 审批时可能没注意到勾选，事后想把导出关掉（数据敏感、
// 或这段时间在排查泄漏）只能等授权过期或整条回收——
// 整条回收会把「查询」一起收走，代价过大。
//
// 只改 grant_record，不动 grant_request：申请单是"当时申请了什么"的留痕，
// 事后调整权限不该去改历史单据。审计日志负责记录这次变更（谁、什么时候、
// 哪个用户哪个源），查不到情况时以审计为准。
//
// 返回 (是否命中, error)：没命中说明授权不存在；调用方据此区分 404 与 500。
func (s *GrantStore) SetCanExport(ctx context.Context, id int64, canExport bool) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE grant_record SET can_export = $2 WHERE id = $1`, id, canExport)
	if err != nil {
		return false, fmt.Errorf("update grant can_export: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// CanExport 判断某用户在某数据源上是否有导出权限。
//
// 这是导出接口的唯一闸门，因此判定条件必须与「能查询」严格一致，
// 否则会出现两种方向相反的事故：
//   - 判宽了（只看 can_export=true）：一条已撤销或已过期的授权仍能导出，
//     等于给了比查询更大的权限面；
//   - 判严了（漏掉某一档 scope）：整源授权能查却不能导出。
//
// 所以这里复用 ActiveGrants 的过滤条件（未撤销、未过期、账号未被删除），
// 再加一条 can_export。多条授权取「或」，与 MyDatasources 的 bool_or 一致。
//
// 停用（disabled）账号不在 SQL 里排除：它们能登录本身就被登录链路挡了，
// 而已签发的 token 在 12 小时内仍可用——这个口径与查询侧完全相同，
// 导出不该比查询更严（那会造成「能查不能导」的困惑）。
func (s *GrantStore) CanExport(ctx context.Context, userID, datasourceID int64) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (
		     SELECT 1 FROM grant_record g
		      WHERE g.user_id = $1
		        AND g.datasource_id = $2
		        AND g.can_export
		        AND g.revoked_at IS NULL
		        AND g.expires_at > now()
		        AND EXISTS (
		            SELECT 1 FROM app_user u
		             WHERE u.id = g.user_id AND u.status <> $3
		        )
		 )`, userID, datasourceID, string(model.StatusDeleted)).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("query export permission: %w", err)
	}
	return ok, nil
}

// GrantScopes 返回某用户在某数据源上生效授权的 scope 列表。
//
// 元数据接口用它来决定「只授权了部分表时该显示哪些表」：
// 不能去列整库的表，那会把用户无权看到的表名也暴露出来。
func (s *GrantStore) GrantScopes(ctx context.Context, userID, datasourceID int64) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT scope FROM grant_record
		 WHERE user_id = $1 AND datasource_id = $2
		   AND revoked_at IS NULL AND expires_at > now()
		 ORDER BY id`, userID, datasourceID)
	if err != nil {
		return nil, fmt.Errorf("query grant scopes: %w", err)
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var scope string
		if err := rows.Scan(&scope); err != nil {
			return nil, fmt.Errorf("scan grant scope: %w", err)
		}
		out = append(out, scope)
	}
	return out, rows.Err()
}
