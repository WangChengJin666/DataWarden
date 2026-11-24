package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/opensourceprj/datawarden/internal/authz"
	"github.com/opensourceprj/datawarden/internal/model"
)

// GrantRequestStore 是申请单持久化接口。
type GrantRequestStore interface {
	Create(ctx context.Context, r *model.GrantRequest) (int64, error)
	Get(ctx context.Context, id int64) (*model.GrantRequest, error)
	ListByStatus(ctx context.Context, status string, limit int) ([]model.GrantRequest, error)
	ListByUser(ctx context.Context, userID int64, limit int) ([]model.GrantRequest, error)
	Approve(ctx context.Context, reqID, reviewerID int64, comment string) (*model.Grant, error)
	Reject(ctx context.Context, reqID, reviewerID int64, comment string) error
	Cancel(ctx context.Context, reqID, userID int64) error
}

// ApplyableDatasourceLister 列出开放申请的数据源。
type ApplyableDatasourceLister interface {
	ListApplyable(ctx context.Context) ([]model.Datasource, error)
	Get(ctx context.Context, id int64) (*model.Datasource, error)
}

type GrantRequestHandler struct {
	Requests  GrantRequestStore
	Datasrc   ApplyableDatasourceLister
	OnApprove func(ctx context.Context, g *model.Grant) // 审批通过后刷新权限缓存
}

type createGrantRequestReq struct {
	DatasourceID int64  `json:"datasource_id" binding:"required"`
	Scope        string `json:"scope" binding:"required"`
	Days         int    `json:"days" binding:"required"`
	Reason       string `json:"reason" binding:"required"`
}

// ListApplyable 返回开发者可申请的数据源。
func (h *GrantRequestHandler) ListApplyable(c *gin.Context) {
	items, err := h.Datasrc.ListApplyable(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// Create 提交申请。数据源开放申请即可申请其下任意库。
//
// 不再比对「允许申请的库表范围」：数据源登记时不填范围就是全部可申请，
// 有该数据源的权限即可访问其下的库和表。范围白名单本身也拦不住越权——
// 真正的判定在 querysvc，粒度是库（ds:<id>:<库>:*），表名由请求体自述、
// 不参与鉴权（见 querysvc.ExecuteSQL 的说明）。
func (h *GrantRequestHandler) Create(c *gin.Context) {
	var req createGrantRequestReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ds, err := h.Datasrc.Get(c.Request.Context(), req.DatasourceID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "datasource not found"})
		return
	}
	if !ds.ApplyEnabled {
		c.JSON(http.StatusForbidden, gin.H{"error": "this datasource is not open for application"})
		return
	}
	if req.Days <= 0 || req.Days > ds.MaxApplyDays {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "days must be between 1 and " + itoaInt(ds.MaxApplyDays),
		})
		return
	}
	// 整库授权写成 "db:*"，与 Casbin 策略里 ds:<id>:<db>:* 的形态一致。
	//
	// 前端提交整库时传的是裸库名 "shop"，若原样入库，
	// 生成的策略是 ds:<id>:shop（只有三段中的前两段 + 库名），
	// 而鉴权请求是 ds:<id>:shop:orders——keyMatch("ds:1:shop:orders", "ds:1:shop")
	// 不匹配，于是「整库授权」实际一条表都查不了。这是个静默失效：
	// 审批通过、页面显示已授权，但用户查询全是 403。
	//
	// NormalizeScope 对畸形输入返回空串（含空库名、多余冒号、
	// 纯通配 "*"，见其注释），这里必须拒绝而不是继续往下走：
	// 空 scope 会落库成一条永远匹配不上的授权，
	// "*" 更会变成「本数据源全库全表」——远超申请一个库的本意。
	scope := authz.NormalizeScope(req.Scope)
	if scope == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "申请范围格式不合法：应为库名，不能为空或含通配符",
		})
		return
	}

	uid, _, _ := currentUser(c)
	id, err := h.Requests.Create(c.Request.Context(), &model.GrantRequest{
		UserID:       uid,
		DatasourceID: req.DatasourceID,
		Scope:        scope,
		Days:         req.Days,
		Reason:       req.Reason,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": id})
}

// ListMine 返回当前用户自己的申请单。
func (h *GrantRequestHandler) ListMine(c *gin.Context) {
	uid, _, _ := currentUser(c)
	items, err := h.Requests.ListByUser(c.Request.Context(), uid, 50)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// ListPending 供 DBA 查看待审批列表。支持 status 查询参数。
func (h *GrantRequestHandler) ListPending(c *gin.Context) {
	_, _, role := currentUser(c)
	if role != string(model.RoleDBA) && role != string(model.RoleSuperAdmin) {
		c.JSON(http.StatusForbidden, gin.H{"error": "only dba can review grant requests"})
		return
	}

	status := c.Query("status")
	if status == "" {
		status = string(model.RequestPending)
	}
	items, err := h.Requests.ListByStatus(c.Request.Context(), status, 100)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

type reviewReq struct {
	Comment string `json:"comment"`
}

// Approve 审批通过。不能审批自己的申请。
func (h *GrantRequestHandler) Approve(c *gin.Context) {
	uid, username, role := currentUser(c)
	if role != string(model.RoleDBA) && role != string(model.RoleSuperAdmin) {
		c.JSON(http.StatusForbidden, gin.H{"error": "only dba can review grant requests"})
		return
	}

	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	// 强制自审拦截：审计系统的底线
	existing, err := h.Requests.Get(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "grant request not found"})
		return
	}
	if existing.UserID == uid {
		c.JSON(http.StatusForbidden, gin.H{"error": "cannot approve your own request"})
		return
	}

	var req reviewReq
	_ = c.ShouldBindJSON(&req)

	g, err := h.Requests.Approve(c.Request.Context(), id, uid, req.Comment)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}

	auditEvent(c, uid, username, "grant.approve", existing.DatasourceID, map[string]any{
		"request_id": id,
		"user_id":    existing.UserID,
		"scope":      existing.Scope,
		"days":       existing.Days,
		"grant_id":   g.ID,
	})

	if h.OnApprove != nil {
		h.OnApprove(c.Request.Context(), g)
	}
	c.JSON(http.StatusOK, gin.H{"grant_id": g.ID, "expires_at": g.ExpiresAt})
}

// Reject 驳回申请。
func (h *GrantRequestHandler) Reject(c *gin.Context) {
	uid, username, role := currentUser(c)
	if role != string(model.RoleDBA) && role != string(model.RoleSuperAdmin) {
		c.JSON(http.StatusForbidden, gin.H{"error": "only dba can review grant requests"})
		return
	}

	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	var req reviewReq
	_ = c.ShouldBindJSON(&req)
	if strings.TrimSpace(req.Comment) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "comment is required when rejecting"})
		return
	}

	existing, err := h.Requests.Get(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "grant request not found"})
		return
	}
	if existing.UserID == uid {
		c.JSON(http.StatusForbidden, gin.H{"error": "cannot reject your own request"})
		return
	}

	if err := h.Requests.Reject(c.Request.Context(), id, uid, req.Comment); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}

	auditEvent(c, uid, username, "grant.reject", existing.DatasourceID, map[string]any{
		"request_id": id,
		"comment":    req.Comment,
	})
	c.JSON(http.StatusOK, gin.H{"status": "rejected"})
}

// Cancel 申请人撤销自己的待审申请。
func (h *GrantRequestHandler) Cancel(c *gin.Context) {
	uid, username, _ := currentUser(c)
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	if err := h.Requests.Cancel(c.Request.Context(), id, uid); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	auditEvent(c, uid, username, "grant.cancel", 0, map[string]any{"request_id": id})
	c.JSON(http.StatusOK, gin.H{"status": "canceled"})
}

// splitScope 把 "库" / "库:*" / "库:t1,t2" 拆成库名与表清单。
//
// "*" 必须归一成「无非表清单」：
// 整库授权在内部统一表示成 tables 为空（与 model.ApplyScope.Tables 的语义一致）。
// 若把 "*" 当成一个表名放进 tables，后续：
//   - tablesFor 会把它追加进表下拉，界面上多出一项叫 "*"
//   - 元数据接口会去找一张名为 "*" 的表，整库配置永远匹配不上
func splitScope(scope string) (db string, tables []string) {
	parts := strings.SplitN(scope, ":", 2)
	db = strings.TrimSpace(parts[0])
	if len(parts) < 2 {
		return db, nil
	}
	for _, t := range strings.Split(parts[1], ",") {
		s := strings.TrimSpace(t)
		// "*" 单独出现表示整库，等价于不写表清单
		if s == "" || s == "*" {
			continue
		}
		tables = append(tables, s)
	}
	return db, tables
}

func containsStr(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func itoaInt(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
