package model

import "time"

// Role 是用户角色。审批权限绑定在 dba 上，不引入独立流程引擎。
type Role string

const (
	RoleDeveloper  Role = "developer"
	RoleDBA        Role = "dba"
	RoleSuperAdmin Role = "superadmin"
)

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
type ApplyScope struct {
	Database string   `json:"database"`
	Tables   []string `json:"tables"` // 为空表示该库下全部表
}

type Datasource struct {
	ID        int64          `json:"id"`
	Name      string         `json:"name"`
	Type      DatasourceType `json:"type"`
	Host      string         `json:"host"`
	Port      int            `json:"port"`
	Database  string         `json:"database"`
	SecretRef string         `json:"secret_ref"` // 指向密钥管理，绝不存明文密码
	Env       string         `json:"env"`
	CreatedAt time.Time      `json:"created_at"`

	// 可申请配置。管理员决定该数据源是否对开发者开放申请。
	ApplyEnabled bool         `json:"apply_enabled"`
	MaxApplyDays int          `json:"max_apply_days"`
	ApplyScopes  []ApplyScope `json:"apply_scopes"`
	Description  string       `json:"description"`
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
