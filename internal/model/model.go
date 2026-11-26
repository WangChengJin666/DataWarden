package model

import (
	"errors"
	"time"
)

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

// Valid 判断角色值是否属于内置角色。
//
// 账号管理接口必须用它校验入参：role 会直接进 SyncRoles 生成 g 段绑定，
// 接受任意字符串就可能出现一个叫 "*" 或空串的角色。空串在 SyncRoles 里
// 会被跳过（表现为「设了角色却没有权限」），而 "*" 之类的值会安静地留在
// 策略表里，排查时没人会想到去看一个不存在的角色名。
func (r Role) Valid() bool {
	switch r {
	case RoleDeveloper, RoleDBA, RoleSuperAdmin:
		return true
	}
	return false
}

type User struct {
	ID           int64      `json:"id"`
	Username     string     `json:"username"`
	Email        string     `json:"email"`
	PasswordHash string     `json:"-"` // 绝不序列化到响应
	Role         Role       `json:"role"`
	Status       UserStatus `json:"status"`
	// GroupID 为 nil 表示未分组。用指针而不是 0：
	// 0 会被当成「组 id 是 0」这种不存在的组，而 nil 明确表示「没有归属」。
	GroupID   *int64    `json:"group_id"`
	CreatedAt time.Time `json:"created_at"`
}

// UserStatus 是账号状态。用字符串类型而不是裸 string，
// 是为了让「哪些值是合法的」有一个统一入口，不必在每个 handler 里重复 switch。
type UserStatus string

const (
	StatusActive   UserStatus = "active"
	StatusDisabled UserStatus = "disabled"
	// StatusDeleted 是逻辑删除标记：行保留、授权与单据保留、用户名被改写。
	//
	// 为什么用 status 的一个取值，而不是新增 deleted_at 列：
	// 除了登录链路，所有身份查询（AllRoles、List、Get）都写成
	// status = 'active' 或显式指定状态集合，新增一个取值天然被排除。
	// 若新增独立列，每条查询都要补 deleted_at IS NULL，漏一处就是
	// 「已删账号还能登录、还能查数据」，而且不会有任何报错提示。
	//
	// 登录的 FindByUsername 是例外：它要区分「用户不存在」与「账号已停用」，
	// 因此改为按用户名查、再判断状态是否 active，不再依赖 SQL 过滤。
	StatusDeleted UserStatus = "deleted"
)

// Valid 判断状态值是否合法（含 deleted，用于读取侧校验）。
func (s UserStatus) Valid() bool {
	switch s {
	case StatusActive, StatusDisabled, StatusDeleted:
		return true
	}
	return false
}

// Assignable 判断该状态能否由「新建用户」或「修改状态」接口写入。
//
// deleted 只能由删除接口产生：如果允许调用方直接设成 deleted，
// 「删除」就退化成一个名字很吓人的停用，而它其实还改写了用户名、不可恢复。
func (s UserStatus) Assignable() bool {
	return s == StatusActive || s == StatusDisabled
}

// UserGroup 是用户所属的组，语义等同部门。
//
// 组只做归类，不承载任何数据权限：授权仍由「用户角色 + 个人授权」决定。
// 若哪天要在组上挂授权，必须同时改 querysvc 的鉴权与 SyncGrants，
// 否则会出现「组里加人却不生效」的静默错误。
type UserGroup struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// MemberCount 是该组下的用户数（含已停用，不含已删除）。
	// 列表页要显示「几人」，单独发一个计数请求会变成 N+1。
	MemberCount int       `json:"member_count"`
	CreatedAt   time.Time `json:"created_at"`
}

// UserView 是用户管理的列表 / 详情视图：在 User 之上补组名。
//
// 不直接复用 User：User 是登录链路用的模型，给它塞联表结果会让
// 「登录时多查一次组」这种无意义的开销渗进去。列表还要展示组名，
// 只有 group_id 没法用。
type UserView struct {
	ID        int64      `json:"id"`
	Username  string     `json:"username"`
	Email     string     `json:"email"`
	Role      Role       `json:"role"`
	Status    UserStatus `json:"status"`
	GroupID   *int64     `json:"group_id"`
	GroupName string     `json:"group_name"`
	CreatedAt time.Time  `json:"created_at"`
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
//
// Scope 现取值两种：authz.ScopeAllDatabases（"*"，整个数据源，
// 申请审批通过后落这种）以及历史的 "<库>:*"（改口径前审批产生的）。
// 两者都能被 NormalizeScope 归一，鉴权侧不需要感知差异。
type Grant struct {
	ID           int64      `json:"id"`
	UserID       int64      `json:"user_id"`
	DatasourceID int64      `json:"datasource_id"`
	Scope        string     `json:"scope"`
	Action       string     `json:"action"` // 目前只支持 read
	// CanExport 表示这条授权是否允许把查询结果导出成 Excel。
	//
	// 它跟着「单条授权」走，而不是跟着用户或组走：同一个人完全可能
	// A 数据源批了导出、B 数据源没批，那是两次独立审批各自的决定。
	// 导出**不进 Casbin**（act 段只有 read），判定在 httpapi 的导出接口；
	// 理由见迁移 0010 的注释。
	CanExport bool       `json:"can_export"`
	ExpiresAt time.Time  `json:"expires_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
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
	// CanExport 见 Grant.CanExport：该用户在这个数据源上能否导出查询结果。
	CanExport bool      `json:"can_export"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

// ScopeView 是授权范围的结构化形态，供前端直接渲染权限提示。
//
// 为什么不在 DatasourceBrief 里直接放原始 scope 字符串：
// 原始形态有 "*"、"shop"、"shop:*"、"shop:orders,users" 几种写法，
// 前端要自己解析才能显示出「整个数据源 / 整个 shop 库 / 指定表」——
// 一旦解析逻辑写错，就会重演「整库授权显示成一张叫 * 的表」那类问题。
// 后端已经知道语义，就在这里一次性拆好。
type ScopeView struct {
	// Database 为 "*" 表示**整个数据源**（所有库、所有表），
	// 对应审批通过时落的 scope。其它取值都是具体的库名。
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
	// CanExport 只是**展示层的提示**：这个数据源上是否存在「批了导出」的授权。
	//
	// 放在这里是为了让查询页能提前把按钮藏起来，不必让用户点一次
	// 才收到 403。但它**不是授权判定**，真正的闸门是
	// store.CanExportDatabase（按库逐条匹配）。
	//
	// 两者为什么必须分开：一个数据源下有 shop、crm 两个库，
	// shop 那条批了导出、crm 那条没批，按数据源聚合只能得到 true，
	// 拿它放行就等于把 shop 的许可借给了 crm。这个字段只回答
	// "要不要显示按钮"，用户切到 crm 后点导出，后端会按库拒绝。
	//
	// 取值口径：
	//   - 开发者：该数据源上存在任一生效且 can_export=true 的授权（取「或」）；
	//   - 管理角色（dba / superadmin）恒为 true，与免授权查询同一口径。
	CanExport bool `json:"can_export"`
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
//
// Scope 不再由申请人填写：申请粒度就是数据源本身，
// 落库固定为 authz.ScopeAllDatabases（整个数据源的库与表）。
type GrantRequest struct {
	ID             int64              `json:"id"`
	UserID         int64              `json:"user_id"`
	Username       string             `json:"username,omitempty"` // 列表展示用，非表字段
	DatasourceID   int64              `json:"datasource_id"`
	DatasourceName string             `json:"datasource_name,omitempty"`
	Scope          string             `json:"scope"`
	Days           int                `json:"days"`
	Reason         string             `json:"reason"`
	// CanExport 是申请人的诉求：是否要求把查询结果导出成 Excel。
	//
	// 只是留痕，实际权限由审批通过时复制到 grant_record.can_export 的那份决定。
	// 不直接生效是刻意的：否则申请人在审批前改一下勾选就等于自己给自己
	// 加了导出，审批人所看到的与最终生效的就不是同一件事。
	CanExport      bool               `json:"can_export"`
	Status         GrantRequestStatus `json:"status"`
	ReviewerID     *int64             `json:"reviewer_id,omitempty"`
	ReviewComment  string             `json:"review_comment"`
	ReviewedAt     *time.Time         `json:"reviewed_at,omitempty"`
	GrantID        *int64             `json:"grant_id,omitempty"`
	CreatedAt      time.Time          `json:"created_at"`
}

// 登录失败的分类错误。
//
// 放在 model 而不是 store：httpapi 通过接口依赖数据层，不能反过来 import store
// （会成环），而它又必须能区分这几种失败给出不同提示。model 是两边都已依赖的
// 唯一公共位置，错误语义本身也确实是领域概念。
//
// 这里有意的取舍：允许用户名枚举。产品要求「密码错了就说是密码错」，
// 而不是笼统回「invalid credentials」——后者让停用账号的用户对着正确密码
// 反复重试，也分不清是记错密码还是账号被停。
var (
	// ErrLoginUserNotFound 表示用户名不存在。
	ErrLoginUserNotFound = errors.New("login user not found")
	// ErrLoginAccountInactive 表示用户名存在但账号已停用或已删除。
	ErrLoginAccountInactive = errors.New("login account is inactive")
	// ErrWrongPassword 表示用户名存在、账号可用，但密码不匹配。
	//
	// 由 handler 在 bcrypt 校验失败时返回，不由 store 产生：
	// store 只负责取哈希，密码学上的判定属于 auth/handler 层。
	ErrWrongPassword = errors.New("wrong password")
)
