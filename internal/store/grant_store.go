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
		`INSERT INTO grant_record (user_id, datasource_id, scope, action, expires_at)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		g.UserID, g.DatasourceID, g.Scope, g.Action, g.ExpiresAt,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert grant: %w", err)
	}
	return id, nil
}

// ActiveGrants 返回未撤销且未过期的授权，供 Casbin 重建策略。
func (s *GrantStore) ActiveGrants(ctx context.Context) ([]model.Grant, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, user_id, datasource_id, scope, action, expires_at, revoked_at, created_at
		 FROM grant_record
		 WHERE revoked_at IS NULL AND expires_at > $1
		 ORDER BY id`, time.Now())
	if err != nil {
		return nil, fmt.Errorf("query active grants: %w", err)
	}
	defer rows.Close()

	out := []model.Grant{}
	for rows.Next() {
		var g model.Grant
		if err := rows.Scan(&g.ID, &g.UserID, &g.DatasourceID, &g.Scope,
			&g.Action, &g.ExpiresAt, &g.RevokedAt, &g.CreatedAt); err != nil {
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
func (s *GrantStore) ListActive(ctx context.Context, limit int) ([]model.GrantView, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx,
		`SELECT g.id, g.user_id, u.username, g.datasource_id, d.name,
		        g.scope, g.action, g.expires_at, g.created_at
		 FROM grant_record g
		 JOIN app_user u   ON u.id = g.user_id
		 JOIN datasource d ON d.id = g.datasource_id
		 WHERE g.revoked_at IS NULL AND g.expires_at > now()
		 ORDER BY g.expires_at, g.id
		 LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list active grants: %w", err)
	}
	defer rows.Close()

	out := []model.GrantView{}
	for rows.Next() {
		var g model.GrantView
		if err := rows.Scan(&g.ID, &g.UserID, &g.Username, &g.DatasourceID,
			&g.DatasourceName, &g.Scope, &g.Action, &g.ExpiresAt, &g.CreatedAt); err != nil {
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
func (s *GrantStore) MyDatasources(ctx context.Context, userID int64) ([]model.DatasourceBrief, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT d.id, d.name, d.type, d.description, d.db_name,
		        array_agg(g.scope ORDER BY g.scope) AS scopes
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
			&b.Database, &rawScopes); err != nil {
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
		out = append(out, b)
	}
	return out, rows.Err()
}

// parseScopes 把授权 scope 字符串拆成结构化形态。
// 归一规则与 authz.NormalizeScope / httpapi.splitScope 保持一致：
//   - "shop" 或 "shop:*"  → {Database: "shop", Tables: nil}（整库）
//   - "shop:orders,users" → {Database: "shop", Tables: [orders, users]}
//
// 不能把 "*" 当成一个表名塞进 Tables，否则前端会渲染出一张叫 * 的表，
// 也让「整库」和「全表清单」两种语义混在一起。
//
// 两件事分开做：
//  1. 用 authz.NormalizeScope 做**校验与归一**（畸形返回空串，一律丢弃）
//  2. 再把合法的 "库:表,表" 拆成结构
//
// 为什么不自己解析：入口（申请）已经用 NormalizeScope 拦过畸形输入，
// 但库里可能存在历史脏数据（早期版本写入的、或手工改库的）。
// 若这里自己宽松解析，"shop:*:*" 会变成 {shop, ["*:*"]} 显示到界面上，
// 用户看到一条语义不明的权限却无从自查。复用同一个函数，
// 保证「能存进去的」和「能显示的」永远是同一套规则。
func parseScopes(raw []string) []model.ScopeView {
	if len(raw) == 0 {
		return []model.ScopeView{}
	}
	out := make([]model.ScopeView, 0, len(raw))
	seen := map[string]int{} // 库名 → out 下标，用于合并同一个库的多条授权

	for _, sc := range raw {
		norm := authz.NormalizeScope(sc)
		if norm == "" {
			// 非法/畸形 scope 不展示：它要么永远匹配不上（无意义），
			// 要么语义不明（误导）。跳过比显示一条看不懂的权限更好。
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
		`SELECT id, user_id, datasource_id, scope, action, expires_at, revoked_at, created_at
		 FROM grant_record WHERE id = $1`, id,
	).Scan(&g.ID, &g.UserID, &g.DatasourceID, &g.Scope, &g.Action,
		&g.ExpiresAt, &g.RevokedAt, &g.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("query grant %d: %w", id, err)
	}
	return &g, nil
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
