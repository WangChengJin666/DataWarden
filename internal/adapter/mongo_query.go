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

	// 取参数部分（括号内到末尾）
	argsStart := strings.Index(input, "(")
	args := input[argsStart:]

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
