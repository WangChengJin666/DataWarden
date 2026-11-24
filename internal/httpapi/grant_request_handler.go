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
	// PendingDatasourceIDs 返回该用户仍有待审批申请单的数据源。
	// 申请页据此把「已申请」的数据源标灰，避免同一数据源重复排队。
	PendingDatasourceIDs(ctx context.Context, userID int64) (map[int64]bool, error)
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
	DatasourceID int64 `json:"datasource_id" binding:"required"`
	// Scope 已不再要求前端提交：申请粒度是「数据源」，审批通过即拥有
	// 该数据源下所有库表的查询权限。字段保留是为了兼容旧客户端
	// （它们仍会传库名），非空时会被忽略——见 Create。
	Scope  string `json:"scope"`
	Days   int    `json:"days" binding:"required"`
	Reason string `json:"reason" binding:"required"`
}

// applyableItem 是「可申请的数据源」列表的一行。
//
// 内嵌完整的数据源结构而不是另建精简视图：这个接口只给已登录用户看，
// 而 Datasource 的 JSON 里本来就不含密码（只有详情接口回显），
// 所以沿用原字段即可，前端不必改已有的读取代码。
//
// Applied 是「当前用户在这个数据源上是否还挂着待审批的申请」：
// 有了它，前端才能把已申请的那几行标成不可再点——否则同一数据源
// 可以反复提交，审批队列里堆一串一模一样的单子。
type applyableItem struct {
	model.Datasource
	Applied bool `json:"applied"`
}

// ListApplyable 返回开发者可申请的数据源，并标注哪些已经申请过。
func (h *GrantRequestHandler) ListApplyable(c *gin.Context) {
	items, err := h.Datasrc.ListApplyable(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	uid, _, _ := currentUser(c)
	// 查待审批集合失败不该让整个申请页打不开：此时最多是「已申请」
	// 标记缺失，用户多点一次申请会在 Create(409) 被挡回来。
	// 比起让人看不到任何数据源，这个降级方向更安全。
	pending, err := h.Requests.PendingDatasourceIDs(c.Request.Context(), uid)
	if err != nil {
		pending = nil
	}

	out := make([]applyableItem, 0, len(items))
	for _, d := range items {
		out = append(out, applyableItem{Datasource: d, Applied: pending[d.ID]})
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// Create 提交申请。粒度是数据源：审批通过即拥有该数据源下所有库表的查询权限。
//
// 为什么不再收「申请哪个库」：
//   - 授权模型本来就是「数据源 + 库」，一库一申请等于让人为了查两个库
//     跑两遍审批，而库里有什么表由数据库决定，用户填的库名还可能拼错，
//     拼错的表现是审批通过后用不了，双方都看不出来；
//   - 范围白名单也拦不住任何东西——真正的判定在 querysvc，
//     库级鉴权（ds:<id>:<库>:*），表名不参与鉴权。
//
// 落库的 scope 固定为 authz.ScopeAllDatabases，生成策略 ds:<id>:*:*。
// 不校验「库是否存在」：那要求申请时就连接目标库，既慢又会因账号/网络
// 问题把「提交申请」这个动作变成不可用。
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

	uid, _, _ := currentUser(c)

	// 已有待审批的申请就拒绝重复提交。
	//
	// 前端会把这类数据源标成「已申请」，但那是展示层——手工调接口
	// 或者两个浏览器标签页同时点，都还能塞进第二条。审批人看到两条
	// 一模一样的单子，批掉一条另一条还挂着，用户会以为申请没成功。
	// 判定必须在服务端。
	pending, err := h.Requests.PendingDatasourceIDs(c.Request.Context(), uid)
	if err == nil && pending[req.DatasourceID] {
		c.JSON(http.StatusConflict, gin.H{"error": "该数据源已有待审批的申请，请等待审批结果"})
		return
	}

	id, err := h.Requests.Create(c.Request.Context(), &model.GrantRequest{
		UserID:       uid,
		DatasourceID: req.DatasourceID,
		// 服务端决定，不接受客户端指定：
		// 若照抄请求里的 scope，旧客户端传的 "shop" 会落成一条只覆盖
		// 单个库的授权，与新口径（整个数据源）不一致，
		// 而界面上两者看起来都是「已授权」。
		Scope:  authz.ScopeAllDatabases,
		Days:   req.Days,
		Reason: req.Reason,
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
//
// scope = "*"（authz.ScopeAllDatabases，即整个数据源）返回库名为 "*"：
// 调用方 tablesFor 拿它跟真实库名比，永远不相等，于是走「去库列表里
// 取授权范围」那条分支——这正好是对的，整源授权的用户能看到哪些库
// 本来就要由 ListDatabases 的逐库鉴权决定，不能在这里凭空生成。
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
