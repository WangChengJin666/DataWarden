package httpapi

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/opensourceprj/datawarden/internal/model"
)

// DatasourceStore 是数据源持久化的最小接口。
type DatasourceStore interface {
	List(ctx context.Context) ([]model.Datasource, error)
	Get(ctx context.Context, id int64) (*model.Datasource, error)
	Create(ctx context.Context, ds *model.Datasource) (int64, error)
	Update(ctx context.Context, ds *model.Datasource) error
	Delete(ctx context.Context, id int64) error
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

// Get 返回单个数据源详情。
func (h *DatasourceHandler) Get(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	ds, err := h.Store.Get(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, ds)
}

type applyScopeReq struct {
	Database string   `json:"database"`
	Tables   []string `json:"tables"`
}

type datasourceReq struct {
	Name         string          `json:"name" binding:"required"`
	Type         string          `json:"type" binding:"required"`
	Host         string          `json:"host" binding:"required"`
	Port         int             `json:"port" binding:"required"`
	Database     string          `json:"database"`
	SecretRef    string          `json:"secret_ref" binding:"required"`
	Env          string          `json:"env"`
	ApplyEnabled bool            `json:"apply_enabled"`
	MaxApplyDays int             `json:"max_apply_days"`
	ApplyScopes  []applyScopeReq `json:"apply_scopes"`
	Description  string          `json:"description"`
}

// validTypes 限制可登记的数据源类型，避免脏数据进入 Adapter 构造。
var validTypes = map[string]bool{
	string(model.TypeMySQL):      true,
	string(model.TypePostgreSQL): true,
	string(model.TypeRedis):      true,
	string(model.TypeMongoDB):    true,
}

func (r datasourceReq) toModel() *model.Datasource {
	scopes := make([]model.ApplyScope, 0, len(r.ApplyScopes))
	for _, s := range r.ApplyScopes {
		scopes = append(scopes, model.ApplyScope{
			Database: s.Database,
			Tables:   s.Tables,
		})
	}
	env := r.Env
	if env == "" {
		env = "prod"
	}
	return &model.Datasource{
		Name:         r.Name,
		Type:         model.DatasourceType(r.Type),
		Host:         r.Host,
		Port:         r.Port,
		Database:     r.Database,
		SecretRef:    r.SecretRef,
		Env:          env,
		ApplyEnabled: r.ApplyEnabled,
		MaxApplyDays: r.MaxApplyDays,
		ApplyScopes:  scopes,
		Description:  r.Description,
	}
}

// requireDBA 校验当前用户是否为 DBA 及以上。返回 false 时已写入响应。
func requireDBA(c *gin.Context) (int64, string, bool) {
	uid, username, role := currentUser(c)
	if role != string(model.RoleDBA) && role != string(model.RoleSuperAdmin) {
		c.JSON(http.StatusForbidden, gin.H{"error": "only dba can manage datasources"})
		return 0, "", false
	}
	return uid, username, true
}

// Create 登记数据源，仅 dba / superadmin 可操作。
func (h *DatasourceHandler) Create(c *gin.Context) {
	uid, username, ok := requireDBA(c)
	if !ok {
		return
	}

	var req datasourceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !validTypes[req.Type] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported datasource type: " + req.Type})
		return
	}

	ds := req.toModel()
	id, err := h.Store.Create(c.Request.Context(), ds)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	auditEvent(c, uid, username, "datasource.create", id, map[string]any{
		"name":          ds.Name,
		"type":          req.Type,
		"apply_enabled": ds.ApplyEnabled,
	})
	c.JSON(http.StatusCreated, gin.H{"id": id})
}

// Update 修改数据源，含可申请配置。
func (h *DatasourceHandler) Update(c *gin.Context) {
	uid, username, ok := requireDBA(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	var req datasourceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !validTypes[req.Type] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported datasource type: " + req.Type})
		return
	}

	ds := req.toModel()
	ds.ID = id
	if err := h.Store.Update(c.Request.Context(), ds); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	auditEvent(c, uid, username, "datasource.update", id, map[string]any{
		"name":           ds.Name,
		"apply_enabled":  ds.ApplyEnabled,
		"max_apply_days": ds.MaxApplyDays,
	})
	c.JSON(http.StatusOK, gin.H{"id": id})
}

// Delete 删除数据源。若已被申请或授权引用，返回 409。
func (h *DatasourceHandler) Delete(c *gin.Context) {
	uid, username, ok := requireDBA(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	if err := h.Store.Delete(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	auditEvent(c, uid, username, "datasource.delete", id, nil)
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}
