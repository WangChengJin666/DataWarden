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
}

// NewRouter 注册全部路由。公开路由只有登录。
func NewRouter(d Deps) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())

	v1 := r.Group("/api/v1")
	v1.POST("/login", d.AuthHandler.Login)

	authed := v1.Group("")
	authed.Use(AuthMiddleware(d.Issuer))
	{
		authed.GET("/datasources", d.Datasource.List)
		authed.POST("/datasources", d.Datasource.Create)
		authed.POST("/query/sql", d.Query.ExecuteSQL)
	}
	return r
}
