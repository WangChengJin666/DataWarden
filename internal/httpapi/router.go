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
	Query       *QueryHandler
	GrantReq    *GrantRequestHandler
	Grant       *GrantHandler
	Metadata    *MetadataHandler
	Redis       *RedisHandler
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

		// 元数据：查询页的库 / 表下拉与编辑器补全都依赖它。
		// 不限制为 DBA——开发者本来就要选库选表，返回内容已按授权过滤。
		if d.Metadata != nil {
			authed.GET("/datasources/:id/databases", d.Metadata.ListDatabases)
			authed.GET("/datasources/:id/tables", d.Metadata.ListTables)
			authed.GET("/datasources/:id/columns", d.Metadata.Columns)
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
			// 「我能查哪些数据源」：查询页的下拉用这个，不能给开发者开
			// GET /datasources（那条限 DBA 且含 host/port/username）。
			authed.GET("/grants/mine", d.Grant.ListMine)
		}
	}
	return r
}
