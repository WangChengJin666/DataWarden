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
	ID           int64
	Username     string
	Email        string
	PasswordHash string
	Role         Role
	Status       string
	CreatedAt    time.Time
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
	ID        int64
	Name      string
	Type      DatasourceType
	Host      string
	Port      int
	Database  string
	SecretRef string // 指向密钥管理，绝不存明文密码
	Env       string
	CreatedAt time.Time
}

// Grant 是生效中的数据源查询授权。Casbin 策略由此表重新生成。
type Grant struct {
	ID           int64
	UserID       int64
	DatasourceID int64
	Scope        string // 形如 "db:*" 或 "db:tbl1,db:tbl2"
	Action       string // 目前只支持 read
	ExpiresAt    time.Time
	RevokedAt    *time.Time
	CreatedAt    time.Time
}

type AuditLog struct {
	ID        int64
	ActorID   int64
	ActorName string
	Action    string
	Object    string
	Detail    []byte // JSON
	IP        string
	UserAgent string
	CreatedAt time.Time
}
