package model

import "time"

// Role 是用户角色。审批权限绑定在 dba 上，不引入独立流程引擎。
type Role string

const (
	RoleDeveloper  Role = "developer"
	RoleDBA        Role = "dba"
	RoleSuperAdmin Role = "superadmin"
)

// IsPrivileged 判断该角色是否属于「平台管理侧」，即是否需要走授权申请流程。
//
// dba / superadmin 不参与授权：他们本身负责审批与回收授权，
// 再让他们给自己申请一条授权才能查数据，是把审批人放进了被审批的位置——
// 实际操作中只能靠「先批自己一条」绕过，等于流程空转。
//
// 所有判断「是不是管理角色」的地方都应该调这个函数，而不是各自
// 再写一遍 role == "dba" || role == "superadmin"：
// 漏写一处就会出现「下拉框看得到全量数据源、执行时却 403」
// 这种前后不一致的现象，而且很难从界面现象倒推出是哪个环节漏了。
func (r Role) IsPrivileged() bool {
	return r == RoleDBA || r == RoleSuperAdmin
}

type User struct {
	ID           int64     `json:"id"`
	Username     string    `json:"username"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"` // 绝不序列化到响应
	Role         Role      `json:"role"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
}

// DatasourceType 决定用哪个 Adapter 实现。
type DatasourceType string

const (
	TypeMySQL      DatasourceType = "mysql"
	TypePostgreSQL DatasourceType = "postgresql"
	TypeRedis      DatasourceType = "redis"
	TypeMongoDB    DatasourceType = "mongodb"
)

// ApplyScope 是数据源允许申请的一个库表范围。
//
// 已废弃（仅作结构保留）：数据源不再配置「允许申请的库表范围」，
// 登记时不填范围 = 该数据源下的库/表都可以申请。授权粒度是
// 「数据源 + 库」（Casbin object ds:<id>:<库>:*），表不参与鉴权，
// 逐库逐表的白名单既没人维护，也给不出比库级鉴权更强的保证。
//
// 前端与申请校验都已不再读写它；留着类型是为了不让旧接口
// 字段变成编译错误，新增代码不要再引用。
type ApplyScope struct {
	Database string   `json:"database"`
	Tables   []string `json:"tables"` // 为空表示该库下全部表
}

type Datasource struct {
	ID       int64          `json:"id"`
	Name     string         `json:"name"`
	Type     DatasourceType `json:"type"`
	Host     string         `json:"host"`
	Port     int            `json:"port"`
	Database string         `json:"database"`

	// 连接目标库使用的账号。每个数据源独立配置，便于按库分配最小权限账号。
	Username string `json:"username"`
	// AuthSource 仅 MongoDB 使用：账号所在的认证库。
	//
	// MongoDB 的账号归属于某个库：建在 admin 的是全局管理员，
	// 建在业务库的只对该库有效。连接时用错认证库会直接报 AuthenticationFailed，
	// 表现为「密码错误」，极难自查，所以必须显式配置。
	// 留空表示与 Database 相同（mongosh 的默认行为）。
	AuthSource string `json:"auth_source"`
	// Password 是明文密码，直接落库。
	//
	// 这里不做加密是有意的取舍：平台需要把明文交给驱动去连目标库，
	// 加密只能提升「数据库被单独拖走」这一种场景的安全性，
	// 挡不住拿到整机权限的人（密钥通常和数据库在同一台机器上）。
	// 因此改为如实明文存储，并把保护责任放到数据库访问控制与备份加密上。
	//
	// 本字段永不序列化（json:"-"）：默认任何接口都不吐密码。
	// 唯一回显明文的入口是 DBA 编辑数据源，走 WithPlainPassword() 填 PlainPassword。
	Password string `json:"-"`

	// Options 是连接高级参数，透传给驱动（由 adapter 做白名单校验）。
	//
	// MongoDB：replicaSet / tls / tlsCAFile / tlsInsecure / readPreference /
	//          directConnection / authMechanism / appName
	// PostgreSQL：sslmode（云实例基本强制 require）
	//
	// 用 map 而不是一组固定字段：不同库支持的参数差异大，穷举字段会让结构膨胀，
	// 而且每加一个参数都要改迁移。落 JSONB 后扩展不需要动表结构。
	Options map[string]string `json:"options"`

	CreatedAt time.Time `json:"created_at"`

	// 可申请配置。管理员决定该数据源是否对开发者开放申请。
	ApplyEnabled bool         `json:"apply_enabled"`
	MaxApplyDays int          `json:"max_apply_days"`
	ApplyScopes  []ApplyScope `json:"apply_scopes"`
	Description  string       `json:"description"`
	// HasPassword 供前端判断是否已设置密码。列表响应里只有这个布尔值。
	HasPassword bool `json:"has_password"`

	// PlainPassword 是给编辑弹窗回显的明文密码，只在详情接口
	// （DBA / superadmin 专用）由 WithPlainPassword() 填充。
	//
	// 用独立的序列化字段而不是直接放开 Password 的 json tag，
	// 是为了让「哪个接口会吐密码」在类型和代码搜索上都一目了然：
	// 只有调用 WithPlainPassword() 的地方会带出这个 key。
	PlainPassword *string `json:"password,omitempty"`
}

// WithPlainPassword 把明文密码放进响应字段，供 DBA 编辑数据源时回显。
//
// 前提是调用方已经确认过权限（路由层 requireDBAMiddleware）。
// 列表接口绝不能调用它：一次响应会把全部数据源的密码带到前端。
func (d Datasource) WithPlainPassword() Datasource {
	out := d
	if d.Password != "" {
		pw := d.Password
		out.PlainPassword = &pw
	}
	return out
}

// Grant 是生效中的数据源查询授权。Casbin 策略由此表重新生成。
type Grant struct {
	ID           int64      `json:"id"`
	UserID       int64      `json:"user_id"`
	DatasourceID int64      `json:"datasource_id"`
	Scope        string     `json:"scope"`  // 形如 "db:*" 或 "db:tbl1,db:tbl2"
	Action       string     `json:"action"` // 目前只支持 read
	ExpiresAt    time.Time  `json:"expires_at"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// GrantView 是授权的展示形态：在 Grant 基础上补上用户名与数据源名。
//
// 管理页要回答「现在谁能查哪个库」，只有 user_id / datasource_id 没法用，
// 所以单独建一个视图结构，而不是给 Grant 加可选字段——
// Grant 是落库模型，不该为了前端展示塞入联表结果。
type GrantView struct {
	ID             int64     `json:"id"`
	UserID         int64     `json:"user_id"`
	Username       string    `json:"username"`
	DatasourceID   int64     `json:"datasource_id"`
	DatasourceName string    `json:"datasource_name"`
	Scope          string    `json:"scope"`
	Action         string    `json:"action"`
	ExpiresAt      time.Time `json:"expires_at"`
	CreatedAt      time.Time `json:"created_at"`
}

// ScopeView 是授权范围的结构化形态，供前端直接渲染权限提示。
//
// 为什么不在 DatasourceBrief 里直接放原始 scope 字符串：
// 原始形态有 "shop"、"shop:*"、"shop:orders,users" 三种写法，
// 前端要自己解析才能显示出「整个 shop 库 / 指定表」——
// 一旦解析逻辑写错，就会重演「整库授权显示成一张叫 * 的表」那类问题。
// 后端已经知道语义，就在这里一次性拆好。
type ScopeView struct {
	Database string `json:"database"`
	// Tables 为 nil（或空数组）表示**整库授权**，与 "shop"、"shop:*" 两种写法等价。
	//
	// 这个 nil 语义是整个链路共用的隐式约定：
	//   store.parseScopes 合并同库多条授权时用它判断整库
	//   httpapi.splitScope 拆分时也依赖它
	//   httpapi.tablesFor 据此决定「走 ListTables 列全表」还是「只列授权表」
	// 因此绝不能把 nil 改成 ["*"]，否则界面上会多出一项叫 "*" 的表，
	// 且「整库」与「指定表清单」两种语义会被混在一起。
	Tables []string `json:"tables"`
}

// DatasourceBrief 是给「可查询的数据源」下拉用的精简视图。
//
// 为什么不能直接复用 Datasource：完整结构里有 host、port、username——
// 这些是内网拓扑与账号信息，只该给管理员看。开发者只需要知道
// 「哪个数据源我可以查、能查哪些库表」，所以单独定义一个不含敏感字段的结构。
type DatasourceBrief struct {
	ID          int64          `json:"id"`
	Name        string         `json:"name"`
	Type        DatasourceType `json:"type"`
	Description string         `json:"description"`
	// Database 是数据源配置的默认库，用于帮用户预填查询表单。
	// 对 Redis 而言它是 db 号。
	Database string `json:"database"`
	// Scopes 是该用户在这个数据源上的授权范围（已结构化）。
	Scopes []ScopeView `json:"scopes"`
}

type AuditLog struct {
	ID        int64     `json:"id"`
	ActorID   int64     `json:"actor_id"`
	ActorName string    `json:"actor_name"`
	Action    string    `json:"action"`
	Object    string    `json:"object"`
	Detail    []byte    `json:"detail"` // JSON
	IP        string    `json:"ip"`
	UserAgent string    `json:"user_agent"`
	CreatedAt time.Time `json:"created_at"`
}

// GrantRequestStatus 是申请单状态。
type GrantRequestStatus string

const (
	RequestPending  GrantRequestStatus = "pending"
	RequestApproved GrantRequestStatus = "approved"
	RequestRejected GrantRequestStatus = "rejected"
	RequestCanceled GrantRequestStatus = "canceled"
)

// GrantRequest 是开发者提交的数据源授权申请。
// 审批通过后生成一条 Grant（生效授权），并在本记录上回填 grant_id。
type GrantRequest struct {
	ID             int64              `json:"id"`
	UserID         int64              `json:"user_id"`
	Username       string             `json:"username,omitempty"` // 列表展示用，非表字段
	DatasourceID   int64              `json:"datasource_id"`
	DatasourceName string             `json:"datasource_name,omitempty"`
	Scope          string             `json:"scope"`
	Days           int                `json:"days"`
	Reason         string             `json:"reason"`
	Status         GrantRequestStatus `json:"status"`
	ReviewerID     *int64             `json:"reviewer_id,omitempty"`
	ReviewComment  string             `json:"review_comment"`
	ReviewedAt     *time.Time         `json:"reviewed_at,omitempty"`
	GrantID        *int64             `json:"grant_id,omitempty"`
	CreatedAt      time.Time          `json:"created_at"`
}
