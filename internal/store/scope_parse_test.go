package store

import (
	"testing"

	"github.com/opensourceprj/datawarden/internal/model"
)

// parseScopes 把 scope 字符串拆成结构化形态。
//
// 关键语义：整库（"shop" / "shop:*"）的 Tables 必须是 nil，
// 不能是 ["*"]——否则前端会渲染出一张叫 * 的表，
// 也让「整库」和「指定表清单」两种语义混在一起。
func TestParseScopes(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []model.ScopeView
	}{
		{
			name: "裸库名按整库处理",
			in:   []string{"shop"},
			want: []model.ScopeView{{Database: "shop", Tables: nil}},
		},
		{
			name: "库:* 也是整库",
			in:   []string{"shop:*"},
			want: []model.ScopeView{{Database: "shop", Tables: nil}},
		},
		{
			name: "表清单",
			in:   []string{"shop:orders,users"},
			want: []model.ScopeView{{Database: "shop", Tables: []string{"orders", "users"}}},
		},
		{
			name: "多个库",
			in:   []string{"shop:orders", "pay:*"},
			want: []model.ScopeView{
				{Database: "shop", Tables: []string{"orders"}},
				{Database: "pay", Tables: nil},
			},
		},
		{
			name: "同库多条合并",
			in:   []string{"shop:orders", "shop:users"},
			want: []model.ScopeView{{Database: "shop", Tables: []string{"orders", "users"}}},
		},
		{
			name: "同库整库+表 → 整库优先",
			in:   []string{"shop:orders", "shop:*"},
			want: []model.ScopeView{{Database: "shop", Tables: nil}},
		},
		{
			name: "同库整库+表（顺序反过来）",
			in:   []string{"shop", "shop:orders"},
			want: []model.ScopeView{{Database: "shop", Tables: nil}},
		},
		{
			name: "表名去重",
			in:   []string{"shop:orders", "shop:orders"},
			want: []model.ScopeView{{Database: "shop", Tables: []string{"orders"}}},
		},
		{
			name: "空输入返回空切片而非 nil",
			in:   nil,
			want: []model.ScopeView{},
		},
		{
			name: "忽略空白项",
			in:   []string{"", "  ", "shop:  , orders "},
			want: []model.ScopeView{{Database: "shop", Tables: []string{"orders"}}},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseScopes(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("parseScopes(%v) 返回 %d 项，want %d 项: %+v", c.in, len(got), len(c.want), got)
			}
			for i := range got {
				if got[i].Database != c.want[i].Database {
					t.Errorf("[%d] Database = %q, want %q", i, got[i].Database, c.want[i].Database)
				}
				// nil 与 空切片 在业务上是两种不同语义：nil=整库
				if (got[i].Tables == nil) != (c.want[i].Tables == nil) {
					t.Errorf("[%d] Tables nil 状态不符: got %v, want %v",
						i, got[i].Tables, c.want[i].Tables)
					continue
				}
				if len(got[i].Tables) != len(c.want[i].Tables) {
					t.Errorf("[%d] Tables = %v, want %v", i, got[i].Tables, c.want[i].Tables)
					continue
				}
				for j := range got[i].Tables {
					if got[i].Tables[j] != c.want[i].Tables[j] {
						t.Errorf("[%d] Tables[%d] = %q, want %q",
							i, j, got[i].Tables[j], c.want[i].Tables[j])
					}
				}
			}
		})
	}
}

// 库名本身带点（"my.data"）时必须整体作为库名，不能被点号切开。
func TestParseScopes_DottedDatabaseName(t *testing.T) {
	got := parseScopes([]string{"my.data:events"})
	if len(got) != 1 {
		t.Fatalf("want 1 scope, got %+v", got)
	}
	if got[0].Database != "my.data" {
		t.Errorf("Database = %q, want my.data", got[0].Database)
	}
	if len(got[0].Tables) != 1 || got[0].Tables[0] != "events" {
		t.Errorf("Tables = %v, want [events]", got[0].Tables)
	}
}

// 畸形 scope 不得出现在界面上。
//
// 入口（申请）已由 authz.NormalizeScope 拦截，但库里可能有历史脏数据。
// 若 parseScopes 自己宽松解析，"shop:*:*" 会显示成 {shop, ["*:*"]}，
// 用户看到一条语义不明的权限却无从自查。
func TestParseScopes_DropsMalformed(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []model.ScopeView
	}{
		{
			name: "多余冒号被丢弃",
			in:   []string{"shop:*:*"},
			want: []model.ScopeView{},
		},
		{
			name: "多段被丢弃",
			in:   []string{"a:b:c:d"},
			want: []model.ScopeView{},
		},
		{
			name: "库名为空被丢弃",
			in:   []string{":orders"},
			want: []model.ScopeView{},
		},
		{
			name: "纯通配被丢弃",
			in:   []string{"*"},
			want: []model.ScopeView{},
		},
		{
			name: "表名含通配符被丢弃",
			in:   []string{"shop:ord*"},
			want: []model.ScopeView{},
		},
		{
			name: "脏数据不影响合法数据",
			in:   []string{"shop:*:*", "shop:orders"},
			want: []model.ScopeView{{Database: "shop", Tables: []string{"orders"}}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseScopes(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("parseScopes(%v) = %+v, want %+v", c.in, got, c.want)
			}
			for i := range got {
				if got[i].Database != c.want[i].Database {
					t.Errorf("[%d] db = %q, want %q", i, got[i].Database, c.want[i].Database)
				}
				if len(got[i].Tables) != len(c.want[i].Tables) {
					t.Errorf("[%d] tables = %v, want %v", i, got[i].Tables, c.want[i].Tables)
				}
			}
		})
	}
}
