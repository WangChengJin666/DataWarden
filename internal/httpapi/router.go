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
		// 数据源管理（DBA）
		authed.GET("/datasources", d.Datasource.List)
		// 测试连接：不写库，只验证配置能否连上。
		// 必须放在 /datasources/:id 之前，避免 test 被当成 id 匹配（写在这里更直观）。
		authed.POST("/datasources/test", d.Datasource.TestConnection)
		authed.GET("/datasources/:id", d.Datasource.Get)
		authed.POST("/datasources", d.Datasource.Create)
		authed.PUT("/datasources/:id", d.Datasource.Update)
		authed.DELETE("/datasources/:id", d.Datasource.Delete)

		// 查询
		authed.POST("/query/sql", d.Query.ExecuteSQL)
		authed.POST("/query/mongo", d.Query.ExecuteMongo)
		if d.Redis != nil {
			authed.POST("/query/redis", d.Redis.Query)
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
	}
	return r
}
