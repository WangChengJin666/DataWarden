package httpapi

import (
	"github.com/gin-gonic/gin"
	"github.com/opensourceprj/datawarden/internal/auth"
)

// Deps 汇总路由所需依赖。
type Deps struct {
	Issuer      *auth.TokenIssuer
	AuthHandler *AuthHandler
	Datasource  *DatasourceHandler
	// Query 负责 SQL / Mongo / Redis 查询；Export 负责把结果导出成 Excel，
	// 两者共用同一个 querysvc.Service（导出多一道权限闸门）。
	Query  *QueryHandler
	Export *ExportHandler
	GrantReq    *GrantRequestHandler
	Grant       *GrantHandler
	Metadata    *MetadataHandler
	Redis       *RedisHandler
	// Group / User 是账号管理（组与用户），仅 superadmin 可用
	Group *GroupHandler
	User  *UserHandler
	// SelfPassword 是「修改自己的密码」，所有已登录角色都可用，不挂角色中间件
	SelfPassword *SelfPasswordHandler
	// Auditor 用于记录数据源管理与审批动作，可为 nil
	Auditor EventRecorder
}

// NewRouter 注册全部路由。公开路由只有登录。
func NewRouter(d Deps) *gin.Engine {
	if d.Auditor != nil {
		SetAuditor(d.Auditor)
	}

	r := gin.New()
	r.Use(gin.Recovery())

	v1 := r.Group("/api/v1")
	v1.POST("/login", d.AuthHandler.Login)

	authed := v1.Group("")
	authed.Use(AuthMiddleware(d.Issuer))
	{
		// 数据源管理（仅 DBA / superadmin）
		//
		// List 也必须限权：返回体里有主机、端口、库名、连接账号，
		// 这些是内网拓扑和账号信息，对开发者没有用途（他们用「申请授权」页）。
		// 页面文案写的是「仅 DBA 可管理」，接口就不能对开发者放行，
		// 否则绕开前端直接调 API 就能看到全部数据源清单。
		authed.GET("/datasources", requireDBAMiddleware(), d.Datasource.List)
		// 测试连接：不写库，只验证配置能否连上。
		// 必须放在 /datasources/:id 之前，避免 test 被当成 id 匹配（写在这里更直观）。
		authed.POST("/datasources/test", requireDBAMiddleware(), d.Datasource.TestConnection)
		// Get 会给 DBA 回显密码掩码，绝不能让开发者拿到
		authed.GET("/datasources/:id", requireDBAMiddleware(), d.Datasource.Get)
		authed.POST("/datasources", requireDBAMiddleware(), d.Datasource.Create)
		authed.PUT("/datasources/:id", requireDBAMiddleware(), d.Datasource.Update)
		authed.DELETE("/datasources/:id", requireDBAMiddleware(), d.Datasource.Delete)

		// 查询
		authed.POST("/query/sql", d.Query.ExecuteSQL)
		authed.POST("/query/mongo", d.Query.ExecuteMongo)
		if d.Redis != nil {
			authed.POST("/query/redis", d.Redis.Query)
		}
		// 导出：单独的权限闸门（授权记录上的 can_export），
		// 不是「能查就能导」——导出是数据外带，必须由审批单独批过。
		if d.Export != nil {
			authed.POST("/query/export", d.Export.Export)
		}

		// 元数据：查询页的库 / 表下拉与编辑器补全都依赖它。
		// 不限制为 DBA——开发者本来就要选库选表，返回内容已按授权过滤。
		if d.Metadata != nil {
			authed.GET("/datasources/:id/databases", d.Metadata.ListDatabases)
			authed.GET("/datasources/:id/tables", d.Metadata.ListTables)
			authed.GET("/datasources/:id/columns", d.Metadata.Columns)
			// 整库表结构：SQL 编辑器解析出别名后要据此提示 `别名.` 的字段。
			// 逐表调 /columns 一次输入会打出十几个请求，跟不上打字。
			authed.GET("/datasources/:id/schema", d.Metadata.Schema)
		}

		// 授权申请
		if d.GrantReq != nil {
			authed.GET("/grant/applyable", d.GrantReq.ListApplyable)
			authed.POST("/grant/requests", d.GrantReq.Create)
			authed.GET("/grant/requests/mine", d.GrantReq.ListMine)
			authed.GET("/grant/requests", d.GrantReq.ListPending)
			authed.POST("/grant/requests/:id/approve", d.GrantReq.Approve)
			authed.POST("/grant/requests/:id/reject", d.GrantReq.Reject)
			authed.POST("/grant/requests/:id/cancel", d.GrantReq.Cancel)
		}

		// 已生效授权的查看与回收（仅 DBA / superadmin）
		if d.Grant != nil {
			authed.GET("/grants", requireDBAMiddleware(), d.Grant.List)
			authed.POST("/grants/:id/revoke", requireDBAMiddleware(), d.Grant.Revoke)
			// 开关某条授权的导出能力。不改 Casbin 策略，
			// 导出判定在 /query/export 里现查 grant_record.can_export，改完即时生效。
			authed.POST("/grants/:id/export", requireDBAMiddleware(), d.Grant.SetExport)
			// 「我能查哪些数据源」：查询页的下拉用这个，不能给开发者开
			// GET /datasources（那条限 DBA 且含 host/port/username）。
			authed.GET("/grants/mine", d.Grant.ListMine)
		}

		// 账号管理：组与用户。仅 superadmin。
		//
		// 逐个路由挂中间件而不是用一个子 group：/datasources 那一片也是
		// 逐个挂 requireDBAMiddleware，保持一致；子 group 少写几行，
		// 但会让「这条路由到底受不受保护」需要往上翻一层才能确认。
		if d.Group != nil {
			authed.GET("/groups", requireSuperAdminMiddleware(), d.Group.List)
			authed.POST("/groups", requireSuperAdminMiddleware(), d.Group.Create)
			authed.PUT("/groups/:id", requireSuperAdminMiddleware(), d.Group.Update)
			authed.DELETE("/groups/:id", requireSuperAdminMiddleware(), d.Group.Delete)
		}
		if d.User != nil {
			authed.GET("/users", requireSuperAdminMiddleware(), d.User.List)
			authed.POST("/users", requireSuperAdminMiddleware(), d.User.Create)
			authed.PUT("/users/:id", requireSuperAdminMiddleware(), d.User.Update)
			authed.DELETE("/users/:id", requireSuperAdminMiddleware(), d.User.Delete)
			authed.POST("/users/:id/status", requireSuperAdminMiddleware(), d.User.SetStatus)
			authed.POST("/users/:id/reset-password", requireSuperAdminMiddleware(), d.User.ResetPassword)
		}

		// 修改自己的密码：任何已登录角色都能用，**不要**挂角色中间件。
		// 路径放在 /me 下而不是 /users/:id/password，是为了让「只能改自己」
		// 在 URL 上就成立——带 id 的路径总有人会试着传别人的 id。
		if d.SelfPassword != nil {
			authed.POST("/me/password", d.SelfPassword.ChangeOwnPassword)
		}
	}
	return r
}
