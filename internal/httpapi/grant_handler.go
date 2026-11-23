package httpapi

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/opensourceprj/datawarden/internal/model"
)

// GrantStore 是授权记录的管理接口。
//
// 与 querysvc.Enforcer 分开：Enforcer 只回答「能不能」，这里要回答
// 「现在有谁在什么权限上」。吊销之后必须重建策略，否则内存里的旧策略还在。
type GrantStore interface {
	ListActive(ctx context.Context, limit int) ([]model.GrantView, error)
	Get(ctx context.Context, id int64) (*model.Grant, error)
	Revoke(ctx context.Context, id int64) error
	// MyDatasources 返回某用户当前有权查询的数据源（去重、仅生效中的授权）。
	MyDatasources(ctx context.Context, userID int64) ([]model.DatasourceBrief, error)
}

// GrantHandler 提供授权的查看与回收。
//
// 之前 grant_record 只能写（审批通过时插入），没有任何入口撤销：
// DBA 批错了只能连数据库改。审计平台自己留一个必须连库才能纠错的口子
// 是说不通的，所以补上这对接口。
type GrantHandler struct {
	Store GrantStore
	// OnRevoke 在吊销成功后触发策略重建。为 nil 时不刷新，
	// 此时吊销要等到下一次定时刷新才生效（有延迟，但不会永久失效）。
	OnRevoke func(ctx context.Context)
}

// ListMine 返回当前用户「可以查询」的数据源。
//
// 为什么需要这个接口：查询页原来直接调 GET /datasources 填下拉，
// 但那条路由限 DBA——开发者一进查询页就 403，下拉是空的，
// 于是什么都查不了。而开发者本来只该看到自己拿到授权的数据源，
// 不该看到全量清单（里面有主机、端口、账号）。
//
// 返回的是精简视图（不带 host/port/username），并附带该用户在这些
// 数据源上的授权 scope，前端可据此提示「你在这个库上有哪些表的权限」。
func (h *GrantHandler) ListMine(c *gin.Context) {
	uid, _, _ := currentUser(c)
	items, err := h.Store.MyDatasources(c.Request.Context(), uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// List 列出生效中的授权，仅 DBA / superadmin。
func (h *GrantHandler) List(c *gin.Context) {
	if _, _, ok := requireDBA(c); !ok {
		return
	}
	items, err := h.Store.ListActive(c.Request.Context(), 200)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// Revoke 立即收回一条授权，仅 DBA / superadmin。
//
// 收回后立刻重建 Casbin 策略，不等定时刷新：吊销是应急操作，
// 「等一分钟才生效」在这个场景下不可接受。
func (h *GrantHandler) Revoke(c *gin.Context) {
	uid, username, ok := requireDBA(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	g, err := h.Store.Get(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "grant not found"})
		return
	}
	if g.RevokedAt != nil {
		// 幂等：已撤销的再撤一次不算错，避免前端重复点击报错
		c.JSON(http.StatusOK, gin.H{"status": "already_revoked"})
		return
	}

	if err := h.Store.Revoke(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	auditEvent(c, uid, username, "grant.revoke", g.DatasourceID, map[string]any{
		"grant_id":      id,
		"target_user":   g.UserID,
		"scope":         g.Scope,
		"datasource_id": g.DatasourceID,
	})

	if h.OnRevoke != nil {
		h.OnRevoke(c.Request.Context())
	}
	c.JSON(http.StatusOK, gin.H{"status": "revoked"})
}
