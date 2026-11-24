package adapter

import (
	"testing"
)

// 联表查询里同名列必须被区分开：
// SELECT * FROM app_user a INNER JOIN audit_log t ON a.id = t.actor_id
// 会返回两个 id、两个 created_at，重复列名会让前端按列名生成的表格
// 出现重复 key（React 报 warning 且渲染错行），用户也不知道哪个 id 属于哪张表。
func TestDedupeColumnNames_RenamesOnlyDuplicates(t *testing.T) {
	in := []string{
		"id", "username", "email", "password_hash", "role", "status", "created_at",
		"id", "actor_id", "actor_name", "action", "object", "detail", "ip", "user_agent", "created_at",
	}
	got := dedupeColumnNames(in)

	if len(got) != len(in) {
		t.Fatalf("length changed: %d -> %d", len(in), len(got))
	}

	seen := make(map[string]int, len(got))
	for _, c := range got {
		seen[c]++
	}
	for name, n := range seen {
		if n > 1 {
			t.Fatalf("column %q still duplicated (%d times): %v", name, n, got)
		}
	}

	// 不重名的列保持原样，用户才找得到熟悉的列
	for _, keep := range []string{"username", "email", "role", "action", "detail"} {
		if !contains(got, keep) {
			t.Errorf("non-duplicate column %q should stay unchanged, got %v", keep, got)
		}
	}
	// 重名列带上位置序号，按位置一一对应驱动给出的列
	if got[0] != "1.id" || got[6] != "7.created_at" || got[7] != "8.id" || got[15] != "16.created_at" {
		t.Errorf("duplicate columns not position-tagged as expected: %v", got)
	}
}

// 没有重名时必须原样返回（不产生任何改名，避免无谓的 diff 与困惑）。
func TestDedupeColumnNames_NoDuplicatePassthrough(t *testing.T) {
	in := []string{"id", "name", "created_at"}
	got := dedupeColumnNames(in)
	for i := range in {
		if got[i] != in[i] {
			t.Fatalf("expected passthrough, got %v", got)
		}
	}
}

// 极端情况：库里本来就有叫 "1.id" 的列时也不能撞名。
func TestDedupeColumnNames_CollisionWithGeneratedName(t *testing.T) {
	in := []string{"1.id", "id", "id"}
	got := dedupeColumnNames(in)
	seen := make(map[string]bool, len(got))
	for _, c := range got {
		if seen[c] {
			t.Fatalf("collision on generated name: %v", got)
		}
		seen[c] = true
	}
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
