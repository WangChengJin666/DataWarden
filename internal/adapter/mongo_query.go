package adapter

import (
	"fmt"
	"regexp"
	"strings"
)

// MongoQuery 是解析后的 MongoDB 查询。
//
// MongoDB 不认 SQL，用户可能写两种形式：
//  1. 原生命令：db.users.find({"status":"active"}).limit(10)
//  2. 集合 + 过滤条件：由上层（HTTP）把 collection/filter 组合进来
//
// 无论哪种，最终都要经过这里做只读校验，绝不允许写操作穿透。
type MongoQuery struct {
	// Collection 是目标集合，可能带库前缀（db.coll 形式时）。
	Collection string
	// Op 是操作名，如 find / findOne / countDocuments / aggregate / distinct。
	Op string
	// Args 是操作参数原文，交给 mongo shell 解析成 BSON。
	Args string
	// Raw 是原始输入，用于审计。
	Raw string
}

// SplitDBCollection 把集合名拆成 (库, 集合)。
//
// MongoDB 的 shell 写法允许 db.shop.users.find(...)，解析出来是 "shop.users"。
// 「哪部分算库」这件事必须**只有一份实现**：执行层（QueryMongo 用它决定
// 查哪个库）和鉴权层（querysvc / httpapi 用它拼 ds:<id>:<库>:<集合>）
// 必须拆出完全相同的结果。两份实现就是两条会各自漂移的规则，
// 一旦不一致，就可能出现「鉴权放行了 A 库、实际查到 B 库」的越权。
//
// 规则：取最后一个点号之前为库、之后为集合。
//
// 为什么用最后一个点号而不是第一个：库名本身允许带点
// （"system.js" 是常见写法，"my.data" 也合法）。只有集合名不允许含点，
// 所以最后一个点号之后一定是集合名。
//
// 边界：无点号时库用传入的 database（可能为空，由调用方报错）。
func SplitDBCollection(collection, database string) (db, coll string, err error) {
	coll = collection
	db = database
	if i := strings.LastIndex(collection, "."); i > 0 {
		db = collection[:i]
		coll = collection[i+1:]
	}
	if coll == "" {
		return "", "", fmt.Errorf("集合名为空")
	}
	return db, coll, nil
}

// Signature 返回用于审计的简短签名。
func (q *MongoQuery) Signature() string {
	return q.Collection + "." + q.Op
}

// mongoReadOps 是允许的只读操作白名单。
// 用白名单而不是黑名单：MongoDB 的操作集合会随版本增加，
// 黑名单漏一个就是一次写权限泄漏。
var mongoReadOps = map[string]bool{
	"find":                   true,
	"findOne":                true,
	"countDocuments":         true,
	"count":                  true,
	"distinct":               true,
	"aggregate":              true, // 需额外检查 $out / $merge
	"estimatedDocumentCount": true,
}

// mongoWriteMarkers 是即使出现在 aggregate 管道里也必须拒绝的写阶段。
var mongoWriteMarkers = []string{"$out", "$merge"}

// mongoShellRe 匹配 db.<collection>.<op>( 形式，允许 collection 带库前缀与点号。
// 例如 db.users.find( / db.shop.users.find( / db.getCollection("sys.log").find(
var mongoShellRe = regexp.MustCompile(`^\s*db\.(?:getCollection\(\s*["']([\w.\-]+)["']\s*\)|([\w.\-]+))\.(\w+)\s*\(`)

// ParseMongoQuery 解析并校验一条 MongoDB 查询，写操作直接拒绝。
func ParseMongoQuery(raw string) (*MongoQuery, error) {
	input := strings.TrimSpace(raw)
	// 去掉结尾分号，shell 习惯写法
	input = strings.TrimSuffix(input, ";")
	input = strings.TrimSpace(input)

	if input == "" {
		return nil, fmt.Errorf("查询不能为空")
	}

	m := mongoShellRe.FindStringSubmatch(input)
	if m == nil {
		// 允许 "db.coll.op(...)" 之外还有 "users.find(...)" 这种省略 db. 的写法吗？
		// 不允许：省略后无法确定是库级命令还是集合操作，交给上层用集合+过滤条件模式。
		return nil, fmt.Errorf("无法识别的 MongoDB 命令格式，期望 db.<集合>.<操作>(...)")
	}

	collection := m[1]
	if collection == "" {
		collection = m[2]
	}
	op := m[3]

	if !mongoReadOps[op] {
		return nil, fmt.Errorf("不允许的 MongoDB 操作 %q：平台只支持查询", op)
	}

	// 取参数部分：从「操作名后面的那个括号」开始，而不是字符串里第一个括号。
	//
	// 不能用 strings.Index(input, "(")：db.getCollection("x").find(...) 里
	// 第一个括号属于 getCollection，截出来的 Args 会变成 ("x").find(...)，
	// 下游把 "x" 当成过滤条件解析，报「过滤条件不是合法的 JSON」。
	// 这里用正则匹配到的完整前缀长度定位，两种写法都正确。
	prefixEnd := strings.Index(input, m[0])
	if prefixEnd < 0 {
		prefixEnd = 0
	}
	args := input[prefixEnd+len(m[0])-1:]

	lower := strings.ToLower(args)
	for _, marker := range mongoWriteMarkers {
		if strings.Contains(lower, strings.ToLower(marker)) {
			return nil, fmt.Errorf("不允许的聚合阶段 %s：它会写入数据", marker)
		}
	}

	// 兜底：即使操作名合法，参数里也不该出现写方法调用
	for _, w := range []string{"insertone(", "insertmany(", "updateone(", "updatemany(",
		"replaceone(", "deleteone(", "deletemany(", "drop(", "dropdatabase("} {
		if strings.Contains(lower, w) {
			return nil, fmt.Errorf("参数中出现写操作 %s，已拒绝", strings.TrimSuffix(w, "("))
		}
	}

	return &MongoQuery{
		Collection: collection,
		Op:         op,
		Args:       args,
		Raw:        input,
	}, nil
}
