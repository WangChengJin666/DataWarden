package httpapi

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/opensourceprj/datawarden/internal/auth"
	"github.com/opensourceprj/datawarden/internal/model"
)

// context key，避免字符串字面量散落各处。
const (
	ctxUserID   = "uid"
	ctxUsername = "usr"
	ctxRole     = "role"
)

// AuthMiddleware 校验 Bearer Token 并把身份放进 context。
func AuthMiddleware(issuer *auth.TokenIssuer) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.GetHeader("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing bearer token"})
			return
		}
		raw := strings.TrimPrefix(h, "Bearer ")
		claims, err := issuer.Parse(raw)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			return
		}
		c.Set(ctxUserID, claims.UserID)
		c.Set(ctxUsername, claims.Username)
		c.Set(ctxRole, claims.Role)
		c.Next()
	}
}

// requireDBAMiddleware 在路由层拦截非 DBA 请求，避免「前端藏了按钮、接口仍然全开」。
//
// 原本权限校验只写在各个 handler 里，List / Get 两个读接口漏掉了。
// 把它们收到路由层统一拦截，比逐个 handler 里记得写 requireDBA 更不容易漏。
// handler 内仍保留 requireDBA 校验：路由是唯一入口时才安全，
// 将来有人把 handler 挂到别的 group 上，也不会意外放开。
func requireDBAMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		_, _, role := currentUser(c)
		if role != string(model.RoleDBA) && role != string(model.RoleSuperAdmin) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "forbidden: only dba can manage datasources",
			})
			return
		}
		c.Next()
	}
}

// requireSuperAdminMiddleware 在路由层拦下非超级管理员的账号管理请求。
//
// 为什么账号管理不走 Casbin 而直接判角色：
// Casbin 的策略主体来自 app_user.role（SyncRoles 每次重建），
// 而账号管理本身就在改 app_user。用被管理的数据判断「谁有权管理它」，
// 一旦策略重建那一步出问题（清空 p 段、漏调 reAddBuiltinPolicies），
// 就会出现「所有人都进不了用户管理」——连修账号的入口都没了。
// 角色判定只是一次字符串比较，加一层中间件足够，且不引入依赖。
//
// 也不给 dba 开放：dba 已经能改数据源、能审批，再给账号管理，
// 他可以直接把自己提成 superadmin，角色分级就失效了。
func requireSuperAdminMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		_, _, role := currentUser(c)
		if role != string(model.RoleSuperAdmin) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "forbidden: only superadmin can manage accounts",
			})
			return
		}
		c.Next()
	}
}

// requireSuperAdmin 是 handler 内的第二道校验，与 requireDBA 同样的写法。
//
// 路由层已经拦过一次，这里再来一次是为了防止将来有人把 handler
// 挂到别的 group 上——那样路由层的保护就悄悄失效了，而 handler
// 内的判断仍然有效。返回 false 时已写入响应。
func requireSuperAdmin(c *gin.Context) (int64, string, bool) {
	uid, username, role := currentUser(c)
	if role != string(model.RoleSuperAdmin) {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "forbidden: only superadmin can manage accounts",
		})
		return 0, "", false
	}
	return uid, username, true
}

// currentUser 取出中间件写入的身份。未鉴权路由调用时返回零值。
func currentUser(c *gin.Context) (int64, string, string) {
	var id int64
	if v, ok := c.Get(ctxUserID); ok {
		if n, ok := v.(int64); ok {
			id = n
		}
	}
	var username, role string
	if v, ok := c.Get(ctxUsername); ok {
		if s, ok := v.(string); ok {
			username = s
		}
	}
	if v, ok := c.Get(ctxRole); ok {
		if s, ok := v.(string); ok {
			role = s
		}
	}
	return id, username, role
}
