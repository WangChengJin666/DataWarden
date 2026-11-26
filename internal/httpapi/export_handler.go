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
// 「这个人在这个数据源上有没有导出权限」，给接口更宽的依赖
// 只会让「它还能做什么」变得含糊。
type ExportStore interface {
	CanExport(ctx context.Context, userID, datasourceID int64) (bool, error)
}

// ExportDenier 判断某用户在某数据源上「连查询权限都没有」。
//
// 用来区分两种拒绝，给前端不同提示：
//   - 没有查询权限 → 403「no permission on this datasource」
//     （与查询接口同一句话，不额外透露任何信息）
//   - 有查询权限、但没批导出 → 403「no permission to export」
//     （用户可以据此去申请导出，不申请则永远卡在同一个错误上）
//
// 实参是 authz.Enforcer：对整源授权 Enforce(uid, "ds:<id>:*:*", "read")
// 正是申请通过后落库的那条策略，命中即可认定「这个源他能查」。
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
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	uid, username, role := currentUser(c)

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
		allowed, err := h.Grants.CanExport(c.Request.Context(), uid, req.DatasourceID)
		if err != nil {
			// 查不出来就拒绝：导出是数据外带动作，不能在权限不明时放行。
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if !allowed {
			// 区分"没查询权限"和"有查询权限但没批导出"：后者是用户
			// 能自己解决的（去申请页勾上导出），笼统一句话会让人以为
			// 平台坏了或者自己没权限查数据。
			if h.Enforcer != nil && !h.Enforcer.Enforce(
				SubjectOf(uid), fmt.Sprintf("ds:%d:%s", req.DatasourceID, "*:*"), "read") {
				c.JSON(http.StatusForbidden, gin.H{"error": "no permission on this datasource"})
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
	switch strings.ToLower(strings.TrimSpace(req.Type)) {
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
