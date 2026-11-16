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
