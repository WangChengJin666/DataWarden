package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/opensourceprj/datawarden/internal/adapter"
	"github.com/opensourceprj/datawarden/internal/model"
	"github.com/opensourceprj/datawarden/internal/querysvc"
	"github.com/opensourceprj/datawarden/internal/xlsx"
)

// ExportStore 是导出接口需要的权限查询。
//
// 只依赖这一个方法而不是整个 GrantStore：导出接口的闸门就是
// 「这个人能不能在这个数据源的**这个库**上导出」，给接口更宽的依赖
// 只会让「它还能做什么」变得含糊。
//
// 注意签名带 database：判定粒度必须是库。只看「这个数据源上有没有
// 任何一条批了导出的授权」会把 A 库的许可借给 B 库（见 store 层注释）。
type ExportStore interface {
	CanExportDatabase(ctx context.Context, userID, datasourceID int64, database string) (bool, error)
}

// ExportDenier 判断某用户在某库上「有没有查询权限」。
//
// 用来区分两种拒绝，给前端不同提示：
//   - 没有查询权限 → 403「no permission on this database」
//     （与查询接口同一句话，不额外透露任何信息）
//   - 有查询权限、但没批导出 → 403 提示去申请导出
//     （用户可以据此自己解决，否则会永远卡在同一个错误上）
//
// 实参是 authz.Enforcer。判定对象必须与 querysvc 完全一致：
//
//	ds:<id>:<库>:*
//
// 不能写成 ds:<id>:*:*。matcher 是 keyMatch(r.obj, p.obj)（见
// authz/casbin.go 的 modelText），而 casbin 的 KeyMatch 只做**字节前缀
// 比较**：取策略侧 p.obj 第一个 `*` 之前的部分作为前缀，请求对象必须以
// 它开头，`*` 之后的内容整段忽略。策略只由 grant_record 经
// NormalizeScope 生成（"*" → "*:*"，"shop:*" 保持原样），于是：
//   - 库级策略 "ds:1:shop:*" 的前缀是 "ds:1:shop:"，而这里发出的请求
//     对象是 "ds:1:*:*" —— 前缀对不上，不命中；
//   - 整源策略 "ds:1:*:*" 的前缀只有 "ds:1:"，能命中该源任意对象，
//     所以走正常申请流程的用户（Approve 恒写 ScopeAllDatabases）
//     不会踩到这个坑。
//
// 踩坑的只有「持有历史/手工库级授权」的用户：他们查询时 querysvc 用
// ds:1:shop:* 判定命中，查得动；导出却因为上面那个对不上的前缀被判成
// 「没有查询权限」，提示方向与事实相反。
//
// 所以这两个场景要分开看：判定对象照抄 querysvc 的 ds:<id>:<库>:*，
// 用 Casbin 结论决定提示文案是"没查询权限"还是"没批导出"；
// 而**是否放行**由 CanExportDatabase 按库匹配授权记录决定，两者都通过才导。
//
// 为什么不直接按 grant_record 里有没有覆盖该库的记录来判断：
// 那样会把畸形 scope（例如 "*:*"）当成"有查询权限"。这类记录
// SyncGrants 会跳过、不生成策略，用户实际查不了，导出却被提示成
// "缺导出权限"。Casbin 策略是"当前真正生效的权限"的唯一事实源，
// 提示方向要跟着它走。
type ExportDenier interface {
	Enforce(subject, object, action string) bool
}

// ExportHandler 把一次查询结果导出成 Excel（.xlsx）。
//
// 为什么不是「前端把表格数据 POST 回来让后端转格式」：
// 那样导出的是前端手里的那一份（可能被用户改过、可能只有当前分页），
// 而审计里能证明的只是「他提交了一份数据」。现在这条链路是后端
// 用同一份凭证重新执行同一条语句，导出的行数与内容都可复现，
// 审计才算说得清——这也和「查询走 querysvc 统一脱敏」保持一致：
// 导出的内容同样经过脱敏规则，不是把原始数据泄露出去。
//
// 与 /query/* 的区别只有两点：多一道导出权限闸门，以及输出格式变成 xlsx。
type ExportHandler struct {
	Service *querysvc.Service
	Factory AdapterFactory
	// Grants 决定「这个数据源能不能导出」，为 nil 时一律拒绝（fail-closed）。
	Grants ExportStore
	// Enforcer 用于区分「没查询权限」与「没导出权限」，可为 nil
	//（此时两种情况都回同一句话，不影响安全性）。
	Enforcer ExportDenier
}

// 导出的行数上限。
//
// 比单次查询上限（500）宽得多：导出的用途就是拿全量，
// 卡在 500 行等于让人导出个截图。但必须有个上限——不设上限时
// 一条 SELECT 就能把整个库拉进内存再序列化成 xlsx，进程直接 OOM，
// 而这是任何一个拿到授权的账号都能触发的事故。
const (
	exportDefaultRowLimit = 5000
	exportMaxRowLimit     = 20000
)

// exportRequest 是导出请求体。
//
// 与 sqlQueryRequest / mongoQueryRequest 刻意分开而不是复用：
// 导出没有 "tables"（那是查询审计用的自述字段），也不需要脱敏开关
// （脱敏是硬规则）。复用一个带可选字段的大结构会让「导出能不能关掉
// 脱敏」变成一个问题，而答案永远是"不能"。
type exportRequest struct {
	DatasourceID int64  `json:"datasource_id" binding:"required"`
	// Type 决定按哪种查询执行：sql / mongo。Redis 不支持导出（见 Export）。
	Type       string   `json:"type" binding:"required"`
	Database   string   `json:"database"`
	Collection string   `json:"collection"`
	Op         string   `json:"op"`
	SQL        string   `json:"sql"`
	Filter     string   `json:"filter"`
	Command    string   `json:"command"`
	RowLimit   int      `json:"row_limit"`
	// Filename 是前端建议的文件名（不含扩展名），后端只做清洗，
	// 不接受任意字符——它直接进 Content-Disposition。
	Filename string `json:"filename"`
}

// Export 执行一次受控查询并把结果写成 xlsx。
func (h *ExportHandler) Export(c *gin.Context) {
	if h.Service == nil || h.Factory == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "export not configured"})
		return
	}

	var req exportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// 走 writeBindError 而不是 err.Error()：后者会把内部结构体名
		// （exportRequest）、字段名和校验 tag 一起吐给前端。
		writeBindError(c, err)
		return
	}

	uid, username, role := currentUser(c)

	// 0. 先校验 type，再谈权限。
	//
	// 顺序不能反：type 非法是请求写错了（400），与权限无关。
	// 若放在闸门之后，一个 type 拼错的开发者请求会先撞上权限判定，
	// 得到 403 "当前授权不包含导出权限"——而他可能压根有权限，
	// 只是 type 传了 "MySQL" 之类。误导性提示比不给提示更糟。
	//
	// 归一方式（ToLower + TrimSpace）与下面 switch 完全一致，
	// 两处必须同步改，否则会出现"过了校验但进不了分支"的空档。
	kind := strings.ToLower(strings.TrimSpace(req.Type))
	switch kind {
	case "sql", "mongo":
		// 合法，继续
	case "redis":
		// Redis 的结果是 key → 类型 → 值（hash / list / zset / 二进制），
		// 塞进二维表只会得到一列乱糟糟的字符串；要给一份能用的表格
		// 得先决定"一个 key 的 100 个元素怎么摊平"，那是产品决定而不是导出功能。
		c.JSON(http.StatusBadRequest, gin.H{"error": "Redis 查询结果暂不支持导出"})
		return
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "type must be one of sql, mongo"})
		return
	}

	// 1. 导出闸门。
	//
	// 管理角色免授权（与查询同口径）：他们本来就能查任意数据源，
	// 拦导出只是让自己用不了功能，不会减少任何泄露面。
	// 开发者必须有一条 can_export=true 的生效授权，且是**这个数据源**的——
	// A 源批了导出、B 源没批，正是这套判定要正确处理的场景。
	if !model.Role(role).IsPrivileged() {
		if h.Grants == nil {
			c.JSON(http.StatusForbidden, gin.H{"error": "no permission to export"})
			return
		}
		// 判定粒度是「这个数据源的这个库」，与查询侧完全一致。
		//
		// 取不到库名时按数据源类型给不同提示：Mongo 的常见原因是集合名
		// 为空或写坏（SplitDBCollection 返回 err），此时说"请先选择数据库"
		// 会把人引到错误的修正方向上——库明明已经选了。
		authzDB, ok := exportAuthzDatabase(req, kind)
		if !ok {
			if kind == "mongo" {
				if strings.TrimSpace(req.Collection) == "" {
					c.JSON(http.StatusBadRequest, gin.H{"error": "请先选择集合（collection 字段必填）"})
					return
				}
				c.JSON(http.StatusBadRequest, gin.H{"error": "集合名不合法，应形如 users 或 shop.users"})
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": "请先选择数据库（database 字段必填）"})
			return
		}
		allowed, err := h.Grants.CanExportDatabase(c.Request.Context(), uid, req.DatasourceID, authzDB)
		if err != nil {
			// 查不出来就拒绝：导出是数据外带动作，不能在权限不明时放行。
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
			return
		}
		if !allowed {
			// 区分"没查询权限"和"有查询权限但没批导出"：后者是用户
			// 能自己解决的（在申请页勾上导出），笼统一句话会让人以为
			// 平台坏了或者自己连查询权限都没了。
			//
			// 判定对象与 querysvc 一致（ds:<id>:<库>:*），不能写成
			// ds:<id>:*:*——那个只有整源授权才命中，库级授权的用户
			// 会被误判成"连查询权限都没有"。
			//
			// Enforcer 为 nil 时跳过这个分支：此时无法区分两种拒绝，
			// 就按"没批导出"提示。反过来直接报"没有查询权限"会有两种
			// 错误方向——能查的人被告知没权限，或根本不该走的路径给出
			// 误导性结论。
			if h.Enforcer != nil && !h.Enforcer.Enforce(
				SubjectOf(uid), fmt.Sprintf("ds:%d:%s:*", req.DatasourceID, authzDB), "read") {
				c.JSON(http.StatusForbidden, gin.H{
					"error": "no permission on this database（该库无查询权限，需先申请数据源授权）",
				})
				return
			}
			c.JSON(http.StatusForbidden, gin.H{
				"error": "当前授权不包含导出权限，请在「申请授权」页勾选「可导出查询结果」后重新申请",
			})
			return
		}
	}

	ad, err := h.Factory(req.DatasourceID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	rowLimit := clampExportRows(req.RowLimit)

	// 2. 按类型执行。执行的仍然是 querysvc：鉴权、只读校验、限行、
	//    脱敏、审计全部复用，导出不会绕开任何一条。
	var (
		res    *adapter.QueryResult
		action string
	)
	switch kind {
	case "sql":
		action = "export.sql"
		res, err = h.Service.ExecuteSQL(c.Request.Context(), querysvc.SQLRequest{
			Subject:      SubjectOf(uid),
			ActorID:      uid,
			ActorName:    username,
			DatasourceID: req.DatasourceID,
			Database:     req.Database,
			SQL:          req.SQL,
			RowLimit:     rowLimit,
			IP:           c.ClientIP(),
			UserAgent:    c.Request.UserAgent(),
		}, ad)
	case "mongo":
		action = "export.mongo"
		res, err = h.Service.ExecuteMongo(c.Request.Context(), querysvc.MongoRequest{
			Subject:      SubjectOf(uid),
			ActorID:      uid,
			ActorName:    username,
			DatasourceID: req.DatasourceID,
			Database:     req.Database,
			Collection:   req.Collection,
			Op:           req.Op,
			Args:         pickMongoArgs(req),
			RowLimit:     rowLimit,
			IP:           c.ClientIP(),
			UserAgent:    c.Request.UserAgent(),
		}, ad)
	}

	if err != nil {
		switch {
		case errors.Is(err, querysvc.ErrForbidden):
			c.JSON(http.StatusForbidden, gin.H{"error": "no permission on this database"})
		case errors.Is(err, querysvc.ErrNoDatabase):
			c.JSON(http.StatusBadRequest, gin.H{"error": "请先选择数据库（database 字段必填）"})
		case errors.Is(err, querysvc.ErrNoCollection):
			c.JSON(http.StatusBadRequest, gin.H{"error": "请先选择集合"})
		case errors.Is(err, querysvc.ErrNotReadOnly):
			c.JSON(http.StatusBadRequest, gin.H{"error": "only read-only statements are allowed"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}

	// 3. 生成 xlsx。失败只影响下载，不影响已经发生的查询——
	//    查询本身已在 querysvc 里审计过（query.execute / mongo.execute）。
	book, err := xlsx.FromQueryResult(res.Columns, res.Rows)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "generate xlsx: " + err.Error()})
		return
	}

	name := exportFilename(req.Filename)
	c.Header("Content-Disposition", contentDisposition(name))
	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Header("Content-Length", fmt.Sprintf("%d", len(book)))
	// 前端靠这个响应头拿真实文件名与截断标记：Content-Disposition 里的
	// 中文名经过 RFC 5987 编码，直接读出来是一串 %E4%...
	c.Header("X-Export-Rows", fmt.Sprintf("%d", len(res.Rows)))
	c.Header("X-Export-Truncated", boolStr(res.Truncated))

	// 单独一条审计：导出与外带相关，不能只在查询审计里留个痕。
	// 记下这个数据源、语句与行数，事后能回答"他到底导走了什么"。
	auditEvent(c, uid, username, action, req.DatasourceID, map[string]any{
		"database":   req.Database,
		"collection": req.Collection,
		"row_count":  len(res.Rows),
		"truncated":  res.Truncated,
		"row_limit":  rowLimit,
		"query":      exportQueryText(req),
		"bytes":      len(book),
	})

	c.Data(http.StatusOK, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", book)
}

// exportAuthzDatabase 取出这次导出实际落在哪个库上，用于复刻 querysvc 的
// 库级鉴权对象。
//
// kind 是已归一的 type（ToLower + TrimSpace），由调用方算一次传进来：
// 若这里再解析一遍 req.Type，两处的归一规则就会各写一套，
// 改其中一处时另一处会静默漂移（例如新增一种别名后只有执行分支认得）。
//
// SQL：直接用请求里的 database（querysvc 就是拿它拼 ds:<id>:<库>:*）。
// Mongo：集合名可能带 "db.coll" 前缀，真正的库在那一段里；拆不出来时
// 退回请求里的 database。
//
// 返回 false 表示无法确定库名——调用方据此跳过"是否连查询权限都没有"
// 的判断，而不是猜一个库名去鉴权（猜错的库名会得出错误结论）。
func exportAuthzDatabase(req exportRequest, kind string) (string, bool) {
	if kind == "mongo" {
		db, _, err := querysvc.SplitDBCollection(req.Collection, req.Database)
		if err != nil {
			return "", false
		}
		if db = strings.TrimSpace(db); db != "" {
			return db, true
		}
	}
	// 统一 TrimSpace：这个返回值会被**同时**用于查授权与拼 Casbin 对象
	// （ds:<id>:<库>:*），两侧必须同一口径。库名可能来自手工输入，
	// 带个首尾空格时若只在一侧修剪，就会出现"授权判定通过、
	// 鉴权对象对不上"的偏差，表现为误报"该库无查询权限"。
	db := strings.TrimSpace(req.Database)
	if db == "" {
		return "", false
	}
	return db, true
}

// pickMongoArgs 取 Mongo 查询的参数原文：结构化模式用 filter，命令行模式用 command。
func pickMongoArgs(req exportRequest) string {
	if strings.TrimSpace(req.Command) != "" {
		return req.Command
	}
	return req.Filter
}

// exportQueryText 是写进审计里的"导出了什么"。
//
// Mongo 的两种输入形态合并成一条可读文本；SQL 原样记录。
// 只用于审计呈现，不参与任何判定。
func exportQueryText(req exportRequest) string {
	if strings.TrimSpace(req.Command) != "" {
		return req.Command
	}
	if strings.TrimSpace(req.Filter) != "" {
		return req.Filter
	}
	return req.SQL
}

// clampExportRows 把请求的行数收敛到 [1, exportMaxRowLimit]。
// 非正数一律当成"用默认值"，而不是当成 0 行——用户在输入框里
// 清空数字时传上来的就是 0，把它理解成"一行都不要"只会得到空文件。
func clampExportRows(n int) int {
	if n <= 0 {
		return exportDefaultRowLimit
	}
	if n > exportMaxRowLimit {
		return exportMaxRowLimit
	}
	return n
}

// exportFilename 把前端建议的文件名清洗成安全的形态。
//
// Content-Disposition 里出现的字符会被浏览器解析成头的一部分，
// 而文件名来自前端（可被篡改），因此只保留中文、字母、数字、空格
// 与 _ - . 这几个字符；其余一律替换成下划线。
// 点号也不允许出现在开头（.hidden）或连续（../ 之类的路径穿越）。
func exportFilename(raw string) string {
	name := strings.TrimSpace(raw)
	if name == "" {
		name = "query_result"
	}
	// 去掉扩展名：前端建议的名字可能已经带了 .xlsx，统一由后端补
	name = strings.TrimSuffix(name, ".xlsx")
	name = strings.TrimSuffix(name, ".xls")

	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 0x4e00 && r <= 0x9fff: // 中文
			b.WriteRune(r)
		case r == '_' || r == '-' || r == ' ' || r == '.' || r == '（' || r == '）':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := strings.Trim(b.String(), " .")
	if out == "" {
		out = "query_result"
	}
	// 文件名过长会让某些客户端截断甚至拒绝保存
	if r := []rune(out); len(r) > 80 {
		out = string(r[:80])
	}
	return out + ".xlsx"
}

// contentDisposition 生成兼容中文文件名的头。
//
// 只给 filename 会让非 ASCII 名被浏览器丢弃（RFC 6266 要求用
// RFC 5987 的 filename*=UTF-8''...）。两个都给：老客户端读 filename，
// 新客户端读 filename*，后者优先。
func contentDisposition(name string) string {
	ascii := make([]rune, 0, len(name))
	for _, r := range name {
		if r < 0x80 && r != '"' && r != '\\' {
			ascii = append(ascii, r)
		} else {
			ascii = append(ascii, '_')
		}
	}
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`,
		string(ascii), url.PathEscape(name))
}

func boolStr(v bool) string {
	if v {
		return "1"
	}
	return "0"
}
