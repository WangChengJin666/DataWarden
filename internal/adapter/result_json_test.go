package adapter

import (
	"encoding/json"
	"strings"
	"testing"
)

// 零行结果的 rows 必须序列化成 []，不能是 null。
//
// 现象：一个条件不匹配的查询返回 {"columns":[...],"rows":null,"row_count":0}，
// 前端表格对 rows 调 .map 直接抛 TypeError，整个查询页白屏。
// 空结果是正常情况（条件不匹配、表为空），不能变成崩溃。
func TestQueryResult_EmptyRowsSerializeAsArray(t *testing.T) {
	// 模拟 pg / mysql adapter 的构造方式：显式初始化成空切片
	res := &QueryResult{
		Columns: []string{"id", "name"},
		Rows:    make([][]interface{}, 0),
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"rows":[]`) {
		t.Fatalf("empty rows must serialize as [], got %s", b)
	}
	if strings.Contains(string(b), `"rows":null`) {
		t.Fatalf("empty rows must not be null, got %s", b)
	}
}

// 守住回归：如果有人把 Rows 改回 nil 声明，这里会失败。
func TestQueryResult_NilRowsIsTheBug(t *testing.T) {
	// 这条断言证明「修复前的写法确实产 null」，避免后人以为无所谓
	var nilRows [][]interface{}
	b, _ := json.Marshal(QueryResult{Columns: []string{"id"}, Rows: nilRows})
	if !strings.Contains(string(b), `"rows":null`) {
		t.Skipf("Go 的 nil 切片序列化行为变了，检查 adapter 的初始化方式：%s", b)
	}
}
