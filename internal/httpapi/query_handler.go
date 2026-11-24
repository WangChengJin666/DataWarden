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
	DatasourceID int64    `json:"datasource_id" binding:"required"`
	Database     string   `json:"database" binding:"required"`
	Tables       []string `json:"tables"` // 选填：管理角色无需逐表鉴权，开发者缺表由 service 拒绝
	SQL          string   `json:"sql" binding:"required"`
	RowLimit     int      `json:"row_limit"`
}

// ExecuteSQL 执行受控只读查询。
//
// Tables 从必填改成选填：管理角色（dba / superadmin）不需要授权，
// 也就不需要平台替它做逐表鉴权——他们的授权对象是 ds:<id>:*:*
// （见 authz.BuiltinPolicies），任何表都放行。对开发者仍然是必填的，
// 缺表时由 querysvc 返回 ErrNoTables（那里是唯一校验点，
// 因为批处理 / CLI 等内部调用方也走同一个 service）。
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
			// 不提示具体是哪张表无权限，避免探知表存在性
			c.JSON(http.StatusForbidden, gin.H{"error": "no permission on one or more tables"})
		case errors.Is(err, querysvc.ErrNoTables):
			c.JSON(http.StatusBadRequest, gin.H{"error": "tables field is required for authorization"})
		case errors.Is(err, querysvc.ErrNotReadOnly):
			c.JSON(http.StatusBadRequest, gin.H{"error": "only read-only statements are allowed"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}
	c.JSON(http.StatusOK, res)
}
