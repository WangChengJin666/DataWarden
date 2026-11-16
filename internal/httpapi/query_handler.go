package httpapi

import (
	"errors"
	"net/http"
	"strconv"

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
	Tables       []string `json:"tables" binding:"required"`
	SQL          string   `json:"sql" binding:"required"`
	RowLimit     int      `json:"row_limit"`
}

// ExecuteSQL 执行受控只读查询。
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
		Subject:      "u:" + strconv.FormatInt(uid, 10),
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
