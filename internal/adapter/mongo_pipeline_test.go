package adapter

import (
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// 回归测试：用户直接贴 mongosh 里的聚合管道（带 NumberLong 等 shell 字面量）
// 必须能执行，而不是报「invalid JSON input. Position: 6. Character: $」。
//
// 那个报错位置指向 $match 的 $，真正的问题是前面的 NumberLong("...")：
// UnmarshalExtJSON 不认识 shell 调用，报错位置又毫无帮助，用户只能来问。
func TestParseMongoPipeline_ShellLiteral(t *testing.T) {
	raw := `db.integralTypeCollect.aggregate([
{ $match: { projectId: NumberLong("14759057188135424") } },
{ $group: { _id: { projectId: "$projectId", status: "$status" }, totalScore: { $sum: "$status" } } },
{ $sort: { "_id.status": 1 } }
])`

	q, err := ParseMongoQuery(raw)
	if err != nil {
		t.Fatalf("ParseMongoQuery: %v", err)
	}
	pipeline, err := parseMongoPipeline(q.Args)
	if err != nil {
		t.Fatalf("parseMongoPipeline: %v", err)
	}
	if len(pipeline) != 3 {
		t.Fatalf("stages = %d, want 3", len(pipeline))
	}

	// NumberLong 必须落成 int64 且精度不丢，否则 $match 匹配不到任何文档，
	// 用户会以为「数据源没数据」。
	match := pipeline[0]
	if len(match) != 1 || match[0].Key != "$match" {
		t.Fatalf("stage0 = %v, want $match", match)
	}
	cond, ok := match[0].Value.(bson.D)
	if !ok {
		t.Fatalf("$match value type %T, want bson.D", match[0].Value)
	}
	var got int64
	for _, kv := range cond {
		if kv.Key == "projectId" {
			got, ok = kv.Value.(int64)
			if !ok {
				t.Fatalf("projectId type %T, want int64", kv.Value)
			}
		}
	}
	if got != 14759057188135424 {
		t.Fatalf("projectId = %d, want 14759057188135424", got)
	}
}

// 未加引号的 NumberLong 也要认：mongosh 里两种写法都合法。
func TestParseMongoPipeline_NumberLongBare(t *testing.T) {
	pipeline, err := parseMongoPipeline(`([{ "$match": { "a": NumberLong(42) } }])`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pipeline) != 1 {
		t.Fatalf("stages = %d, want 1", len(pipeline))
	}
}

// 管道后面的 options 不能被当成管道内容，否则解析直接失败。
func TestParseMongoPipeline_WithOptions(t *testing.T) {
	pipeline, err := parseMongoPipeline(`([{"$match":{}}], {"allowDiskUse":true})`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pipeline) != 1 {
		t.Fatalf("stages = %d, want 1", len(pipeline))
	}
}

// 字符串常量里的 ] 不能提前结束管道截取。
func TestParseMongoPipeline_BracketInsideString(t *testing.T) {
	pipeline, err := parseMongoPipeline(`([{"$match":{"tag":"a]b"}},{"$limit":1}])`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pipeline) != 2 {
		t.Fatalf("stages = %d, want 2", len(pipeline))
	}
}

// 写阶段即使在字面量转换之后也必须被拦住——转换不能成为绕过 $out 的通道。
func TestParseMongoPipeline_WritesStillRejected(t *testing.T) {
	for _, in := range []string{
		`([{"$match":{"a":NumberLong("1")}},{"$out":"copy"}])`,
		`([{"$merge":{"into":"copy"}}])`,
	} {
		if _, err := parseMongoPipeline(in); err == nil {
			t.Errorf("%s must be rejected", in)
		}
	}
}

// 字符串里出现 $out 字样不该被误判（沿用原有整串扫描行为的一致性检查）。
func TestParseMongoPipeline_StringLiteralNotTouched(t *testing.T) {
	// 字符串里的 NumberLong 字样保持原样，不被替换成对象
	got, err := shellExtJSON(`{"msg":"NumberLong(1)","real":NumberInt(2)}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, `"msg":"NumberLong(1)"`) {
		t.Fatalf("string literal was rewritten: %s", got)
	}
	if !strings.Contains(got, `{"$numberInt":"2"}`) {
		t.Fatalf("NumberInt not converted: %s", got)
	}
}

// 常见字面量都要能转，且转出来的东西必须能被扩展 JSON 解析器接受。
func TestShellExtJSON_KnownLiterals(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`NumberLong("12")`, `{"$numberLong":"12"}`},
		{`NumberInt(7)`, `{"$numberInt":"7"}`},
		{`NumberDecimal("1.5")`, `{"$numberDecimal":"1.5"}`},
		{`ObjectId("507f1f77bcf86cd799439011")`, `{"$oid":"507f1f77bcf86cd799439011"}`},
		{`ISODate("2024-01-01T00:00:00Z")`, `{"$date":"2024-01-01T00:00:00Z"}`},
		{`new Date(1700000000000)`, `{"$date":{"$numberLong":"1700000000000"}}`},
		{`Date("2024-01-01T00:00:00Z")`, `{"$date":"2024-01-01T00:00:00Z"}`},
		// 未知调用原样保留
		{`{"$sum":"$status"}`, `{"$sum":"$status"}`},
	}
	for _, c := range cases {
		got, err := shellExtJSON(c.in)
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: got %s, want %s", c.in, got, c.want)
		}
		// 转换结果能被扩展 JSON 解析
		var v interface{}
		if err := bson.UnmarshalExtJSON([]byte(got), false, &v); err != nil {
			t.Errorf("%s: converted %s not valid extJSON: %v", c.in, got, err)
		}
	}
}

// 未知字面量（Timestamp 等）保持原样，不做猜测性转换。
func TestShellExtJSON_UnknownLeftAlone(t *testing.T) {
	got, err := shellExtJSON(`{"t":Timestamp(1,2)}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != `{"t":Timestamp(1,2)}` {
		t.Fatalf("got %s, want untouched", got)
	}
}

// 裸键与单引号是 mongosh 常见写法，必须能等价成严格 JSON。
func TestQuoteBareKeys(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{ $match: { a: 1 } }`, `{ "$match": { "a": 1 } }`},
		{`{"$sort":{_id.status:1}}`, `{"$sort":{"_id.status":1}}`},
		{`{'a':'b c'}`, `{"a":"b c"}`},
		{`[{"$match":{"tag":"a, b"}}]`, `[{"$match":{"tag":"a, b"}}]`},
	}
	for _, c := range cases {
		got, err := quoteBareKeys(c.in)
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: got %s, want %s", c.in, got, c.want)
		}
	}
}

// 名字相近但不是字面量的调用不能被误替换（MyNumberLong( 之类）。
func TestShellExtJSON_NoPartialMatch(t *testing.T) {
	got, err := shellExtJSON(`{"a":MyNumberLong(1)}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, `MyNumberLong(1)`) {
		t.Fatalf("partial match happened: %s", got)
	}
}

// 参数非法时要给出能看懂的报错，而不是原始的解析器噪音。
func TestShellExtJSON_BadArgs(t *testing.T) {
	for _, in := range []string{
		`NumberLong("abc")`,
		`NumberLong()`,
		`ObjectId()`,
		`new Date(2024, 1, 1)`,
	} {
		if _, err := shellExtJSON(in); err == nil {
			t.Errorf("%s should fail", in)
		}
	}
}

// find 的过滤条件同样要支持 shell 字面量。
func TestParseMongoCallArgs_ShellLiteral(t *testing.T) {
	filter, _, err := parseMongoCallArgs(`({"_id":ObjectId("507f1f77bcf86cd799439011")})`, "find")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(filter) != 1 || filter[0].Key != "_id" {
		t.Fatalf("filter = %v", filter)
	}
}

// 管道不是数组时必须给出可操作的提示，不能只抛解析器原文。
func TestParseMongoPipeline_NotArray(t *testing.T) {
	_, err := parseMongoPipeline(`({"$match":{}})`)
	if err == nil || !strings.Contains(err.Error(), "聚合管道必须是数组") {
		t.Fatalf("err = %v, want 提示管道必须是数组", err)
	}
}
