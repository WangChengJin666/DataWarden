package querysvc

import (
	"strings"

	"github.com/opensourceprj/datawarden/internal/adapter"
)

// QueryResult 是 adapter 结果集的别名，便于在 service 层直接使用。
type QueryResult = adapter.QueryResult

// MaskRule 定义一类列的脱敏方式，按列名关键字匹配。
type MaskRule struct {
	// ColumnKeywords 命中任一关键字即套用该规则（小写比较）。
	ColumnKeywords []string
	// Apply 接收原值，返回脱敏结果。
	Apply func(string) string
}

// Rules 是一组有序的脱敏规则。先匹配先生效。
type Rules []MaskRule

// DefaultRules 提供开箱即用的常见规则。
// 后续可改为从 sensitive_rule 表加载。
func DefaultRules() Rules {
	return Rules{
		{
			ColumnKeywords: []string{"phone", "mobile", "tel"},
			Apply:          maskKeep(3, 4),
		},
		{
			ColumnKeywords: []string{"id_card", "idcard", "identity", "cert_no"},
			Apply:          maskKeep(6, 4),
		},
		{
			ColumnKeywords: []string{"email", "mail"},
			Apply:          maskEmail,
		},
		{
			ColumnKeywords: []string{"bank", "card_no", "account_no"},
			Apply:          maskKeep(4, 4),
		},
	}
}

// Mask 对单个值按列名套用规则。未命中任何规则时原值返回。
func (r Rules) Mask(column, value string) string {
	if value == "" {
		return value
	}
	lc := strings.ToLower(column)
	for _, rule := range r {
		for _, kw := range rule.ColumnKeywords {
			if strings.Contains(lc, kw) {
				return rule.Apply(value)
			}
		}
	}
	return value
}

// MaskResult 就地脱敏整个结果集。对行比列短的情况安全跳过。
func (r Rules) MaskResult(res *QueryResult) {
	if res == nil {
		return
	}
	for rowIdx, row := range res.Rows {
		for colIdx, col := range res.Columns {
			if colIdx >= len(row) {
				break
			}
			s, ok := row[colIdx].(string)
			if !ok {
				continue
			}
			res.Rows[rowIdx][colIdx] = r.Mask(col, s)
		}
	}
}

// maskKeep 返回一个"保留前 head 位与后 tail 位、中间打星"的脱敏函数。
// 值过短时整体打星，避免保留部分反而泄露信息。
func maskKeep(head, tail int) func(string) string {
	return func(v string) string {
		runes := []rune(v)
		if len(runes) <= head+tail {
			return "***"
		}
		return string(runes[:head]) + strings.Repeat("*", len(runes)-head-tail) + string(runes[len(runes)-tail:])
	}
}

// maskEmail 保留本地部分前 2 位与完整域名。
func maskEmail(v string) string {
	at := strings.IndexByte(v, '@')
	if at <= 0 {
		return "***"
	}
	local := v[:at]
	if len(local) <= 2 {
		return "**" + v[at:]
	}
	return local[:2] + "***" + v[at:]
}
