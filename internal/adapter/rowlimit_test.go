package adapter

import (
	"strings"
	"testing"
)

// 语义类语句不能被包成子查询：SHOW VARIABLES LIKE 'x' 嵌进
// SELECT * FROM (...) 后是非法 SQL，而 IsReadOnlySQL 又放行了 SHOW/DESC/EXPLAIN。
// 这两个函数的口径必须一致，否则「允许的语句执行不了」。
func TestEnsureRowLimit_DoesNotWrapNonSelect(t *testing.T) {
	cases := []string{
		"SHOW VARIABLES LIKE 'max_connections'",
		"SHOW TABLES",
		"DESC orders",
		"DESCRIBE orders",
		"EXPLAIN SELECT * FROM orders",
		"explain analyze select 1",
	}
	for _, c := range cases {
		got := EnsureRowLimit(c, 101)
		if strings.Contains(got, "_dw_limit_wrap") {
			t.Errorf("%q must not be wrapped, got %q", c, got)
		}
		if got != c {
			t.Errorf("%q should pass through unchanged, got %q", c, got)
		}
	}
}

// 每个被 IsReadOnlySQL 放行的语句都必须能通过 EnsureRowLimit 得到可执行形态。
// 这条断言防的是「两个函数各改各的、口径逐渐漂移」。
func TestEnsureRowLimit_ConsistentWithReadOnly(t *testing.T) {
	allowed := []string{
		"SELECT 1",
		"SHOW TABLES",
		"DESC orders",
		"EXPLAIN SELECT 1",
		"WITH t AS (SELECT 1) SELECT * FROM t",
	}
	for _, q := range allowed {
		if !IsReadOnlySQL(q) {
			t.Fatalf("%q should be read-only", q)
		}
		if out := EnsureRowLimit(q, 100); strings.TrimSpace(out) == "" {
			t.Errorf("%q produced empty statement", q)
		}
	}
}

func TestEnsureRowLimit_WrapsPlainSelect(t *testing.T) {
	got := EnsureRowLimit("SELECT * FROM orders", 101)
	want := "SELECT * FROM (SELECT * FROM orders) AS _dw_limit_wrap LIMIT 101"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// 已带 LIMIT 的语句不再包一层：外层包裹会让行数语义变得难以解释。
func TestEnsureRowLimit_KeepsExistingLimit(t *testing.T) {
	cases := []string{
		"SELECT * FROM t LIMIT 10",
		"SELECT * FROM t\nLIMIT 10",
		"SELECT * FROM t\tLIMIT 10",
		"SELECT * FROM t limit 10;",
	}
	for _, c := range cases {
		got := EnsureRowLimit(c, 101)
		if strings.Contains(got, "_dw_limit_wrap") {
			t.Errorf("%q already has LIMIT, must not be wrapped; got %q", c, got)
		}
	}
}

// 字符串字面量里的 "limit" 不是 LIMIT 子句，
// 之前用 Contains 判断会把这类语句当成「已限行」而完全不加限制。
func TestEnsureRowLimit_LimitInsideStringIsNotClause(t *testing.T) {
	got := EnsureRowLimit(`SELECT * FROM t WHERE note = 'no limit here'`, 101)
	if !strings.Contains(got, "_dw_limit_wrap") {
		t.Errorf("string literal must not count as LIMIT clause, got %q", got)
	}
}

// 注释里的 limit 同理。
func TestEnsureRowLimit_LimitInsideCommentIsNotClause(t *testing.T) {
	got := EnsureRowLimit("SELECT * FROM t -- limit 10\n", 101)
	if !strings.Contains(got, "_dw_limit_wrap") {
		t.Errorf("comment must not count as LIMIT clause, got %q", got)
	}
}

// 子查询里的 LIMIT 不能顶替外层的限行：
// 外层仍需要限制，否则子查询返回多少行就返回多少行。
func TestEnsureRowLimit_SubqueryLimitNeedsOuterWrap(t *testing.T) {
	got := EnsureRowLimit("SELECT * FROM (SELECT * FROM t LIMIT 5) x", 101)
	if !strings.Contains(got, "_dw_limit_wrap") {
		t.Errorf("subquery LIMIT must not satisfy the outer limit; got %q", got)
	}
}
