package adapter

import (
	"context"
	"database/sql"
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
	// AuthSource 是 MongoDB 的认证库，即「账号建在哪个库」。
	//
	// 它和 Database 不是一回事：账号可以建在 admin（全局管理员），
	// 也可以建在某个业务库（该库的专属用户）。填对才能通过 SCRAM 认证。
	// 留空时回落 admin，与 MongoDB 官方工具（驱动 URI 默认值、mongosh
	// 的 --authenticationDatabase 默认值）一致。
	// 其他数据库类型忽略此字段。
	AuthSource string

	// Options 是连接高级参数，键值直接透传给驱动。
	//
	// 为什么需要：真实环境的连接远不止 host/port/user/password——
	//   - MongoDB 副本集只能填一个 host，必须带 replicaSet=
	//   - 云数据库普遍要求 TLS，需要 tls=true 与 CA 证书路径
	//   - 生产的只读查询通常挂从库，需要 readPreference=secondaryPreferred
	//   - PostgreSQL 的 sslmode 之前被硬编码成 disable，云实例直接连不上
	//
	// 用 map[string]string 而不是给每类库加一串字段：不同类型支持的参数
	// 差别很大（Mongo 的 replicaSet、PG 的 sslmode），穷举字段会让结构
	// 无限膨胀；透传给驱动则「驱动支持什么就能填什么」。
	//
	// 白名单在 newMongo/newPostgres 里做，不接受任意参数——
	// 否则 readonly 用户也能通过参数改变连接语义。
	Options map[string]string
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

// TableColumns 是一张表的字段清单（不含类型，保持与 Columns 接口一致）。
type TableColumns struct {
	Table   string   `json:"table"`
	Columns []string `json:"columns"`
}

// SchemaReader 是「一次取回整库所有表字段」的可选能力。
//
// 为什么需要它而不是在 handler 里循环调 Columns：SQL 编辑器要提示
// `别名.` 之后的字段时，别名对应哪张表只有解析完 SQL 才知道，
// 而且用户可能随时改别名——逐表按需请求既猜不准也跟不上输入速度。
// 一次性把整个库的字段拉回来（本地缓存），补全才能做到「打字即提示」。
//
// 与 DatabaseQuerier 同理用可选接口：Redis 没有 schema，MongoDB 的字段
// 是采样得来的、代价远大于一次元数据查询，两者都不实现这个方法，
// handler 用类型断言决定是否走批量路径。
type SchemaReader interface {
	// ListSchema 返回指定库下所有表（含视图）及其字段名。
	//
	// db 的语义与 Adapter.ListDatabases 的返回值一致：
	// PostgreSQL 是 schema 名，MySQL 是库名。
	ListSchema(ctx context.Context, db string) ([]TableColumns, error)
}

// scanSchemaRows 把 (table_name, column_name) 两列结果按表分组。
//
// PostgreSQL 与 MySQL 的 information_schema.columns 列名一致，
// 查询都按 table_name, ordinal_position 排序，所以两个适配器共用这段逻辑；
// 顺序必须保序——编辑器展示字段时按建表顺序比按字母序更贴近直觉。
func scanSchemaRows(rows *sql.Rows) ([]TableColumns, error) {
	var (
		out   []TableColumns
		lastT string
	)
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			return nil, err
		}
		if len(out) == 0 || table != lastT {
			out = append(out, TableColumns{Table: table, Columns: []string{}})
			lastT = table
		}
		out[len(out)-1].Columns = append(out[len(out)-1].Columns, column)
	}
	return out, rows.Err()
}

// Adapter 是 SQL 类数据源的统一入口。
// 注意：Adapter 不做权限判断，权限永远在 querysvc 层。
type Adapter interface {
	Ping(ctx context.Context) error
	ListDatabases(ctx context.Context) ([]string, error)
	ListTables(ctx context.Context, db string) ([]string, error)
	Query(ctx context.Context, sql string, rowLimit int) (*QueryResult, error)
	// QueryWithArgs 执行带参数的只读查询。
	//
	// 只给元数据接口用（information_schema 查询要传库名/表名）。
	// 用户 SQL 永远走 Query，不接受外部传入的参数——
	// 否则「参数化查询」会变成一个绕过只读白名单的后门。
	QueryWithArgs(ctx context.Context, sql string, args []any, rowLimit int) (*QueryResult, error)
	// Close 释放底层连接池。测试连接这类一次性使用必须调用，否则会泄漏连接。
	Close() error
}

// DatabaseQuerier 是「能在调用方指定的库下执行」的 Adapter。
//
// 为什么单独定义而不加进 Adapter：库名落在哪儿因数据库而异。
// MongoDB 的库随每条命令一起传，PostgreSQL 的库由连接串决定（连上后
// 切库需要新连接），MySQL 的库是会话级 schema——只有 MySQL 在
// 「用户在下拉里选了库」和「SQL 实际在哪执行」之间存在断层：
// 连接串里带的是数据源配置的默认库，用户选的库根本没参与连接。
//
// 因此由 MySQL 实现这个可选接口，querysvc 用类型断言决定走不走它。
// 用可选接口而不是给 Adapter 加方法：后者会让所有适配器（含测试替身）
// 都得实现一个对 PG / Mongo 毫无意义的方法。
type DatabaseQuerier interface {
	QueryInDatabase(ctx context.Context, db, sql string, rowLimit int) (*QueryResult, error)
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

// EnsureRowLimit 给语句加上行数上限，返回可直接执行的 SQL。
//
// 不能一律包成 SELECT * FROM (...) —— SHOW / DESC / EXPLAIN 不是 SELECT，
// 嵌进子查询后语法直接报错：
//
//	SHOW VARIABLES LIKE 'x'
//	→ SELECT * FROM (SHOW VARIABLES LIKE 'x') AS _dw_limit_wrap LIMIT 101   ✗
//
// 而 IsReadOnlySQL 明确放行了这五个关键字，放行了却执行不了。
// 所以按语句类型分流：
//
//	SELECT / WITH             → 外层包裹限行
//	SHOW / DESC / DESCRIBE    → 原样执行，靠驱动读取的行数截断
//	EXPLAIN                   → 原样执行（PG 的 EXPLAIN 不接受 LIMIT 包裹）
//
// 语义类语句（SHOW/DESC/EXPLAIN）的返回规模本身有限：SHOW 是元数据行，
// DESC 是列定义，EXPLAIN 是执行计划，不存在「扫全表」的风险，
// 真正的兜底在 adapter.Query 的 rowLimit 循环计数里。
func EnsureRowLimit(sql string, limit int) string {
	s := strings.TrimSpace(sql)
	trimmed := strings.TrimRight(strings.TrimSpace(s), "; \t\r\n")

	upper := strings.ToUpper(trimmed)

	// 语义类语句：加不了 LIMIT，原样执行
	for _, kw := range []string{"SHOW", "DESC", "DESCRIBE", "EXPLAIN"} {
		if strings.HasPrefix(upper, kw) {
			return trimmed
		}
	}

	// 已经有 LIMIT 的语句不再改：外层再包一层会让 LIMIT 语义变化
	// （内层的 LIMIT 先截断，外层再截一次，行数还是内层的），
	// 也躲不过「LIMIT 写在换行后」这类写法，所以这里做词法级判断。
	if hasTopLevelLimit(trimmed) {
		return trimmed
	}
	return fmt.Sprintf("SELECT * FROM (%s) AS _dw_limit_wrap LIMIT %d", trimmed, limit)
}

// hasTopLevelLimit 判断语句顶层是否已经带了 LIMIT 子句。
//
// 之前用 strings.Contains(upper, " LIMIT ")，有三个坑：
//   1. LIMIT 写在换行后（"...\nLIMIT 10"）→ 判不出来，被外层再包一层
//   2. LIMIT 后面是制表符 → 同样判不出来
//   3. 字符串字面量里出现 " limit "（如 WHERE note = 'no limit here'）→ 误判为已限行，
//      结果真的没有限行
//
// 这里做一次轻量词法扫描：跳过字符串、标识符、注释，只认顶层的 LIMIT 关键字。
func hasTopLevelLimit(s string) bool {
	depth := 0
	inStr := false
	var quote byte

	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inStr:
			if c == quote {
				// 处理 SQL 的 '' 转义
				if i+1 < len(s) && s[i+1] == quote {
					i++
					continue
				}
				inStr = false
			}
			continue
		case c == '\'' || c == '"' || c == '`':
			inStr = true
			quote = c
			continue
		case c == '-' && i+1 < len(s) && s[i+1] == '-':
			// 行注释：跳到行尾
			for i < len(s) && s[i] != '\n' {
				i++
			}
			continue
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			// 块注释：跳到 */
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				return false
			}
			i += end + 3
			continue
		case c == '(':
			depth++
			continue
		case c == ')':
			if depth > 0 {
				depth--
			}
			continue
		}

		if depth != 0 || (c != 'l' && c != 'L') {
			continue
		}
		// 顶层出现 "limit" 且前后都不是标识符字符
		if i+5 > len(s) || !strings.EqualFold(s[i:i+5], "limit") {
			continue
		}
		beforeOK := i == 0 || !isIdentChar(s[i-1])
		after := i + 5
		afterOK := after >= len(s) || !isIdentChar(s[after])
		if beforeOK && afterOK {
			return true
		}
	}
	return false
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
