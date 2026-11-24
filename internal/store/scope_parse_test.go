package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/opensourceprj/datawarden/internal/model"
)

// Approve 不得把申请单的 scope 原样复制进 grant_record。
//
// 这是「申请粒度是数据源」的出口防线。Approve 一度写成
// `Scope: scope`（直接取 SELECT 出来的列），于是一条历史 pending 单
// 或手工插入的库级 scope 被批准后会重新产出一条只覆盖单库的授权——
// 审批通过、界面显示已授权，但用户换个库就是 403，且没有任何报错。
//
// 用源码断言而不是集成测试：Approve 依赖真实 PG（TEST_DATABASE_URL
// 缺省时整组测试是 Skip 的），而这条约定必须在任何环境下都被守住。
// 做法与 redisq 包里「强制不使用 KEYS」的源码扫描一致。
func TestApprove_ForcesAllDatabasesScope(t *testing.T) {
	approve := findFuncDecl(t, "grant_request_store.go", "Approve")

	// 找 model.Grant 复合字面量里的 Scope 字段，断言它是
	// authz.ScopeAllDatabases 常量，而不是某个标识符（scope 变量）
	var (
		found     bool
		scopeExpr ast.Expr
	)
	ast.Inspect(approve, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		// 必须是 model.Grant：只比对 Sel.Name == "Grant" 会误命中
		// 其它包下的同名类型（将来某个依赖也叫 Grant 就会让断言悄悄失效）。
		sel, ok := lit.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Grant" {
			return true
		}
		if pkgIdent, ok := sel.X.(*ast.Ident); !ok || pkgIdent.Name != "model" {
			return true
		}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Scope" {
				found = true
				scopeExpr = kv.Value
			}
		}
		return true
	})
	if !found {
		t.Fatal("Approve 里没有构造 model.Grant 的 Scope 字段，测试需随实现更新")
	}

	vs, ok := scopeExpr.(*ast.SelectorExpr)
	if !ok {
		t.Fatalf("Approve 的 Scope 必须是常量 authz.ScopeAllDatabases，"+
			"实际是 %T——直接取申请单的 scope 列会让历史库级申请绕过新口径", scopeExpr)
	}
	// X 不一定是 *ast.Ident（也可能是嵌套选择器），不能直接断言后取 .Name，
	// 否则会 nil 解引用把测试打成 panic 而不是给出可读的失败原因。
	pkg, ok := vs.X.(*ast.Ident)
	if !ok {
		t.Fatalf("Approve 的 Scope 表达式不是 authz.ScopeAllDatabases 形式，实际为 %T", vs.X)
	}
	if pkg.Name != "authz" || vs.Sel.Name != "ScopeAllDatabases" {
		t.Fatalf("Approve 的 Scope 必须是 authz.ScopeAllDatabases，实际是 %s.%s",
			pkg.Name, vs.Sel.Name)
	}
}

// 审批 SQL 不应再 SELECT scope 列：取出来不用会让读者以为它参与决策。
func TestApprove_DoesNotSelectScopeColumn(t *testing.T) {
	approve := findFuncDecl(t, "grant_request_store.go", "Approve")

	checked := 0
	ast.Inspect(approve, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil || !strings.Contains(s, "FOR UPDATE") {
			return true
		}
		checked++
		if strings.Contains(s, "scope") {
			t.Errorf("Approve 的加锁查询仍在 SELECT scope 列，但该值已不参与决策；"+
				"保留会让后续维护者误以为批准范围来自申请单。SQL=%s", s)
		}
		return true
	})
	// 扫描范围只在 Approve 函数体内，若一条 FOR UPDATE 都没找到，
	// 说明 SQL 被挪到别处了，这条断言也就失去了保护对象——必须报错。
	if checked == 0 {
		t.Error("Approve 里没找到含 FOR UPDATE 的查询，断言已失效，请随实现更新")
	}
}

// findFuncDecl 解析指定源文件并返回同名函数声明。
// 找不到直接 Fatal：所有基于源码的断言都依赖它存在。
func findFuncDecl(t *testing.T, file, name string) *ast.FuncDecl {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == name {
			return fn
		}
	}
	t.Fatalf("%s 中未找到函数 %s，测试需随实现更新", file, name)
	return nil
}

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

// 整数据源授权（"*"）是独立的一档，不是「库名叫 * 的整库」。
//
// 查询页要据此提示「整个数据源的所有库表」。若它被当成普通的库，
// 前端会显示「库 * 下所有表」，用户看不懂这指的是什么范围；
// 若它与历史库级授权并存且都返回，同一数据源下会显示出两个互不相容的
// 授权等级，用户无法判断自己的边界。
func TestParseScopes_AllDatabases(t *testing.T) {
	got := parseScopes([]string{"*"})
	if len(got) != 1 {
		t.Fatalf("want 1 scope, got %+v", got)
	}
	if got[0].Database != "*" || got[0].Tables != nil {
		t.Errorf("整源授权应表示为 {Database:*, Tables:nil}，got %+v", got[0])
	}

	// 与历史库级授权并存时，只保留整源这一档
	got = parseScopes([]string{"*", "shop:*", "pay:orders"})
	if len(got) != 1 || got[0].Database != "*" {
		t.Errorf("有整源授权时应只返回整源一档，got %+v", got)
	}

	// 整源授权排在后面时同样要合并掉库级明细。
	//
	// 这条防的是一个真实缺陷：早先的实现只判 `allIdx == 0`，
	// 于是 ["shop:*", "*"] 会同时返回两档，前端显示出两个互不相容的
	// 权限等级——而两个用户的实际权限完全相同，界面上却不一样。
	got = parseScopes([]string{"shop:*", "*"})
	if len(got) != 1 || got[0].Database != "*" {
		t.Errorf("整源授权排在后面时也应只返回整源一档，got %+v", got)
	}

	// 三条以上、整源夹在中间，同样只能剩一项
	got = parseScopes([]string{"shop:orders", "*", "pay:*"})
	if len(got) != 1 || got[0].Database != "*" {
		t.Errorf("整源授权夹在中间时也应只返回整源一档，got %+v", got)
	}

	// "*:*" 是整源授权的另一种写法，必须识别出来而不是当成畸形数据丢弃。
	//
	// authz.NormalizeScope("*:*") 返回空串（它只接受 "*"），照直用它
	// 会让这类记录在权限页上凭空消失——用户看到「我明明有授权」却查不到。
	// store 层因此单独走 normalizeForDisplay 认它。
	got = parseScopes([]string{"*:*"})
	if len(got) != 1 || got[0].Database != "*" {
		t.Errorf("*:* 应识别为整数据源授权，got %+v", got)
	}
	// 与库级授权并存时同样只保留整源
	got = parseScopes([]string{"shop:*", "*:*"})
	if len(got) != 1 || got[0].Database != "*" {
		t.Errorf("*:* 与库级授权并存时应只返回整源一档，got %+v", got)
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
			// "*:*" 不再算畸形：它是整源授权的另一种写法，由
			// normalizeForDisplay 单独识别（见 TestParseScopes_AllDatabases）。
			// 这里保留一条反向用例，防止有人误把它加回「丢弃」清单。
			name: "** 仍被丢弃",
			in:   []string{"**"},
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
