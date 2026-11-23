package httpapi

import (
	"context"
	"net/http"
	"strings"

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
	// Tester 用于「测试连接」。为 nil 时该接口返回 503。
	Tester ConnTester
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
//
// 编辑弹窗需要把连接密码直接显示在密码框里，所以要返回明文。
//
// 为什么可以返回：
//   - 路由层已限定只有 DBA / superadmin 能访问，和「谁能改这个数据源」完全一致；
//     拿得到明文的人本来就有权改掉它，掩码并不会让权限更小
//   - 平台本身就把密码明文存在元数据库里（见 model.Datasource.Password 的说明），
//     该数据库的读取者早就能看到明文
//
// 为什么只在这个接口返回：
//   - List 一次返回全部数据源，回显密码等于一次响应带走全库凭据
//   - 编辑只需要一条，因此只有详情接口回显
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
	// 明文不进 Password 字段（它永久 json:"-"），走单独的序列化字段，
	// 这样「哪些接口会吐密码」在类型上就能一眼看出来。
	c.JSON(http.StatusOK, ds.WithPlainPassword())
}

type applyScopeReq struct {
	Database string   `json:"database"`
	Tables   []string `json:"tables"`
}

type datasourceReq struct {
	Name         string            `json:"name" binding:"required"`
	Type         string            `json:"type" binding:"required"`
	Host         string            `json:"host" binding:"required"`
	Port         int               `json:"port" binding:"required"`
	Database     string            `json:"database"`
	Username     string            `json:"username"`
	Password     string            `json:"password"`
	AuthSource   string            `json:"auth_source"`
	Options      map[string]string `json:"options"`
	ApplyEnabled bool              `json:"apply_enabled"`
	MaxApplyDays int               `json:"max_apply_days"`
	ApplyScopes  []applyScopeReq   `json:"apply_scopes"`
	Description  string            `json:"description"`
}

// validTypes 限制可登记的数据源类型，避免脏数据进入 Adapter 构造。
var validTypes = map[string]bool{
	string(model.TypeMySQL):      true,
	string(model.TypePostgreSQL): true,
	string(model.TypeRedis):      true,
	string(model.TypeMongoDB):    true,
}

// toModel 把请求转成模型。密码按明文写入。
func (r datasourceReq) toModel() *model.Datasource {
	scopes := make([]model.ApplyScope, 0, len(r.ApplyScopes))
	for _, s := range r.ApplyScopes {
		scopes = append(scopes, model.ApplyScope{
			Database: s.Database,
			Tables:   s.Tables,
		})
	}

	return &model.Datasource{
		Name:         r.Name,
		Type:         model.DatasourceType(r.Type),
		Host:         r.Host,
		Port:         r.Port,
		Database:     r.Database,
		Username:     r.Username,
		Password:     r.Password,
		AuthSource:   r.AuthSource,
		Options:      r.Options,
		ApplyEnabled: r.ApplyEnabled,
		MaxApplyDays: r.MaxApplyDays,
		ApplyScopes:  scopes,
		Description:  r.Description,
	}
}

// validateDatasource 按数据源类型校验必填项，返回空串表示通过。
//
// 为什么不能只靠前端表单校验：表单是唯一的守卫时，任何绕过前端的调用
// （脚本、旧版前端、改包）都能存进一条必然连不上的数据源，
// 而错误要到「用户点查询」时才暴露，且报的是驱动层的含糊错误。
//
// 各类型的必填项不同（见 README「各类型对连接字段的要求」）：
//   - PostgreSQL：库名必填，它是 DSN 的 dbname
//   - MongoDB：默认库必填，否则不知道要查哪个库
//   - MySQL：库名可空（连接但不选库，查询时写 db.table）
//   - Redis：库名是 db 号，可空（默认 0）
func validateDatasource(ds *model.Datasource) string {
	switch ds.Type {
	case model.TypePostgreSQL:
		if strings.TrimSpace(ds.Database) == "" {
			return "PostgreSQL 必须填写库名：它是连接串的 dbname，留空无法建立连接"
		}
	case model.TypeMongoDB:
		if strings.TrimSpace(ds.Database) == "" {
			return "MongoDB 必须填写默认库，否则无法确定要查哪个库"
		}
	}

	// 有密码必须有账号（MongoDB 的 SCRAM 认证需要用户名；
	// Redis 例外——它可以是纯 requirepass 密码认证）
	if strings.TrimSpace(ds.Password) != "" && strings.TrimSpace(ds.Username) == "" &&
		ds.Type != model.TypeRedis {
		return "填写了连接密码，但未填连接账号"
	}
	return ""
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

	if msg := validateDatasource(ds); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msg})
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

	// 与 Create 同一套校验。注意 Password 留空表示"不修改"，
	// 此时不能因为 ds.Password 为空就跳过账号检查——
	// 但也不能因密码为空而报错（那是合法的"不改密码"）。
	// validateDatasource 只在「有密码但无账号」时报错，留空密码不影响。
	if msg := validateDatasource(ds); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msg})
		return
	}

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
