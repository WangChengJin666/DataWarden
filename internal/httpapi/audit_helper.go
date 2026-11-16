package httpapi

import (
	"context"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/opensourceprj/datawarden/internal/querysvc"
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
