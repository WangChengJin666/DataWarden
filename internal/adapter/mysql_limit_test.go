package adapter

import (
	"strings"
	"testing"
)

// MySQL 的限行必须用「末尾追加 LIMIT」，不能包成派生表。
//
// MySQL 要求派生表列名唯一，联表 SELECT * 到处都是同名列（两张表的 id），
// 包一层就变成 Error 1060 (42S21): Duplicate column name 'x'。
// 这是最普通的写法（SELECT * FROM 主表 JOIN 明细表），不能一执行就报错。
func TestMySQLQueryWithLimit_NoDerivedTable(t *testing.T) {
	q := "SELECT * FROM doc_activity a INNER JOIN doc_activity_pro t ON a.id = t.id"
	got := mysqlQueryWithLimit(q, 101)
	if strings.Contains(got, "_dw_limit_wrap") {
		t.Fatalf("must not wrap into a derived table, got %q", got)
	}
	if !strings.HasSuffix(got, " LIMIT 101") {
		t.Fatalf("expected trailing LIMIT 101, got %q", got)
	}
	if !strings.HasPrefix(got, q) {
		t.Fatalf("original statement must be preserved, got %q", got)
	}
}

// 已带 LIMIT 的语句不重复追加。
func TestMySQLQueryWithLimit_KeepsExistingLimit(t *testing.T) {
	for _, q := range []string{
		"SELECT * FROM t LIMIT 10",
		"SELECT * FROM t\nLIMIT 10",
		"SELECT * FROM t limit 10;",
	} {
		got := mysqlQueryWithLimit(q, 101)
		if strings.Count(strings.ToUpper(got), "LIMIT") != 1 {
			t.Errorf("%q must keep a single LIMIT, got %q", q, got)
		}
	}
}

// 语义类语句加不了 LIMIT，原样执行，靠行数计数兜底。
func TestMySQLQueryWithLimit_SemanticStatements(t *testing.T) {
	for _, q := range []string{"SHOW TABLES", "DESC orders", "EXPLAIN SELECT 1"} {
		if got := mysqlQueryWithLimit(q, 101); got != q {
			t.Errorf("%q must pass through, got %q", q, got)
		}
	}
}

// 字符串字面量里的 limit 不算 LIMIT 子句，必须仍然追加。
func TestMySQLQueryWithLimit_LimitInsideString(t *testing.T) {
	got := mysqlQueryWithLimit("SELECT * FROM t WHERE note = 'no limit here'", 101)
	if !strings.HasSuffix(got, " LIMIT 101") {
		t.Fatalf("must still append LIMIT, got %q", got)
	}
}

// 尾部分号会被去掉，否则追加 LIMIT 之后语法错误。
func TestMySQLQueryWithLimit_TrimsSemicolon(t *testing.T) {
	got := mysqlQueryWithLimit("SELECT * FROM t;", 101)
	if strings.Contains(got, ";") {
		t.Fatalf("semicolon must be trimmed, got %q", got)
	}
	if !strings.HasSuffix(got, " LIMIT 101") {
		t.Fatalf("expected trailing LIMIT, got %q", got)
	}
}
