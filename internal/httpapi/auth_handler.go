package httpapi

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/opensourceprj/datawarden/internal/auth"
)

// UserLookup 由数据层实现，按用户名取密码哈希与角色。
type UserLookup interface {
	FindByUsername(ctx context.Context, username string) (id int64, hash, role string, err error)
}

type AuthHandler struct {
	Users  UserLookup
	Issuer *auth.TokenIssuer
}

type loginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// Login 校验凭据并签发令牌。
// 用户不存在与密码错误返回同一个提示，避免用户名枚举。
func (h *AuthHandler) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "username and password are required"})
		return
	}

	id, hash, role, err := h.Users.FindByUsername(c.Request.Context(), req.Username)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}
	if !auth.VerifyPassword(hash, req.Password) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	tok, err := h.Issuer.Issue(id, req.Username, role)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "issue token failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"token": tok, "role": role})
}
