package adapter

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ConnConfig 是连接目标库所需的全部信息。
type ConnConfig struct {
	Type     string // mysql / postgresql / redis / mongodb
	Host     string
	Port     int
	Database string
	User     string
	Password string
}

// QueryResult 是统一的查询结果，屏蔽各驱动差异。
type QueryResult struct {
	Columns   []string        `json:"columns"`
	Rows      [][]interface{} `json:"rows"`
	RowCount  int             `json:"row_count"`
	Truncated bool            `json:"truncated"`
	ElapsedMs int64           `json:"elapsed_ms"`
}

// TableRef 标识一张表。
type TableRef struct {
	Database string
	Table    string
}

// Adapter 是 SQL 类数据源的统一入口。
// 注意：Adapter 不做权限判断，权限永远在 querysvc 层。
type Adapter interface {
	Ping(ctx context.Context) error
	ListDatabases(ctx context.Context) ([]string, error)
	ListTables(ctx context.Context, db string) ([]string, error)
	Query(ctx context.Context, sql string, rowLimit int) (*QueryResult, error)
}

// readOnlyKeywords 是允许的语句起始关键字白名单。
var readOnlyKeywords = []string{"SELECT", "SHOW", "DESC", "DESCRIBE", "EXPLAIN", "WITH"}

// writeKeywords 出现在 WITH 语句内即判定为可写 CTE。
var writeKeywords = []string{"INSERT", "UPDATE", "DELETE", "MERGE", "UPSERT", "REPLACE", "TRUNCATE"}

// stripLeadingComments 去掉语句开头的 /* */ 与 -- 注释，返回剩余部分。
// 第二个返回值为 false 表示整条语句只有注释（应拒绝）。
func stripLeadingComments(s string) (string, bool) {
	for {
		s = strings.TrimSpace(s)
		switch {
		case strings.HasPrefix(s, "/*"):
			end := strings.Index(s, "*/")
			if end < 0 {
				return "", false
			}
			s = s[end+2:]
		case strings.HasPrefix(s, "--"):
			idx := strings.IndexByte(s, '\n')
			if idx < 0 {
				return "", false
			}
			s = s[idx+1:]
		default:
			return s, s != ""
		}
	}
}

// IsReadOnlySQL 判断语句是否为只读。
// 采用保守白名单：只允许 SELECT / SHOW / DESC / DESCRIBE / EXPLAIN / WITH 开头。
// 额外拦截：多语句（分号后有内容）、可写 CTE。
func IsReadOnlySQL(sql string) bool {
	s, ok := stripLeadingComments(sql)
	if !ok {
		return false
	}

	upper := strings.ToUpper(s)

	// 多语句注入：剥掉末尾分号后，语句中间仍有分号即视为多语句
	trimmed := strings.TrimRight(upper, "; \t\r\n")
	if strings.Contains(trimmed, ";") {
		return false
	}

	matched := false
	for _, kw := range readOnlyKeywords {
		if strings.HasPrefix(trimmed, kw) {
			matched = true
			break
		}
	}
	if !matched {
		return false
	}

	// WITH 语句可能内含 UPDATE/DELETE，需要逐词检查
	if strings.HasPrefix(trimmed, "WITH") {
		flat := strings.Join(strings.Fields(trimmed), " ")
		for _, w := range writeKeywords {
			if containsWord(flat, w) {
				return false
			}
		}
	}

	return true
}

// containsWord 判断 flat（已大写、空格规整）中是否出现完整单词 w。
func containsWord(flat, w string) bool {
	idx := 0
	for {
		pos := strings.Index(flat[idx:], w)
		if pos < 0 {
			return false
		}
		abs := idx + pos
		beforeOK := abs == 0 || !isIdentChar(flat[abs-1])
		after := abs + len(w)
		afterOK := after >= len(flat) || !isIdentChar(flat[after])
		if beforeOK && afterOK {
			return true
		}
		idx = abs + len(w)
	}
}

func isIdentChar(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

// EnsureRowLimit 在语句没有 LIMIT 时追加外层包裹的限行。
func EnsureRowLimit(sql string, limit int) string {
	s := strings.TrimSpace(sql)
	trimmed := strings.TrimSuffix(s, ";")
	trimmed = strings.TrimSpace(trimmed)

	if strings.Contains(strings.ToUpper(trimmed), " LIMIT ") {
		return trimmed
	}
	return fmt.Sprintf("SELECT * FROM (%s) AS _dw_limit_wrap LIMIT %d", trimmed, limit)
}

func elapsed(start time.Time) int64 {
	return time.Since(start).Milliseconds()
}

// normalizeRow 把驱动返回的 []byte 转成 string，时间转 RFC3339，
// 保证 JSON 序列化结果稳定可读。
func normalizeRow(vals []interface{}) []interface{} {
	out := make([]interface{}, len(vals))
	for i, v := range vals {
		switch t := v.(type) {
		case []byte:
			out[i] = string(t)
		case time.Time:
			out[i] = t.Format(time.RFC3339)
		default:
			out[i] = v
		}
	}
	return out
}
