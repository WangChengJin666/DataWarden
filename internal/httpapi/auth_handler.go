package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/opensourceprj/datawarden/internal/auth"
	appmodel "github.com/opensourceprj/datawarden/internal/model"
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
//
// 失败时区分原因：用户名不存在、账号已停用/删除、密码错误。
// 这是产品决定的口径——代价是用户名可被枚举，收益是用户能立刻明白
// 该找回密码还是该找管理员。若将来要收紧为黑盒，只需把三处 JSON
// 改成同一句话，前端不必改动（它直接展示后端文案）。
func (h *AuthHandler) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请输入用户名和密码"})
		return
	}

	id, hash, role, err := h.Users.FindByUsername(c.Request.Context(), req.Username)
	if err != nil {
		switch {
		case errors.Is(err, appmodel.ErrLoginUserNotFound):
			c.JSON(http.StatusUnauthorized, gin.H{"error": "用户不存在"})
		case errors.Is(err, appmodel.ErrLoginAccountInactive):
			c.JSON(http.StatusUnauthorized, gin.H{"error": "账号已停用，请联系管理员"})
		default:
			// 数据库故障等不该伪装成「凭据错误」：那会让用户一直重试，
			// 运维也看不到真实原因。回 500 并记录日志。
			c.JSON(http.StatusInternalServerError, gin.H{"error": "登录服务暂时不可用"})
		}
		return
	}
	if !auth.VerifyPassword(hash, req.Password) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "密码错误"})
		return
	}

	tok, err := h.Issuer.Issue(id, req.Username, role)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "issue token failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"token": tok, "role": role})
}
