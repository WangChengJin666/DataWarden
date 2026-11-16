package adapter

import (
	"testing"
)

func TestIsReadOnlySQL_AllowsSelect(t *testing.T) {
	cases := []string{
		"SELECT * FROM users",
		"select 1",
		"  SELECT id FROM t WHERE a=1  ",
		"SHOW TABLES",
		"DESC users",
		"EXPLAIN SELECT * FROM t",
	}
	for _, c := range cases {
		if !IsReadOnlySQL(c) {
			t.Errorf("should allow: %s", c)
		}
	}
}

func TestIsReadOnlySQL_RejectsWrites(t *testing.T) {
	cases := []string{
		"UPDATE users SET a=1",
		"DELETE FROM users",
		"INSERT INTO users VALUES (1)",
		"DROP TABLE users",
		"TRUNCATE users",
		"ALTER TABLE users ADD c int",
		"CREATE TABLE t (id int)",
		"WITH x AS (SELECT 1) DELETE FROM t",
		"WITH x AS (SELECT 1) UPDATE t SET a=1",
		"WITH x AS (SELECT 1) INSERT INTO t SELECT * FROM x",
	}
	for _, c := range cases {
		if IsReadOnlySQL(c) {
			t.Errorf("should reject: %s", c)
		}
	}
}

func TestIsReadOnlySQL_RejectsCommentOnly(t *testing.T) {
	if IsReadOnlySQL("/* select something */") {
		t.Error("comment-only input must be rejected")
	}
	if IsReadOnlySQL("") {
		t.Error("empty input must be rejected")
	}
}

func TestIsReadOnlySQL_SkipsLeadingComments(t *testing.T) {
	if !IsReadOnlySQL("/* 查订单 */ SELECT * FROM orders") {
		t.Error("leading block comment before SELECT should be allowed")
	}
	if !IsReadOnlySQL("-- 查订单\nSELECT * FROM orders") {
		t.Error("leading line comment before SELECT should be allowed")
	}
	if IsReadOnlySQL("/* 改数据 */ UPDATE orders SET a=1") {
		t.Error("leading comment must not bypass write detection")
	}
}

func TestIsReadOnlySQL_MultiStatementRejected(t *testing.T) {
	// 分号后接写语句属于多语句注入，必须拒绝
	if IsReadOnlySQL("SELECT 1; DROP TABLE users") {
		t.Error("multi-statement with write must be rejected")
	}
}

func TestEnsureRowLimit_AppendsWhenMissing(t *testing.T) {
	got := EnsureRowLimit("SELECT * FROM users", 100)
	if got == "SELECT * FROM users" {
		t.Fatal("should have wrapped the query with a limit")
	}
	if !containsStr(got, "LIMIT 100") {
		t.Errorf("result %q should contain LIMIT 100", got)
	}
}

func TestEnsureRowLimit_KeepsExisting(t *testing.T) {
	sql := "SELECT * FROM users LIMIT 10"
	if got := EnsureRowLimit(sql, 100); got != sql {
		t.Errorf("existing LIMIT must be preserved, got %q", got)
	}
}

func TestEnsureRowLimit_StripsTrailingSemicolon(t *testing.T) {
	got := EnsureRowLimit("SELECT 1;", 5)
	if containsStr(got, ";") {
		t.Errorf("trailing semicolon should be stripped before wrapping, got %q", got)
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
