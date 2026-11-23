package httpapi

import (
	"testing"

	"github.com/opensourceprj/datawarden/internal/model"
)

// --- splitScope：整库 scope 的语义归一 ---

// "*" 必须被当作「整库」而不是一张名为 * 的表。
//
// 若不处理：tablesFor 会把 "*" 追加进表下拉（界面上多出一项叫 *），
// scopeAllowed 会去找一张叫 "*" 的表，整库配置永远匹配不上。
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

// --- scopeAllowed：管理员开放范围 与 用户申请范围 的比对 ---

func TestScopeAllowed(t *testing.T) {
	// 管理员配置：shop 整库 + pay 只开放 orders/trades
	allowed := []model.ApplyScope{
		{Database: "shop", Tables: nil},
		{Database: "pay", Tables: []string{"orders", "trades"}},
	}

	cases := []struct {
		scope string
		want  bool
		why   string
	}{
		{"shop", true, "整库申请，管理员开放了整库"},
		{"shop:*", true, "整库的另一种写法，必须等价"},
		{"shop:orders", true, "整库开放时任意表都可申请"},
		{"pay:orders", true, "命中白名单表"},
		{"pay:orders,trades", true, "多个表都在白名单里"},
		{"pay:orders,refunds", false, "有一张表不在白名单，整条拒绝"},
		{"pay", false, "只开放了部分表，不能申请整库"},
		{"pay:*", false, "整库写法同样不能绕过部分表的限制"},
		{"other", false, "库不在白名单"},
		{"", false, "空 scope 拒绝"},
	}

	for _, c := range cases {
		if got := scopeAllowed(allowed, c.scope); got != c.want {
			t.Errorf("scopeAllowed(%q) = %v, want %v (%s)", c.scope, got, c.want, c.why)
		}
	}
}

// 管理员没配任何范围时，任何申请都不该通过。
func TestScopeAllowed_EmptyAllowedList(t *testing.T) {
	if scopeAllowed(nil, "shop") {
		t.Error("未配置可申请范围时不能放行任何申请")
	}
	if scopeAllowed([]model.ApplyScope{}, "shop:*") {
		t.Error("空切片同样不能放行")
	}
}

// 管理员配置了"全部表"（Tables 为空）时，表级申请也要放行。
func TestScopeAllowed_WholeDatabaseAllowsTableLevel(t *testing.T) {
	allowed := []model.ApplyScope{{Database: "shop"}}
	if !scopeAllowed(allowed, "shop:orders") {
		t.Error("整库开放时，申请单表应当通过")
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
