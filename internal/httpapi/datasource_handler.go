package httpapi

import (
	"context"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/opensourceprj/datawarden/internal/model"
	"github.com/opensourceprj/datawarden/internal/secret"
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
	// Cipher 用于加密落库的密码。为 nil 时拒绝 Create/Update，
	// 避免明文密码被意外写入数据库。
	Cipher *secret.Cipher
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
	Username     string          `json:"username"`
	Password     string          `json:"password"`
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

// toModel 把请求转成模型。密码在此处加密，密文之外的明文不会离开这个函数。
func (r datasourceReq) toModel(cipher *secret.Cipher) (*model.Datasource, error) {
	scopes := make([]model.ApplyScope, 0, len(r.ApplyScopes))
	for _, s := range r.ApplyScopes {
		scopes = append(scopes, model.ApplyScope{
			Database: s.Database,
			Tables:   s.Tables,
		})
	}

	enc := ""
	if r.Password != "" {
		var err error
		enc, err = cipher.Encrypt(r.Password)
		if err != nil {
			return nil, fmt.Errorf("encrypt password: %w", err)
		}
	}

	return &model.Datasource{
		Name:         r.Name,
		Type:         model.DatasourceType(r.Type),
		Host:         r.Host,
		Port:         r.Port,
		Database:     r.Database,
		Username:     r.Username,
		PasswordEnc:  enc,
		ApplyEnabled: r.ApplyEnabled,
		MaxApplyDays: r.MaxApplyDays,
		ApplyScopes:  scopes,
		Description:  r.Description,
	}, nil
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
	if h.Cipher == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "password cipher not configured"})
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

	ds, err := req.toModel(h.Cipher)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	id, err := h.Store.Create(c.Request.Context(), ds)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	auditEvent(c, uid, username, "datasource.create", id, map[string]any{
		"name":          ds.Name,
		"type":          req.Type,
		"host":          ds.Host,
		"username":      ds.Username,
		"apply_enabled": ds.ApplyEnabled,
	})
	c.JSON(http.StatusCreated, gin.H{"id": id})
}

// Update 修改数据源，含可申请配置。密码留空表示不修改。
func (h *DatasourceHandler) Update(c *gin.Context) {
	uid, username, ok := requireDBA(c)
	if !ok {
		return
	}
	if h.Cipher == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "password cipher not configured"})
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

	ds, err := req.toModel(h.Cipher)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	ds.ID = id
	if err := h.Store.Update(c.Request.Context(), ds); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	auditEvent(c, uid, username, "datasource.update", id, map[string]any{
		"name":           ds.Name,
		"username":       ds.Username,
		"apply_enabled":  ds.ApplyEnabled,
		"max_apply_days": ds.MaxApplyDays,
		"password_reset": req.Password != "",
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
