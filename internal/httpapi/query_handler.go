package httpapi

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/opensourceprj/datawarden/internal/adapter"
	"github.com/opensourceprj/datawarden/internal/querysvc"
)

// AdapterFactory 按数据源 ID 构造 Adapter。
type AdapterFactory func(datasourceID int64) (adapter.Adapter, error)

type QueryHandler struct {
	Service *querysvc.Service
	Factory AdapterFactory
}

type sqlQueryRequest struct {
	DatasourceID int64  `json:"datasource_id" binding:"required"`
	Database     string `json:"database" binding:"required"`
	// Tables 是本次查询涉及的表，仅用于审计追溯，不参与鉴权。
	// 授权粒度是「数据源 + 库」，因此前端不必再让用户勾表。
	// 字段保留是为了兼容旧客户端与审计明细，传了就记下，没传也合法。
	Tables   []string `json:"tables"`
	SQL      string   `json:"sql" binding:"required"`
	RowLimit int      `json:"row_limit"`
}

// ExecuteSQL 执行受控只读查询。
//
// 鉴权粒度是「数据源 + 库」：只要对该库有 read 权限，库下所有表都能查
// （见 querysvc.ExecuteSQL）。handler 不再需要按角色补表清单——
// 这里唯一的输入就是 datasource_id + database + sql。
//
// Tables 保留原样透传，只进审计，不影响放行与拒绝。
func (h *QueryHandler) ExecuteSQL(c *gin.Context) {
	if h.Service == nil || h.Factory == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "query service not configured"})
		return
	}

	var req sqlQueryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ad, err := h.Factory(req.DatasourceID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	uid, username, _ := currentUser(c)
	res, err := h.Service.ExecuteSQL(c.Request.Context(), querysvc.SQLRequest{
		Subject:      SubjectOf(uid),
		ActorID:      uid,
		ActorName:    username,
		DatasourceID: req.DatasourceID,
		Database:     req.Database,
		Tables:       req.Tables,
		SQL:          req.SQL,
		RowLimit:     req.RowLimit,
		IP:           c.ClientIP(),
		UserAgent:    c.Request.UserAgent(),
	}, ad)

	if err != nil {
		switch {
		case errors.Is(err, querysvc.ErrForbidden):
			// 不提示具体缺什么权限：库级的拒绝已经足够明确，
			// 再说细就等于告诉用户「这个数据源上有哪些库」
			c.JSON(http.StatusForbidden, gin.H{"error": "no permission on this database"})
		case errors.Is(err, querysvc.ErrNoDatabase):
			// 前端有「先选库」的前置校验，正常流程走不到这里；
			// 出现说明是直接调接口，提示要能指导对方补什么参数。
			c.JSON(http.StatusBadRequest, gin.H{"error": "请先选择数据库（database 字段必填）"})
		case errors.Is(err, querysvc.ErrNotReadOnly):
			c.JSON(http.StatusBadRequest, gin.H{"error": "only read-only statements are allowed"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}
	c.JSON(http.StatusOK, res)
}
