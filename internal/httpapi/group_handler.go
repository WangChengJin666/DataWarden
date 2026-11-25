package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/opensourceprj/datawarden/internal/model"
)

// maxGroupNameLen 是组名长度上限。
//
// 设上限是为了让列表界面的列宽可预期，也顺手挡住把整段说明粘进名字里。
// 64 足够容纳「数据平台-华东研发中心-实时计算组」这种长链组织名。
const maxGroupNameLen = 64

// GroupStore 是组管理的持久化接口，由 store.GroupStore 实现。
type GroupStore interface {
	List(ctx context.Context) ([]model.UserGroup, error)
	Create(ctx context.Context, g *model.UserGroup) (int64, error)
	Update(ctx context.Context, id int64, name, description string) error
	Delete(ctx context.Context, id int64) error
}

// GroupHandler 提供用户组的增删改查。仅 superadmin 可用（见路由中间件）。
//
// 组只做归类：删组不删人（组员自动变未分组），因此这里不需要
// 任何「组里还有人，禁止删除」的拦截——那会逼管理员先把人一个个挪走，
// 而「删部门」这个动作的合理结果本来就是「这些人不属于任何部门」。
type GroupHandler struct {
	Store GroupStore
}

type groupReq struct {
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
}

// validate 校验组名，返回空串表示通过。
func (r groupReq) validate() string {
	name := strings.TrimSpace(r.Name)
	if name == "" {
		return "组名不能为空"
	}
	if len([]rune(name)) > maxGroupNameLen {
		return "组名不能超过 64 个字符"
	}
	return ""
}

// List 返回全部组（含成员数）。
func (h *GroupHandler) List(c *gin.Context) {
	items, err := h.Store.List(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// Create 新建组。
func (h *GroupHandler) Create(c *gin.Context) {
	uid, username, ok := requireSuperAdmin(c)
	if !ok {
		return
	}
	var req groupReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if msg := req.validate(); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msg})
		return
	}

	name := strings.TrimSpace(req.Name)
	id, err := h.Store.Create(c.Request.Context(), &model.UserGroup{
		Name:        name,
		Description: strings.TrimSpace(req.Description),
	})
	if err != nil {
		writeStoreError(c, err)
		return
	}
	auditAccountEvent(c, uid, username, "group.create", "group:"+itoaInt64(id), map[string]any{
		"name": name,
	})
	c.JSON(http.StatusOK, gin.H{"id": id})
}

// Update 改名与改描述。
func (h *GroupHandler) Update(c *gin.Context) {
	uid, username, ok := requireSuperAdmin(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req groupReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if msg := req.validate(); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msg})
		return
	}

	name := strings.TrimSpace(req.Name)
	if err := h.Store.Update(c.Request.Context(), id, name, strings.TrimSpace(req.Description)); err != nil {
		writeStoreError(c, err)
		return
	}
	auditAccountEvent(c, uid, username, "group.update", "group:"+itoaInt64(id), map[string]any{
		"name": name,
	})
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// Delete 删除组。组员会被数据库外键置为「未分组」，不会删除。
func (h *GroupHandler) Delete(c *gin.Context) {
	uid, username, ok := requireSuperAdmin(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.Store.Delete(c.Request.Context(), id); err != nil {
		writeStoreError(c, err)
		return
	}
	auditAccountEvent(c, uid, username, "group.delete", "group:"+itoaInt64(id), nil)
	c.JSON(http.StatusOK, gin.H{"status": "deleted", "members_detached": true})
}
