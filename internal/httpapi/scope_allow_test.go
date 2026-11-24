package httpapi

import (
	"testing"
)

// --- splitScope：整库 scope 的语义归一 ---

// "*" 必须被当作「整库」而不是一张名为 * 的表。
//
// 若不处理：元数据接口的 tablesFor 会把 "*" 追加进表下拉
// （界面上多出一项叫 *），整库授权永远匹配不上。
func TestSplitScope(t *testing.T) {
	cases := []struct {
		in     string
		wantDB string
		wantT  []string
	}{
		{"shop", "shop", nil},
		{"shop:*", "shop", nil},
		{"shop:orders", "shop", []string{"orders"}},
		{"shop:orders,users", "shop", []string{"orders", "users"}},
		{"shop: orders , users ", "shop", []string{"orders", "users"}},
		{" shop ", "shop", nil},
		{"", "", nil},
	}
	for _, c := range cases {
		db, tables := splitScope(c.in)
		if db != c.wantDB {
			t.Errorf("splitScope(%q) db = %q, want %q", c.in, db, c.wantDB)
		}
		if len(tables) != len(c.wantT) {
			t.Errorf("splitScope(%q) tables = %v, want %v", c.in, tables, c.wantT)
			continue
		}
		for i := range tables {
			if tables[i] != c.wantT[i] {
				t.Errorf("splitScope(%q) tables[%d] = %q, want %q",
					c.in, i, tables[i], c.wantT[i])
			}
		}
	}
}

// --- 越权边界：不能靠 scope 里的点号跳库 ---

// 这一组守的是 Mongo 集合名带库前缀（"shop.users"）时鉴权对象的拼接问题。
// querysvc 里有对应实现，这里只验证 scope 侧的归类不会把
// "shop.users" 误当成「库里有个叫 shop.users 的表」。
func TestSplitScope_DottedTableName(t *testing.T) {
	db, tables := splitScope("shop:shop.users")
	if db != "shop" {
		t.Errorf("db = %q, want shop", db)
	}
	if len(tables) != 1 || tables[0] != "shop.users" {
		t.Errorf("tables = %v, want [shop.users]", tables)
	}
}
