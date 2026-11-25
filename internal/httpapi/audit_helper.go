package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/opensourceprj/datawarden/internal/querysvc"
	"github.com/opensourceprj/datawarden/internal/store"
)

// EventRecorder 是 HTTP 层写审计的最小接口，复用 querysvc.Auditor。
type EventRecorder interface {
	Record(ctx context.Context, e querysvc.AuditEvent) error
}

// auditDeps 存放审计依赖，由路由装配时注入。
// 用包级变量是为了避免每个 handler 都多一个字段；
// 只在 NewRouter 里赋值一次，运行期只读。
var auditDeps EventRecorder

// SetAuditor 由 NewRouter 调用，注入审计实现。
func SetAuditor(a EventRecorder) { auditDeps = a }

// auditEvent 记录一次业务审计。审计失败不阻断请求，只影响可观测性。
func auditEvent(c *gin.Context, actorID int64, actorName, action string, datasourceID int64, detail map[string]any) {
	if auditDeps == nil {
		return
	}
	if detail == nil {
		detail = map[string]any{}
	}
	object := "system"
	if datasourceID > 0 {
		object = "datasource:" + itoaInt64(datasourceID)
	}
	_ = auditDeps.Record(c.Request.Context(), querysvc.AuditEvent{
		ActorID:   actorID,
		ActorName: actorName,
		Action:    action,
		Object:    object,
		Detail:    detail,
		IP:        c.ClientIP(),
		UserAgent: c.Request.UserAgent(),
	})
}

func itoaInt64(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// parseID 从路径参数取正整数 ID。失败时已写入 400 响应。
func parseID(c *gin.Context, name string) (int64, bool) {
	raw := c.Param(name)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid " + name})
		return 0, false
	}
	return id, true
}

// parseQueryID 解析查询参数里的正整数 id。失败时已写入 400 响应。
//
// 与 parseID 分开：parseID 只认路径参数（名字固定），查询参数需要
// 自带原始字符串，而且错误提示要带上参数名，否则前端看到
// 「invalid id」不知道是哪个参数错了。
func parseQueryID(c *gin.Context, name, raw string) (int64, bool) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid " + name})
		return 0, false
	}
	return id, true
}

// auditAccountEvent 记录账号体系（用户 / 组）的审计事件。
//
// 与 auditEvent 分开是因为后者把 object 钉死成 datasource:<id>，
// 而这里要记 group:<id> / user:<id>。硬塞进旧函数会让它变成
// 「猜对象类型」的函数，比多几行代码更糟。
func auditAccountEvent(c *gin.Context, actorID int64, actorName, action, object string, detail map[string]any) {
	if auditDeps == nil {
		return
	}
	if detail == nil {
		detail = map[string]any{}
	}
	_ = auditDeps.Record(c.Request.Context(), querysvc.AuditEvent{
		ActorID:   actorID,
		ActorName: actorName,
		Action:    action,
		Object:    object,
		Detail:    detail,
		IP:        c.ClientIP(),
		UserAgent: c.Request.UserAgent(),
	})
}

// writeStoreError 把 store 层的哨兵错误映射成 HTTP 状态码。
//
// 集中在这一处映射，避免每个 handler 各写一遍 switch 后逐渐分叉
// （有人把「组不存在」映射成 400、有人映射成 404，前端就没法统一处理了）。
func writeStoreError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, store.ErrGroupNotFound), errors.Is(err, store.ErrUserNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, store.ErrGroupNameTaken), errors.Is(err, store.ErrUsernameTaken):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case errors.Is(err, store.ErrUserDeleted),
		errors.Is(err, store.ErrLastSuperAdmin),
		errors.Is(err, store.ErrGroupMissing):
		// 这三个都是「请求可以理解但当前状态不允许」，
		// 用 409 而不是 400：400 意味着参数写错了，前端不该提供一个重试路径。
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
}
