package httpapi

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/opensourceprj/datawarden/internal/adapter"
	"github.com/opensourceprj/datawarden/internal/model"
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
// Tables 是选填的，缺失时的语义按角色分流：
//
//   - 管理角色（dba / superadmin）不需要授权，也就不需要平台替它做逐表
//     鉴权——他们的策略对象是 ds:*:*:*（见 authz.BuiltinPolicyObject），
//     任何表都放行。所以这里直接补一个"库:*"占位对象再过 service：
//     鉴权结果与「选了全部表」完全等价，但审计里能看出实际执行的是整库范围。
//
//   - 开发者仍然是必填的，缺表时由 querysvc 返回 ErrNoTables（那里是
//     唯一校验点，因为批处理 / CLI 等内部调用方也走同一个 service）。
//     注意不能在这里就把请求拒掉，否则会绕开 service 的审计与统一错误语义。
//
// 为什么要在这里兜一层：Casbin 的空清单语义是「无条件放行」，而
// querysvc 对空清单是一律拒绝。两边都不认「管理角色整库查询」这个需求，
// 直接调 executeSQL 不传 tables 的结果就是管理员被 400 拦住，
// 而前端下拉里明明写着「选填，管理角色可不填」。
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

	uid, username, role := currentUser(c)
	tables := req.Tables
	if len(tables) == 0 && model.Role(role).IsPrivileged() {
		tables = []string{"*"}
	}

	res, err := h.Service.ExecuteSQL(c.Request.Context(), querysvc.SQLRequest{
		Subject:      SubjectOf(uid),
		ActorID:      uid,
		ActorName:    username,
		DatasourceID: req.DatasourceID,
		Database:     req.Database,
		Tables:       tables,
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
			// 只有开发者会走到这里（管理角色的空清单已在上面补成整库）。
			// 提示里直接说明「为什么必须选表」，否则用户会以为平台故意卡他。
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "请先选择本次查询涉及的表（平台需要逐表校验权限）",
			})
		case errors.Is(err, querysvc.ErrNotReadOnly):
			c.JSON(http.StatusBadRequest, gin.H{"error": "only read-only statements are allowed"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}
	c.JSON(http.StatusOK, res)
}
