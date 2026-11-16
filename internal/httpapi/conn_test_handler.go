package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// ConnTester 用给定配置实际连一次目标库，用于表单里的「测试连接」。
//
// 它不写库、不产生授权、不影响任何持久化状态，只回答「这套配置能否连上」。
type ConnTester interface {
	Test(ctx context.Context, cfg TestConnConfig) (*TestConnResult, error)
}

// TestConnConfig 是测试连接所需的全部信息。
type TestConnConfig struct {
	Type     string
	Host     string
	Port     int
	Database string
	User     string
	Password string
}

// TestConnResult 是测试连接的结果，回传给前端展示。
type TestConnResult struct {
	OK        bool   `json:"ok"`
	Message   string `json:"message"`
	ElapsedMs int64  `json:"elapsed_ms"`
}

type testConnReq struct {
	Type     string `json:"type" binding:"required"`
	Host     string `json:"host" binding:"required"`
	Port     int    `json:"port" binding:"required"`
	Database string `json:"database"`
	Username string `json:"username"`
	Password string `json:"password"`
	// DatasourceID 非 0 时表示编辑现有数据源：
	// 密码留空则用库里已存的密码来测，不必让用户重新输一遍。
	DatasourceID int64 `json:"datasource_id"`
}

// TestConnection 测试数据源连接，仅 dba / superadmin 可用。
//
// 两类调用：
//   - 新增：前端传完整配置 + 明文密码
//   - 编辑：前端传配置，密码留空时用库里已存的密码解密后测试
func (h *DatasourceHandler) TestConnection(c *gin.Context) {
	uid, username, ok := requireDBA(c)
	if !ok {
		return
	}

	var req testConnReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !validTypes[req.Type] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported datasource type: " + req.Type})
		return
	}
	if h.Tester == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "connection test not configured"})
		return
	}

	// 密码解析顺序：请求里填了就用请求的；没填则回退到库里已存的密文。
	password := req.Password
	if password == "" && req.DatasourceID != 0 {
		if h.Cipher == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "password cipher not configured"})
			return
		}
		existing, err := h.Store.Get(c.Request.Context(), req.DatasourceID)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		if existing.PasswordEnc != "" {
			plain, err := h.Cipher.Decrypt(existing.PasswordEnc)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{
					"error": "cannot decrypt stored password: " + err.Error(),
				})
				return
			}
			password = plain
		}
	}

	// 测试连接也要限时，避免目标不可达时前端一直转圈
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	res, err := h.Tester.Test(ctx, TestConnConfig{
		Type:     req.Type,
		Host:     req.Host,
		Port:     req.Port,
		Database: req.Database,
		User:     req.Username,
		Password: password,
	})

	detail := map[string]any{
		"type": req.Type,
		"host": req.Host,
		"port": req.Port,
		"ok":   err == nil && res != nil && res.OK,
	}
	if err != nil {
		detail["error"] = err.Error()
	}
	auditEvent(c, uid, username, "datasource.test", 0, detail)

	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"ok":      false,
			"message": friendlyConnError(err),
		})
		return
	}
	c.JSON(http.StatusOK, res)
}

// friendlyConnError 把驱动错误转成便于用户理解的一句话。
// 沿用原始错误会泄露内部细节（主机、用户名），但也可能太含糊，
// 所以这里做分类后附加可读说明。
func friendlyConnError(err error) string {
	msg := err.Error()
	lower := strings.ToLower(msg)

	switch {
	// MongoDB 的 Unauthorized 表示连上了但没有执行该命令的权限，
	// 与「密码错」不是一回事：密码错会报 Authentication failed。
	// 分开提示，否则用户会一直去改密码却找不到原因。
	case strings.Contains(lower, "requires authentication"),
		strings.Contains(lower, "(unauthorized)"):
		return "权限不足：目标库已开启认证，请填写有查询权限的账号"
	case strings.Contains(lower, "password authentication failed"),
		strings.Contains(lower, "access denied"),
		strings.Contains(lower, "authentication"),
		strings.Contains(lower, "wrongpass"),
		strings.Contains(lower, "invalid username"),
		strings.Contains(lower, "noauth"):
		return "认证失败，请检查账号和密码"
	case strings.Contains(lower, "timeout"),
		strings.Contains(lower, "deadline exceeded"),
		strings.Contains(lower, "i/o timeout"):
		return "连接超时，请检查主机、端口和网络连通性"
	case strings.Contains(lower, "connection refused"),
		strings.Contains(lower, "no route to host"),
		strings.Contains(lower, "cannot connect"),
		// Windows 下的拒绝连接文案，与 Unix 的 "connection refused" 不同
		strings.Contains(lower, "no connection could be made"),
		strings.Contains(lower, "actively refused"),
		strings.Contains(lower, "dial error"):
		return "无法连接到目标主机，请检查主机和端口"
	case strings.Contains(lower, "does not exist"),
		strings.Contains(lower, "unknown database"),
		strings.Contains(lower, "invalid database"):
		return "库不存在，请检查库名（Redis 请填 db 号）"
	case errors.Is(err, context.DeadlineExceeded):
		return "连接超时"
	default:
		return "连接失败：" + msg
	}
}
