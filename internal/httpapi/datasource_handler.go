package httpapi

import (
	"context"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/opensourceprj/datawarden/internal/model"
)

// DatasourceStore 是数据源持久化的最小接口。
type DatasourceStore interface {
	List(ctx context.Context) ([]model.Datasource, error)
	Get(ctx context.Context, id int64) (*model.Datasource, error)
	Create(ctx context.Context, ds *model.Datasource) (int64, error)
}

type DatasourceHandler struct {
	Store DatasourceStore
}

func (h *DatasourceHandler) List(c *gin.Context) {
	items, err := h.Store.List(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

type createDatasourceRequest struct {
	Name      string `json:"name" binding:"required"`
	Type      string `json:"type" binding:"required"`
	Host      string `json:"host" binding:"required"`
	Port      int    `json:"port" binding:"required"`
	Database  string `json:"database"`
	SecretRef string `json:"secret_ref" binding:"required"`
	Env       string `json:"env"`
}

// validTypes 限制可登记的数据源类型，避免脏数据进入 Adapter 构造。
var validTypes = map[string]bool{
	string(model.TypeMySQL):      true,
	string(model.TypePostgreSQL): true,
	string(model.TypeRedis):      true,
	string(model.TypeMongoDB):    true,
}

// Create 登记数据源，仅 dba / superadmin 可操作。
func (h *DatasourceHandler) Create(c *gin.Context) {
	_, _, role := currentUser(c)
	if role != string(model.RoleDBA) && role != string(model.RoleSuperAdmin) {
		c.JSON(http.StatusForbidden, gin.H{"error": "only dba can manage datasources"})
		return
	}

	var req createDatasourceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !validTypes[req.Type] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported datasource type: " + req.Type})
		return
	}

	ds := &model.Datasource{
		Name:      req.Name,
		Type:      model.DatasourceType(req.Type),
		Host:      req.Host,
		Port:      req.Port,
		Database:  req.Database,
		SecretRef: req.SecretRef,
		Env:       req.Env,
	}
	if ds.Env == "" {
		ds.Env = "prod"
	}

	id, err := h.Store.Create(c.Request.Context(), ds)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": id})
}

// parseID 从路径参数取正整数 ID。
func parseID(c *gin.Context, name string) (int64, bool) {
	raw := c.Param(name)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid " + name})
		return 0, false
	}
	return id, true
}
