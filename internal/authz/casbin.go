package authz

import (
	"fmt"

	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
)

// GrantPolicy 是从数据库读出的授权记录，用于重建策略。
type GrantPolicy struct {
	UserID       int64
	DatasourceID int64
	Scope        string // "db:*" 或 "db:tbl1,tbl2"
	Action       string
}

// Enforcer 包装 Casbin，收敛策略操作入口。
type Enforcer struct {
	e *casbin.Enforcer
}

// modelText 定义权限模型。
// 用 keyMatch 而不是 keyMatch2：keyMatch2 把 : 视为路径参数分隔符，
// 会导致 "ds:1:shop:*" 错误匹配 "ds:1:other:orders"（跨库越权）。
// keyMatch 只处理 * 通配，语义与我们的 ds:<id>:<db>:<tbl> 格式一致。
const modelText = `
[request_definition]
r = sub, obj, act

[policy_definition]
p = sub, obj, act

[role_definition]
g = _, _

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = (r.sub == p.sub || g(r.sub, p.sub)) && keyMatch(r.obj, p.obj) && r.act == p.act
`

func NewEnforcer() (*Enforcer, error) {
	m, err := model.NewModelFromString(modelText)
	if err != nil {
		return nil, fmt.Errorf("build casbin model: %w", err)
	}
	e, err := casbin.NewEnforcer(m)
	if err != nil {
		return nil, fmt.Errorf("create casbin enforcer: %w", err)
	}
	return &Enforcer{e: e}, nil
}

// AddPolicy 添加一条直接授予策略，subject 需自带 "u:" 前缀。
func (e *Enforcer) AddPolicy(subject, object, action string) bool {
	ok, _ := e.e.AddPolicy(subject, object, action)
	return ok
}

// AddRoleForUser 给用户绑角色，role 名不带前缀。
func (e *Enforcer) AddRoleForUser(user, role string) error {
	_, err := e.e.AddRoleForUser(user, role)
	return err
}

// Enforce 判断是否允许。subject 形如 "u:42"。
// 出错时一律拒绝（fail-closed），不因内部错误放大权限。
func (e *Enforcer) Enforce(subject, object, action string) bool {
	ok, err := e.e.Enforce(subject, object, action)
	if err != nil {
		return false
	}
	return ok
}

// SyncGrants 用数据库授权记录全量重建直接策略。
// 只清 p 段（直接授权），不动 g 段（角色绑定），否则角色会被一并清空。
func (e *Enforcer) SyncGrants(grants []GrantPolicy) error {
	policies, err := e.e.GetPolicy()
	if err != nil {
		return fmt.Errorf("get policies: %w", err)
	}
	for _, p := range policies {
		if len(p) < 3 {
			continue
		}
		if _, err := e.e.RemovePolicy(p[0], p[1], p[2]); err != nil {
			return fmt.Errorf("remove policy: %w", err)
		}
	}

	for _, g := range grants {
		object := fmt.Sprintf("ds:%d:%s", g.DatasourceID, g.Scope)
		if _, err := e.e.AddPolicy(fmt.Sprintf("u:%d", g.UserID), object, g.Action); err != nil {
			return fmt.Errorf("add grant policy: %w", err)
		}
	}
	return nil
}

// SyncRoles 重建角色绑定，数据来源是 app_user.role。
// 调用顺序必须是先 SyncRoles 再 SyncGrants，否则 g 段为空，角色权限不生效。
func (e *Enforcer) SyncRoles(userRoles map[int64]string) error {
	existing, err := e.e.GetGroupingPolicy()
	if err != nil {
		return fmt.Errorf("get grouping policy: %w", err)
	}
	for _, gp := range existing {
		if len(gp) < 2 {
			continue
		}
		if _, err := e.e.RemoveGroupingPolicy(gp[0], gp[1]); err != nil {
			return fmt.Errorf("remove grouping policy: %w", err)
		}
	}

	for userID, role := range userRoles {
		if role == "" {
			continue
		}
		if _, err := e.e.AddRoleForUser(fmt.Sprintf("u:%d", userID), role); err != nil {
			return fmt.Errorf("bind role for user %d: %w", userID, err)
		}
	}
	return nil
}
