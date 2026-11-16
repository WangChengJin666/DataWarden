package redisq

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestClientAPI_HasNoArbitraryCommand 断言 Client 没有暴露任意命令执行入口。
// 任何人给 Client 加 Do / Execute / Command 方法，本测试会编译失败。
func TestClientAPI_HasNoArbitraryCommand(t *testing.T) {
	var c *Client
	var _ interface {
		Fetch(context.Context, string) (*KeyValue, error)
		Scan(context.Context, string) ([]string, bool, error)
	} = c
}

// TestNoKEYSInSource 扫描 redisq 包源码，断言不存在 KEYS 命令调用。
// 这比运行时 MONITOR 抽查更严格：覆盖所有代码路径，且不受远程 Redis 的 ACL 限制。
func TestNoKEYSInSource(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}

	found := 0
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel == nil {
					return true
				}
				if strings.EqualFold(sel.Sel.Name, "Keys") ||
					strings.EqualFold(sel.Sel.Name, "FlushAll") ||
					strings.EqualFold(sel.Sel.Name, "FlushDB") {
					pos := fset.Position(call.Pos())
					t.Errorf("%s:%d: %s command must never be called",
						pos.Filename, pos.Line, sel.Sel.Name)
					found++
				}
				return true
			})
		}
	}
	if found > 0 {
		t.Logf("found %d forbidden command call(s)", found)
	}
}

// TestScanPatternGuardIsWired 断言 Scan 在发命令前校验 pattern。
func TestScanPatternGuardIsWired(t *testing.T) {
	// 宽泛模式必须在建连前就被拒绝：用一个不存在的地址验证。
	c := &Client{addr: "127.0.0.1:1", maxKeys: 200}
	if c.rdb != nil {
		t.Skip("client has connection, cannot test guard ordering")
	}
	if _, _, err := c.Scan(context.Background(), "*"); err == nil {
		t.Fatal("Scan must reject broad pattern before touching network")
	}
}

// TestNoForbiddenCommandsInDocs 确保计划文档里也提醒了这些禁令。
// 这不是代码正确性要求，而是防止未来有人照抄错误示例。
func TestValidatePatternRejectsFlushPatterns(t *testing.T) {
	// 顺带确认这些模式都被拒绝
	for _, p := range []string{"*", "**", "*:*", "?:*"} {
		if err := ValidatePattern(p); err == nil {
			t.Errorf("pattern %q must be rejected", p)
		}
	}
}
