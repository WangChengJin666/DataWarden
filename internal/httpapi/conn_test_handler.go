package httpapi

import (
	"context"
	"errors"
	"fmt"
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
	// AuthSource 仅 MongoDB 使用：账号所在的认证库，留空回落 admin。
	AuthSource string
	// Options 连接高级参数，必须与正式查询用同一套，否则测通了用不了。
	Options map[string]string
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
	// AuthSource 仅 MongoDB：账号所在的认证库，留空回落 admin。
	AuthSource string `json:"auth_source"`
	// Options 连接高级参数（MongoDB 的 replicaSet/tls、PG 的 sslmode 等）。
	Options map[string]string `json:"options"`
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

	// 密码解析顺序：请求里填了就用请求的；没填则回退到库里已存的值。
	password := req.Password
	if password == "" && req.DatasourceID != 0 {
		existing, err := h.Store.Get(c.Request.Context(), req.DatasourceID)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		password = existing.Password
	}

	// 测试连接也要限时，避免目标不可达时前端一直转圈
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	res, err := h.Tester.Test(ctx, TestConnConfig{
		Type:       req.Type,
		Host:       req.Host,
		Port:       req.Port,
		Database:   req.Database,
		User:       req.Username,
		Password:   password,
		AuthSource: req.AuthSource,
		Options:    req.Options,
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

	// 认证库的取值与 adapter 内部保持一致：显式填了就用它，否则回落 admin。
	// 这里复算一次，只为把「实际按哪个库认证」写进错误提示，
	// 不参与连接本身（连接由 adapter 决定）。
	effectiveAuthSource := req.AuthSource
	if req.Type == "mongodb" && effectiveAuthSource == "" {
		effectiveAuthSource = "admin"
	}

	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"ok":      false,
			"message": friendlyConnError(err, req.Type, effectiveAuthSource),
		})
		return
	}
	c.JSON(http.StatusOK, res)
}

// friendlyConnError 把驱动错误转成便于用户理解的一句话。
// 沿用原始错误会泄露内部细节（主机、用户名），但也可能太含糊，
// 所以这里做分类后附加可读说明。
//
// typ 用于补充类型相关的排查方向（目前只有 MongoDB 需要）；
// authSource 是该 MongoDB 数据源实际使用的认证库，用于在认证失败时
// 告诉用户「平台是按哪个库认证的」——否则用户只会看到「账号或密码错」，
// 而真正的问题往往是账号建在别的库里。
func friendlyConnError(err error, typ, authSource string) string {
	msg := err.Error()
	lower := strings.ToLower(msg)

	switch {
	// MongoDB 的 Unauthorized 表示连上了但没有执行该命令的权限，
	// 与「密码错」不是一回事：密码错会报 Authentication failed。
	// 分开提示，否则用户会一直去改密码却找不到原因。
	case strings.Contains(lower, "requires authentication"),
		strings.Contains(lower, "(unauthorized)"):
		if typ == "mongodb" {
			return fmt.Sprintf(
				"权限不足：账号能登录，但没有读目标库的权限（认证库「%s」）。请给该账号补上目标库的 read 权限",
				authSource)
		}
		return "权限不足：目标库已开启认证，请填写有查询权限的账号"
	case strings.Contains(lower, "password authentication failed"),
		strings.Contains(lower, "access denied"),
		strings.Contains(lower, "authentication"),
		strings.Contains(lower, "wrongpass"),
		strings.Contains(lower, "invalid username"),
		strings.Contains(lower, "noauth"):
		if typ == "mongodb" {
			// MongoDB 认证失败有三大常见原因，仅凭报错文本无法区分：
			//   1. 密码真的错
			//   2. 认证库选错（账号建在另一个库）
			//   3. 只有密码没有账号，或账号被 ACL 禁用
			// 把实际用的认证库写出来，用户对照账号创建时所在的库即可定位。
			if authSource == "admin" {
				return "认证失败：账号或密码不正确。" +
					"当前未指定认证库，平台按默认的 admin 认证（与 mongosh 一致）。" +
					"若该账号是建在业务库里的专属用户，请把「认证库」填成那个库名"
			}
			return fmt.Sprintf(
				"认证失败：账号或密码不正确，或账号不属于认证库「%s」。"+
					"请核对密码，并确认账号创建在哪个库",
				authSource)
		}
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
		// 不回显原文：未识别的驱动错误（TLS 握手失败、DNS 解析失败、
		// 驱动内部错误等）里常带 host、用户名、证书路径，而这些恰恰是
		// 本函数开头要挡掉的东西。之前这里拼了 "连接失败："+msg，
		// 等于前面 9 个分支做了脱敏、最后一个分支又把门打开了。
		//
		// 原文并未丢弃：调用方会把 err 写进审计 detail（见 TestConnection），
		// 管理员可以在审计日志里查到完整原因，用户界面只给排查方向。
		return "连接失败，请检查主机、端口、账号与网络连通性（详细原因见审计日志）"
	}
}
