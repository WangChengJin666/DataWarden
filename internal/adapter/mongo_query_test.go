package adapter

import (
	"strings"
	"testing"
)

func TestParseMongoQuery_ShellForm(t *testing.T) {
	cases := []struct {
		in       string
		op       string
		coll     string
		wantErr  bool
		filterJS string
	}{
		{in: `db.users.find({"status":"active"})`, op: "find", coll: "users", filterJS: `{"status":"active"}`},
		{in: `db.users.findOne({ _id: 1 })`, op: "findOne", coll: "users"},
		{in: `db.users.find()`, op: "find", coll: "users"},
		{in: `db.users.countDocuments({"age":{"$gt":18}})`, op: "countDocuments", coll: "users"},
		{in: `db.users.distinct("city")`, op: "distinct", coll: "users"},
		{in: `db.users.aggregate([{"$match":{"a":1}}])`, op: "aggregate", coll: "users"},
		// 允许 db.getCollection("带点名字").find()
		{in: `db.getCollection("sys.log").find({})`, op: "find", coll: "sys.log"},
		// 分号结尾
		{in: `db.users.find({}).limit(10);`, op: "find", coll: "users"},
		// 带 db 前缀的完整写法
		{in: `db.shop.users.find({})`, op: "find", coll: "shop.users"},
	}

	for _, c := range cases {
		q, err := ParseMongoQuery(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("input %q should be rejected", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("input %q should parse, got %v", c.in, err)
			continue
		}
		if q.Op != c.op {
			t.Errorf("input %q op = %q, want %q", c.in, q.Op, c.op)
		}
		if q.Collection != c.coll {
			t.Errorf("input %q collection = %q, want %q", c.in, q.Collection, c.coll)
		}
	}
}

func TestParseMongoQuery_RejectsWrites(t *testing.T) {
	bad := []string{
		`db.users.insertOne({a:1})`,
		`db.users.insertMany([{a:1}])`,
		`db.users.updateOne({a:1},{$set:{b:2}})`,
		`db.users.updateMany({},{$set:{b:2}})`,
		`db.users.replaceOne({a:1},{b:2})`,
		`db.users.deleteOne({a:1})`,
		`db.users.deleteMany({})`,
		`db.users.remove({})`,
		`db.users.drop()`,
		`db.dropDatabase()`,
		`db.users.createIndex({a:1})`,
		`db.users.dropIndex("a_1")`,
		`db.users.renameCollection("x")`,
		`db.createCollection("x")`,
		// $out / $merge 会写库
		`db.users.aggregate([{"$match":{}},{"$out":"copy"}])`,
		`db.users.aggregate([{"$merge":{"into":"copy"}}])`,
		// 非 db. 开头
		`show collections`,
		`users.find({})`,
		``,
	}

	for _, in := range bad {
		if q, err := ParseMongoQuery(in); err == nil {
			t.Errorf("input %q must be rejected, but parsed as op=%s coll=%s", in, q.Op, q.Collection)
		}
	}
}

// 写关键字藏在字符串常量里时不应误判，但藏在 JSON 里的 $out 必须拦住。
func TestParseMongoQuery_OutInsideString(t *testing.T) {
	// 查询内容里出现 "delete" 字样不应被当成写操作
	in := `db.logs.find({"msg":"delete failed"})`
	if _, err := ParseMongoQuery(in); err != nil {
		t.Fatalf("string literal containing 'delete' must not be rejected: %v", err)
	}
}

func TestParseMongoQuery_Signature(t *testing.T) {
	q, err := ParseMongoQuery(`db.users.find({"a":1})`)
	if err != nil {
		t.Fatal(err)
	}
	// 签名应包含集合与操作，用于审计
	if !strings.Contains(q.Signature(), "users.find") {
		t.Errorf("signature = %q, should contain users.find", q.Signature())
	}
}

// 回归测试：Args 必须从「操作名后面的括号」开始，而不是整串里的第一个括号。
//
// 曾经用 strings.Index(input, "(") 截取，遇到 db.getCollection("x").find(...)
// 时把 getCollection 的括号当成了起点，Args 变成 ("x").find(...)，
// 下游把 "x" 当过滤条件解析并报「不是合法的 JSON」。
func TestParseMongoQuery_ArgsStartAtOpParen(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`db.users.find({"a":1})`, `({"a":1})`},
		{`db.getCollection("users").find({"a":1})`, `({"a":1})`},
		{`db.getCollection("sys.log").find({})`, `({})`},
		{`db.getCollection("sys.log").find({}).limit(5)`, `({}).limit(5)`},
		{`db.shop.users.find({"a":1})`, `({"a":1})`},
	}
	for _, c := range cases {
		q, err := ParseMongoQuery(c.in)
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.in, err)
			continue
		}
		if q.Args != c.want {
			t.Errorf("%s: args = %q, want %q", c.in, q.Args, c.want)
		}
	}
}

// getCollection 形式的过滤条件必须能被真正解析成 bson，而不是止步于正则。
func TestParseMongoQuery_GetCollectionFilterParses(t *testing.T) {
	q, err := ParseMongoQuery(`db.getCollection("users").find({"status":"active"})`)
	if err != nil {
		t.Fatal(err)
	}
	filter, _, err := parseMongoCallArgs(q.Args, q.Op)
	if err != nil {
		t.Fatalf("filter should parse: %v", err)
	}
	if len(filter) != 1 || filter[0].Key != "status" {
		t.Fatalf("filter = %v, want status key", filter)
	}
}
