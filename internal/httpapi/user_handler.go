package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/opensourceprj/datawarden/internal/auth"
	"github.com/opensourceprj/datawarden/internal/model"
	"github.com/opensourceprj/datawarden/internal/store"
)

// generatedPasswordLen 是初始密码长度。
//
// 12 位、字符表 57 个符号，约 70 bit 熵；配合 bcrypt 存储，
// 离线爆破不可行。再长的代价是人工转交时抄错。
const generatedPasswordLen = 12

// maxUsernameLen 是登录名长度上限，与迁移里 TEXT 的实际使用范围配合，
// 同时给界面列宽一个可预期的上界。
const maxUsernameLen = 64

// UserStore 是账号管理的持久化接口，由 store.UserStore 实现。
type UserStore interface {
	List(ctx context.Context, f store.UserFilter) ([]model.UserView, error)
	Get(ctx context.Context, id int64) (*model.UserView, error)
	Create(ctx context.Context, u *model.User) (int64, error)
	UpdateProfile(ctx context.Context, id int64, email string, role model.Role, groupID *int64) error
	SetStatus(ctx context.Context, id int64, status model.UserStatus) error
	ResetPassword(ctx context.Context, id int64, passwordHash string) error
	SoftDelete(ctx context.Context, id int64) error
}

// UserHandler 提供账号管理。仅 superadmin 可用（见路由中间件与 requireSuperAdmin）。
type UserHandler struct {
	Store UserStore
	// OnUserChange 在角色 / 状态 / 删除变化后触发，用于重建 Casbin 策略。
	//
	// 为什么必须回调：Casbin 策略在内存里，由 AllRoles（只取 active）重建。
	// 停用或删除一个用户后若不立刻重建，他手里已生效的授权还会继续放行，
	// 直到下一次 60 秒的定时刷新——对「刚把人删掉」的人来说这是不可接受的窗口。
	OnUserChange func(ctx context.Context)
}

// validUsername 校验登录名，返回空串表示通过。
//
// 字符集与 authz.validScopeSegment 的松紧一致（字母数字 + _ - .）：
// 用户名会进审计与日志，允许 # % 空格之类的字符会让「grep 一下谁干的」
// 变成一件要靠转义的正则活；而且 # 是逻辑删除改写用户名时用的分隔符，
// 必须保留给它专用，否则 split_part 还原原名时会切错。
func validUsername(name string) string {
	if name == "" {
		return "用户名不能为空"
	}
	if len([]rune(name)) > maxUsernameLen {
		return "用户名不能超过 64 个字符"
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == '.':
		default:
			return "用户名只能包含字母、数字与 _ - ."
		}
	}
	return ""
}

type createUserReq struct {
	Username string `json:"username" binding:"required"`
	Email    string `json:"email"`
	Role     string `json:"role" binding:"required"`
	Status   string `json:"status"`
	GroupID  *int64 `json:"group_id"`
}

type updateUserReq struct {
	Email   string `json:"email"`
	Role    string `json:"role" binding:"required"`
	GroupID *int64 `json:"group_id"`
}

// normalizeGroupID 把 0 视作「未分组」。
//
// 前端下拉的「未分组」选项值常常是 0（Select 的空值约定），
// 直接拿去查组会得到「组 0 不存在」，让用户看到一句莫名其妙的报错。
func normalizeGroupID(id *int64) *int64 {
	if id == nil || *id == 0 {
		return nil
	}
	return id
}

// List 返回用户列表。查询参数：keyword / role / status / group_id / ungrouped。
func (h *UserHandler) List(c *gin.Context) {
	if _, _, ok := requireSuperAdmin(c); !ok {
		return
	}

	filter := store.UserFilter{
		Keyword: strings.TrimSpace(c.Query("keyword")),
		Role:    strings.TrimSpace(c.Query("role")),
	}

	if role := filter.Role; role != "" && !model.Role(role).Valid() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid role: " + role})
		return
	}

	// status 允许逗号分隔多值（如 active,disabled），逐个校验。
	if raw := strings.TrimSpace(c.Query("status")); raw != "" {
		for _, s := range strings.Split(raw, ",") {
			s = strings.TrimSpace(s)
			if !model.UserStatus(s).Valid() {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid status: " + s})
				return
			}
			filter.Statuses = append(filter.Statuses, s)
		}
	}

	if raw := strings.TrimSpace(c.Query("group_id")); raw != "" {
		id, ok := parseQueryID(c, "group_id", raw)
		if !ok {
			return
		}
		filter.GroupID = &id
	}
	if c.Query("ungrouped") == "1" || c.Query("ungrouped") == "true" {
		filter.Ungrouped = true
	}

	items, err := h.Store.List(c.Request.Context(), filter)
	if err != nil {
		writeStoreError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// Create 新建用户，密码由服务端随机生成并只在响应里返回一次。
//
// 为什么不让管理员指定初始密码：指定密码意味着密码会经过管理员的脑子里、
// 聊天记录里，而「同一个人给五个新人设同一个初始密码」是很常见的。
// 随机生成 + 一次性展示把这段风险压到最小，代价只是必须当场转交。
func (h *UserHandler) Create(c *gin.Context) {
	uid, actor, ok := requireSuperAdmin(c)
	if !ok {
		return
	}
	var req createUserReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	username := strings.TrimSpace(req.Username)
	if msg := validUsername(username); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msg})
		return
	}
	role := model.Role(req.Role)
	if !role.Valid() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid role: " + req.Role})
		return
	}
	status := model.UserStatus(req.Status)
	if status == "" {
		status = model.StatusActive
	}
	if !status.Assignable() {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "status must be active or disabled; deleted is only produced by deletion",
		})
		return
	}

	plain, err := auth.GeneratePassword(generatedPasswordLen)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "generate password failed"})
		return
	}
	hash, err := auth.HashPassword(plain)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "hash password failed"})
		return
	}

	groupID := normalizeGroupID(req.GroupID)
	id, err := h.Store.Create(c.Request.Context(), &model.User{
		Username:     username,
		Email:        strings.TrimSpace(req.Email),
		PasswordHash: hash,
		Role:         role,
		Status:       status,
		GroupID:      groupID,
	})
	if err != nil {
		writeStoreError(c, err)
		return
	}

	// 审计里绝不写密码：审计日志是长期留存、只读的，
	// 一条明文初始密码留在那里等于把账号永久交出去。
	auditAccountEvent(c, uid, actor, "user.create", "user:"+itoaInt64(id), map[string]any{
		"username": username,
		"role":     string(role),
		"status":   string(status),
	})
	h.refresh(c.Request.Context())

	c.JSON(http.StatusOK, gin.H{"id": id, "password": plain})
}

// Update 改邮箱、角色、所属组。用户名不可改。
func (h *UserHandler) Update(c *gin.Context) {
	uid, actor, ok := requireSuperAdmin(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req updateUserReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	role := model.Role(req.Role)
	if !role.Valid() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid role: " + req.Role})
		return
	}

	groupID := normalizeGroupID(req.GroupID)
	if err := h.Store.UpdateProfile(c.Request.Context(), id, strings.TrimSpace(req.Email), role, groupID); err != nil {
		writeStoreError(c, err)
		return
	}
	auditAccountEvent(c, uid, actor, "user.update", "user:"+itoaInt64(id), map[string]any{
		"role": string(role),
	})
	// 角色变了，Casbin 的 g 段必须重建，否则新角色要到 60 秒后才生效。
	h.refresh(c.Request.Context())
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// SetStatus 启用 / 停用账号。
func (h *UserHandler) SetStatus(c *gin.Context) {
	uid, actor, ok := requireSuperAdmin(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req struct {
		Status string `json:"status" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	status := model.UserStatus(req.Status)
	// 这里显式拦 deleted：删除走 DELETE 接口（它还会改写用户名、清邮箱），
	// 从这条路径设成 deleted 会造出「状态是删除但用户名还占着」的半成品。
	if !status.Assignable() {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "status must be active or disabled; deleted is only produced by deletion",
		})
		return
	}

	// 不允许停用自己：停用后这个 token 立刻查不了数据（Casbin 已无此人），
	// 而请求本身已经通过鉴权、会「成功」执行——操作者当场被自己锁在门外，
	// 还得找另一个超管来救。这与「最后一名超管」的检查是两件事：
	// 有多个超管时停用自己完全合法，但一样会把自己关在外面。
	if id == uid && status == model.StatusDisabled {
		c.JSON(http.StatusConflict, gin.H{"error": "cannot disable your own account"})
		return
	}

	if err := h.Store.SetStatus(c.Request.Context(), id, status); err != nil {
		writeStoreError(c, err)
		return
	}
	auditAccountEvent(c, uid, actor, "user.status", "user:"+itoaInt64(id), map[string]any{
		"status": string(status),
	})
	// 停用后必须立刻重建策略：否则他手里的授权还会继续放行到下次定时刷新。
	h.refresh(c.Request.Context())
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// ResetPassword 重置密码，新密码只在响应里返回一次。
func (h *UserHandler) ResetPassword(c *gin.Context) {
	uid, actor, ok := requireSuperAdmin(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	plain, err := auth.GeneratePassword(generatedPasswordLen)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "generate password failed"})
		return
	}
	hash, err := auth.HashPassword(plain)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "hash password failed"})
		return
	}

	if err := h.Store.ResetPassword(c.Request.Context(), id, hash); err != nil {
		writeStoreError(c, err)
		return
	}
	// 审计只记「重置了谁的密码」，不记密码本身。
	auditAccountEvent(c, uid, actor, "user.reset_password", "user:"+itoaInt64(id), nil)
	c.JSON(http.StatusOK, gin.H{"password": plain})
}

// Delete 逻辑删除账号。
//
// 接口方法仍然是 HTTP DELETE，但语义是「标记删除 + 释放用户名」，
// 不是物理删除：授权与审批记录都挂在这个 user_id 上，删行等于篡改审计口径。
// 这层语义必须写在接口注释里，否则运维在库里查到这行还在会以为删除失败。
func (h *UserHandler) Delete(c *gin.Context) {
	uid, actor, ok := requireSuperAdmin(c)
	if !ok {
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	// 不允许删自己：删除后这个账号立刻失去所有权限，
	// 但请求已经通过鉴权，操作会「成功」，而操作者随即被锁在门外。
	if id == uid {
		c.JSON(http.StatusConflict, gin.H{"error": "cannot delete your own account"})
		return
	}

	if err := h.Store.SoftDelete(c.Request.Context(), id); err != nil {
		writeStoreError(c, err)
		return
	}
	auditAccountEvent(c, uid, actor, "user.delete", "user:"+itoaInt64(id), nil)
	h.refresh(c.Request.Context())
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

// refresh 触发策略重建。OnUserChange 为 nil 时（测试装配）静默跳过。
func (h *UserHandler) refresh(ctx context.Context) {
	if h.OnUserChange != nil {
		h.OnUserChange(ctx)
	}
}
