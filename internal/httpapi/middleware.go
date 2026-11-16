package httpapi

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/opensourceprj/datawarden/internal/auth"
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
