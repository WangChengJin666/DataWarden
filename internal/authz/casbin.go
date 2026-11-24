package authz

import (
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
	appmodel "github.com/opensourceprj/datawarden/internal/model"
)

// GrantPolicy 是从数据库读出的授权记录，用于重建策略。
type GrantPolicy struct {
	UserID       int64
	DatasourceID int64
	Scope        string // "db:*" 或 "db:tbl1,tbl2"
	Action       string
}

// Enforcer 包装 Casbin，收敛策略操作入口。
//
// 自带 RWMutex 是必需的，不是防御性冗余：
//
//	普通 casbin.Enforcer 没有任何锁（只有 SyncedEnforcer 才有）。
//	本项目有一个 60 秒的定时任务在重建策略（SyncGrants 会先 RemovePolicy
//	再 AddPolicy，中间存在「策略被清空」的窗口），而同一时刻 HTTP 请求
//	正在调 Enforce 读策略表。Go 的 map 并发读写会直接 panic
//	（fatal error: concurrent map read and map write），这不是数据竞争
//	检测器才能发现的隐蔽问题，是会让整个进程崩掉的真实故障。
//
// 用 RWMutex 而不是改用 SyncedEnforcer：SyncedEnforcer 的写方法种类多，
// 我们只需要包住「本包暴露的那几个入口」，自己控锁边界更清楚。
// 读用 RLock（Enforce 远多于写），写用 Lock。
type Enforcer struct {
	mu sync.RWMutex
	e  *casbin.Enforcer
}

// BuiltinPolicyObject 是 dba / superadmin 的内置策略对象：全部数据源、全部库表。
//
// 必须是四段完整形式 ds:*:*:*，不能写成三段 ds:*。
// keyMatch 的语义是「以模式前缀匹配」，"ds:*" 对 "ds:1:public:orders"
// 的确是前缀命中（结尾是 *，吞掉后面全部），但写法上省略了两段，
// 后面任何一次调整 object 拼装规则（比如给 Redis 的 db 号加类型前缀）
// 都会让这条策略悄悄失效，失效方式是「管理角色突然全线 403」。
// 写成四段等于把 glob 的边界和 object 的段数钉在一起，
// 有段数变动时这条策略会在 code review 里立刻显形。
//
// 与之对应的判定散落在：querysvc.ExecuteSQL / ExecuteMongo 的逐表鉴权、
// httpapi.RedisHandler.EnforceKey、MetadataHandler.allowed。
// 它们都只调 Enforce，不重复实现角色判断——角色到策略的映射只发生在
// reAddBuiltinPolicies 一处。
const BuiltinPolicyObject = "ds:*:*:*"

// PrivilegedPolicySubjects 是需要全量查询权限的内置角色。
//
// 用切片而不是硬编码两行 AddPolicy：新增管理角色（审计员、只读运维）时
// 只改这里，不用去翻调用点。导出给测试与启动装配共用同一份清单。
var PrivilegedPolicySubjects = []string{
	string(appmodel.RoleDBA),
	string(appmodel.RoleSuperAdmin),
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
	e.mu.Lock()
	defer e.mu.Unlock()
	ok, _ := e.e.AddPolicy(subject, object, action)
	return ok
}

// AddRoleForUser 给用户绑角色，role 名不带前缀。
func (e *Enforcer) AddRoleForUser(user, role string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, err := e.e.AddRoleForUser(user, role)
	return err
}

// Enforce 判断是否允许。subject 形如 "u:42"。
// 出错时一律拒绝（fail-closed），不因内部错误放大权限。
//
// 用 RLock：调用频率远高于策略写入，读锁之间可以并发。
func (e *Enforcer) Enforce(subject, object, action string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	ok, err := e.e.Enforce(subject, object, action)
	if err != nil {
		return false
	}
	return ok
}

// NormalizeScope 把授权范围规范成「与鉴权对象同构」的形态。
//
// 鉴权对象是 ds:<数据源ID>:<库>:<表>，因此 scope 必须恰好是 <库>:<表>：
//
//	"shop"              → "shop:*"       整库
//	"shop:*"            → "shop:*"       已是整库，不变
//	"shop:orders,users" → 原样             表级清单
//
// 为什么必须做这一步（真实故障）：前端提交整库时传的是裸库名 "shop"，
// 若原样入库，生成的策略是 ds:<id>:shop——只有两段。而鉴权请求是
// ds:<id>:shop:orders，keyMatch 不会认为它匹配 ds:<id>:shop。
// 结果是「审批通过、界面显示已授权，但用户查询全是 403」的静默失效。
//
// 返回值语义：合法则返回规范形态，**非法返回空串**，由调用方拒绝。
// 不做「尽力修复」——畸形 scope 修出来的策略大概率是错的，
// 而错的策略比没有策略更难发现（用户看到「已授权」但查不了，
// 或者反过来放行了不该放行的范围）。
//
// 非法输入包括：
//   - 空串、纯空白
//   - 库名为空（":orders"）
//   - 多余的冒号（"a:b:c"）——鉴权对象里表名不允许含冒号
//   - 整段通配（"*"）——它会被拼成 ds:<id>:*:*，等于本数据源全库全表，
//     远超「申请某个库」的本意，必须显式走 DBA 角色而不是申请流程
//   - 任一段含 * ?  [ 等通配符（"shop:ord*" 会意外匹配 orders2）
func NormalizeScope(scope string) string {
	s := strings.TrimSpace(scope)
	if s == "" || s == "*" {
		return ""
	}

	db, tables, hasColon := strings.Cut(s, ":")
	db = strings.TrimSpace(db)
	if db == "" || !validScopeSegment(db, true) {
		return ""
	}

	if !hasColon {
		return db + ":*"
	}
	// 表清单里不允许再出现冒号：鉴权对象是四段，多一段永远匹配不上
	if strings.Contains(tables, ":") {
		return ""
	}

	parts := make([]string, 0, 4)
	for _, t := range strings.Split(tables, ",") {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		// "*" 单独出现表示整库，归一成「不列具体表」
		if t == "*" {
			return db + ":*"
		}
		if !validScopeSegment(t, false) {
			return ""
		}
		parts = append(parts, t)
	}
	if len(parts) == 0 {
		// "shop:" 这种尾部为空的写法，按整库处理
		return db + ":*"
	}
	return db + ":" + strings.Join(parts, ",")
}

// validScopeSegment 校验 scope 的一段（库名或表名）是否可用。
//
// 库名允许点（PG 的 schema 可以是 "my.data" 风格），表名也允许——
// 但两者都不允许 * ? [ ] 这些会改变 keyMatch 语义的字符。
// 这一段校验的意义：策略里的对象是 keyMatch 的模式串，
// 用户可控的字符直接变成通配符的话，一条 "shop:ord*" 就能覆盖
// 所有以 ord 开头的表，而授权界面显示的是「只授权了 ord* 这一张」。
func validScopeSegment(s string, isDB bool) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == '.':
		case r == ' ':
			// 表名里允许空格（少见但合法）；库名不允许
			if isDB {
				return false
			}
		default:
			// 白名单收得很紧（字母数字 + _ - .），这会连带拒绝一些
			// 数据库本身允许的标识符，例如 MongoDB 集合名里的 `$`
			// （"system.$cmd"）或中文表名。这是有意的取舍：
			// 这些字符一旦进入策略对象，会被 keyMatch 当成模式的一部分，
			// 而本平台的授权粒度是「库 + 表」，用非 ASCII/`$` 命名的对象
			// 极少见；真遇到时可以给该数据源配 DBA 角色绕过申请流程。
			return false
		}
	}
	// 库名不能以点开头或结尾（".users" 会被 SplitDBCollection 误判）
	if isDB && (strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".")) {
		return false
	}
	return true
}

// SyncGrants 用数据库授权记录全量重建直接策略。
// 只清 p 段（直接授权），不动 g 段（角色绑定），否则角色会被一并清空。
//
// 整个「先清空再重建」必须在同一把写锁内完成：
// 中途解锁会让其他 goroutine 看到「策略为空」的中间状态，
// 表现为短暂的全员无权限（fail-closed 下就是一片 403）。
func (e *Enforcer) SyncGrants(grants []GrantPolicy) error {
	e.mu.Lock()
	defer e.mu.Unlock()

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

	skipped := 0
	for _, g := range grants {
		// 出口兜底：即使库里存的是裸库名（历史数据、或绕过 handler 直接
		// GrantStore.Create 写入的记录），也要在这里补成 <库>:*，
		// 否则生成出只有两段的策略，授权静默失效。
		scope := NormalizeScope(g.Scope)
		if scope == "" {
			// 非法 scope 直接跳过，绝不能拼进策略：
			//   - 空串会拼出 ds:<id>:（两段），匹配不到任何四段对象，
			//     等于一条永远无效的授权；
			//   - "*" 会拼出 ds:<id>:*:*，等于本数据源全库全表，
			//     远超「申请一个库」的本意。
			// 跳过而不是报错：一条脏数据不该让整个策略重建失败、
			// 进而导致所有用户都没权限。跳过是 fail-closed 的安全方向。
			skipped++
			continue
		}
		object := fmt.Sprintf("ds:%d:%s", g.DatasourceID, scope)
		if _, err := e.e.AddPolicy(fmt.Sprintf("u:%d", g.UserID), object, g.Action); err != nil {
			return fmt.Errorf("add grant policy: %w", err)
		}
	}
	if skipped > 0 {
		// 只记日志不打断：调用方无法处理这种脏数据，
		// 但运维需要知道「有 N 条授权因为格式非法被跳过」。
		log.Printf("WARN: %d 条授权因 scope 格式非法被跳过（请在 grant_record 中修正）", skipped)
	}
	return nil
}

// SyncRoles 重建角色绑定，数据来源是 app_user.role。
// 调用顺序必须是先 SyncRoles 再 SyncGrants，否则 g 段为空，角色权限不生效。
func (e *Enforcer) SyncRoles(userRoles map[int64]string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

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
