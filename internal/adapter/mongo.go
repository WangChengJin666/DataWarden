package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// mongoAdapter 实现 Adapter 接口中的探测与元数据能力。
//
// 注意：MongoDB 不支持 SQL，Query 方法会返回错误。真正的查询走
// QueryMongo。这样既满足接口一致性（连接测试、列表都能复用），
// 又不会让人误以为可以把 SQL 丢给 Mongo。
type mongoAdapter struct {
	client *mongo.Client
	// dbName 是默认库，Ping 时用它做一次权限校验
	dbName string
}

func newMongo(cfg ConnConfig) (Adapter, error) {
	if cfg.Database == "" {
		return nil, fmt.Errorf("MongoDB 必须填写默认库，否则无法确定要查哪个库")
	}
	if cfg.Port == 0 {
		cfg.Port = 27017
	}

	// 认证库与目标库分开：用户可能建在 admin 库但查业务库。
	// 默认用 authSource=admin，这是 MongoDB 最常见的管理员配置。
	uri := fmt.Sprintf("mongodb://%s:%d/?authSource=admin",
		cfg.Host, cfg.Port)

	opts := options.Client().
		ApplyURI(uri).
		SetServerSelectionTimeout(8 * time.Second).
		SetConnectTimeout(8 * time.Second)

	if cfg.User != "" {
		opts.SetAuth(options.Credential{
			Username:   cfg.User,
			Password:   cfg.Password,
			AuthSource: "admin",
		})
	} else if cfg.Password != "" {
		// 无账号但有密码：极少数配置，按无认证处理会静默失败，
		// 明确报错比连不上却看不出原因好。
		return nil, fmt.Errorf("MongoDB 填写了密码但未填账号，请补上账号")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := mongo.Connect(opts)
	if err != nil {
		return nil, fmt.Errorf("mongo connect: %w", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("mongo ping: %w", err)
	}

	return &mongoAdapter{client: client, dbName: cfg.Database}, nil
}

func (a *mongoAdapter) Ping(ctx context.Context) error {
	if err := a.client.Ping(ctx, nil); err != nil {
		return err
	}
	// Ping 只能证明服务端可达，不能证明有权限：
	// 在开启认证的实例上，未认证的 Ping 依然返回成功，
	// 直到执行真实命令才报 Unauthorized。
	// 所以这里补一次轻量读命令，让「测试连接」的结果可信。
	var res bson.M
	if err := a.client.Database(a.dbName).RunCommand(ctx, bson.D{
		{Key: "listCollections", Value: 1},
		{Key: "nameOnly", Value: true},
	}).Decode(&res); err != nil {
		return fmt.Errorf("mongo permission check: %w", err)
	}
	return nil
}

// ListDatabases 列出所有库名。
func (a *mongoAdapter) ListDatabases(ctx context.Context) ([]string, error) {
	names, err := a.client.ListDatabaseNames(ctx, bson.M{})
	if err != nil {
		return nil, fmt.Errorf("mongo list databases: %w", err)
	}
	return names, nil
}

// ListTables 在 MongoDB 里对应集合名。
// db 为空时退回 admin 库，避免空库名导致驱动报错。
func (a *mongoAdapter) ListTables(ctx context.Context, db string) ([]string, error) {
	if db == "" {
		db = "admin"
	}
	names, err := a.client.Database(db).ListCollectionNames(ctx, bson.M{})
	if err != nil {
		return nil, fmt.Errorf("mongo list collections: %w", err)
	}
	return names, nil
}

// Query 不支持：MongoDB 没有 SQL 接口。请使用 QueryMongo。
func (a *mongoAdapter) Query(ctx context.Context, sql string, rowLimit int) (*QueryResult, error) {
	return nil, fmt.Errorf("MongoDB 不支持 SQL 查询，请使用集合查询")
}

func (a *mongoAdapter) Close() error {
	return a.client.Disconnect(context.Background())
}

// MongoQueryRequest 是一次集合查询请求。
type MongoQueryRequest struct {
	// Database 是目标库，空则用连接时的默认库。
	Database string
	// Collection 是集合名，可带 "db.coll" 前缀。
	Collection string
	// Op 是操作名，空默认为 find。
	Op string
	// Args 是参数原文，如 {"status":"active"}。
	// 若为空且 Op 为 find，则表示查询全部（仍受限行约束）。
	Args string
	// Limit 是最大返回条数。
	Limit int
}

// mongoFindArgs 解析 find( 的参数，支持两种写法：
//
//	find({"a":1})            过滤条件
//	find({"a":1}, {"b":1})   过滤条件 + 投影
func parseMongoCallArgs(args string, op string) (bson.D, bson.D, error) {
	inner := strings.TrimSpace(args)
	if strings.HasPrefix(inner, "(") {
		inner = strings.TrimPrefix(inner, "(")
	}
	// 去掉末尾的 ) 与链式调用（.limit(10).sort({})）
	if i := strings.LastIndex(inner, ")"); i >= 0 {
		inner = inner[:i]
	}
	// 链式调用只取第一个参数组
	if i := strings.Index(inner, ")."); i >= 0 {
		inner = inner[:i]
	}
	inner = strings.TrimSpace(inner)

	if inner == "" {
		return bson.D{}, bson.D{}, nil
	}

	parts := splitTopLevelArgs(inner)
	var filter, projection bson.D
	if len(parts) > 0 && strings.TrimSpace(parts[0]) != "" {
		if err := bson.UnmarshalExtJSON([]byte(parts[0]), false, &filter); err != nil {
			return nil, nil, fmt.Errorf("过滤条件不是合法的 JSON/BSON：%w", err)
		}
	}
	if len(parts) > 1 && strings.TrimSpace(parts[1]) != "" {
		if err := bson.UnmarshalExtJSON([]byte(parts[1]), false, &projection); err != nil {
			return nil, nil, fmt.Errorf("投影条件不是合法的 JSON/BSON：%w", err)
		}
	}
	return filter, projection, nil
}

// splitTopLevelArgs 按顶层逗号切分参数，忽略 JSON 对象/数组/字符串里的逗号。
func splitTopLevelArgs(s string) []string {
	var out []string
	var cur strings.Builder
	depth := 0
	inStr := false
	var quote rune

	for _, r := range s {
		switch {
		case inStr:
			cur.WriteRune(r)
			if r == quote {
				inStr = false
			}
			continue
		case r == '"' || r == '\'':
			inStr = true
			quote = r
			cur.WriteRune(r)
		case r == '{' || r == '[' || r == '(':
			depth++
			cur.WriteRune(r)
		case r == '}' || r == ']' || r == ')':
			depth--
			cur.WriteRune(r)
		case r == ',' && depth == 0:
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		out = append(out, s)
	}
	return out
}

// QueryMongo 执行一次只读查询，返回统一的 QueryResult。
//
// 这里不接收原始命令行，而是接收已解析好的结构化请求：
// 命令的合法性由 ParseMongoQuery 在 HTTP 层校验，避免把解析职责混进适配器。
func QueryMongo(ctx context.Context, ad Adapter, req MongoQueryRequest) (*QueryResult, error) {
	m, ok := ad.(*mongoAdapter)
	if !ok {
		return nil, fmt.Errorf("adapter is not a mongo adapter")
	}

	op := req.Op
	if op == "" {
		op = "find"
	}
	if !mongoReadOps[op] {
		return nil, fmt.Errorf("不允许的 MongoDB 操作 %q", op)
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 100
	}

	collName := req.Collection
	var dbName string
	// 支持 shop.users 这种带库前缀的写法
	if i := strings.LastIndex(collName, "."); i > 0 {
		dbName = collName[:i]
		collName = collName[i+1:]
	}
	if dbName == "" {
		dbName = req.Database
	}
	if dbName == "" {
		return nil, fmt.Errorf("未指定数据库")
	}
	if collName == "" {
		return nil, fmt.Errorf("未指定集合名")
	}

	coll := m.client.Database(dbName).Collection(collName)
	start := time.Now()

	// limit+1 用于判断是否被截断
	fetchLimit := int64(limit) + 1

	switch op {
	case "find", "findOne":
		filter, projection, err := parseMongoCallArgs(req.Args, op)
		if err != nil {
			return nil, err
		}
		opts := options.Find().SetLimit(fetchLimit)
		if len(projection) > 0 {
			opts.SetProjection(projection)
		}
		if op == "findOne" {
			opts.SetLimit(1)
		}

		cur, err := coll.Find(ctx, filter, opts)
		if err != nil {
			return nil, fmt.Errorf("mongo find: %w", err)
		}
		defer cur.Close(ctx)

		var docs []bson.D
		if err := cur.All(ctx, &docs); err != nil {
			return nil, fmt.Errorf("mongo decode: %w", err)
		}
		return mongoDocsToResult(docs, limit, start), nil

	case "countDocuments", "count", "estimatedDocumentCount":
		filter, _, err := parseMongoCallArgs(req.Args, op)
		if err != nil {
			return nil, err
		}
		n, err := coll.CountDocuments(ctx, filter)
		if err != nil {
			return nil, fmt.Errorf("mongo count: %w", err)
		}
		return &QueryResult{
			Columns:   []string{"count"},
			Rows:      [][]interface{}{{n}},
			RowCount:  1,
			ElapsedMs: time.Since(start).Milliseconds(),
		}, nil

	case "distinct":
		parts := splitTopLevelArgs(strings.Trim(strings.TrimSpace(req.Args), "()"))
		if len(parts) == 0 {
			return nil, fmt.Errorf("distinct 需要指定字段名")
		}
		field := strings.TrimSpace(parts[0])
		field = strings.Trim(field, `"'`)
		var filter bson.D
		if len(parts) > 1 {
			if err := bson.UnmarshalExtJSON([]byte(parts[1]), false, &filter); err != nil {
				return nil, fmt.Errorf("过滤条件不是合法的 JSON：%w", err)
			}
		}
		var values []interface{}
		if err := coll.Distinct(ctx, field, filter).Decode(&values); err != nil {
			return nil, fmt.Errorf("mongo distinct: %w", err)
		}
		rows := make([][]interface{}, 0, len(values))
		for i, v := range values {
			if i >= limit {
				break
			}
			rows = append(rows, []interface{}{toJSONValue(v)})
		}
		return &QueryResult{
			Columns:   []string{field},
			Rows:      rows,
			RowCount:  len(rows),
			Truncated: len(values) > limit,
			ElapsedMs: time.Since(start).Milliseconds(),
		}, nil

	case "aggregate":
		pipeline, err := parseMongoPipeline(req.Args)
		if err != nil {
			return nil, err
		}
		cur, err := coll.Aggregate(ctx, pipeline)
		if err != nil {
			return nil, fmt.Errorf("mongo aggregate: %w", err)
		}
		defer cur.Close(ctx)

		var docs []bson.D
		for cur.Next(ctx) {
			var d bson.D
			if err := cur.Decode(&d); err != nil {
				return nil, fmt.Errorf("mongo decode: %w", err)
			}
			docs = append(docs, d)
			if len(docs) > limit {
				break
			}
		}
		return mongoDocsToResult(docs, limit, start), nil
	}

	return nil, fmt.Errorf("不支持的 MongoDB 操作 %q", op)
}

// parseMongoPipeline 解析 aggregate 的管道数组。
// 额外再校验一次 $out / $merge，防止绕过 ParseMongoQuery 直接调用适配器。
func parseMongoPipeline(args string) (mongo.Pipeline, error) {
	inner := strings.TrimSpace(args)
	if strings.HasPrefix(inner, "(") {
		inner = strings.TrimPrefix(inner, "(")
	}
	if i := strings.LastIndex(inner, ")"); i >= 0 {
		inner = inner[:i]
	}
	inner = strings.TrimSpace(inner)

	lower := strings.ToLower(inner)
	for _, marker := range mongoWriteMarkers {
		if strings.Contains(lower, strings.ToLower(marker)) {
			return nil, fmt.Errorf("不允许的聚合阶段 %s：它会写入数据", marker)
		}
	}

	var stages []bson.D
	if err := bson.UnmarshalExtJSON([]byte(inner), false, &stages); err != nil {
		return nil, fmt.Errorf("聚合管道不是合法 JSON：%w", err)
	}

	pipeline := make(mongo.Pipeline, 0, len(stages))
	for _, s := range stages {
		pipeline = append(pipeline, s)
	}
	return pipeline, nil
}

// mongoDocsToResult 把文档列表转成表格形态。
//
// 文档字段不固定，取所有文档键的并集作为列，缺失的填 nil。
func mongoDocsToResult(docs []bson.D, limit int, start time.Time) *QueryResult {
	truncated := false
	if len(docs) > limit {
		docs = docs[:limit]
		truncated = true
	}

	// 保持首次出现顺序收集列名
	seen := map[string]bool{}
	var columns []string
	for _, d := range docs {
		for _, kv := range d {
			if !seen[kv.Key] {
				seen[kv.Key] = true
				columns = append(columns, kv.Key)
			}
		}
	}
	if len(columns) == 0 {
		columns = []string{"_id"}
	}

	rows := make([][]interface{}, 0, len(docs))
	for _, d := range docs {
		row := make([]interface{}, len(columns))
		vals := make(map[string]interface{}, len(d))
		for _, kv := range d {
			vals[kv.Key] = kv.Value
		}
		for i, c := range columns {
			row[i] = toJSONValue(vals[c])
		}
		rows = append(rows, row)
	}

	return &QueryResult{
		Columns:   columns,
		Rows:      rows,
		RowCount:  len(rows),
		Truncated: truncated,
		ElapsedMs: time.Since(start).Milliseconds(),
	}
}

// toJSONValue 把 BSON 值转成前端能直接显示的形式。
// 嵌套文档采用压缩 JSON，避免表格里出现 [object Object]。
func toJSONValue(v interface{}) interface{} {
	switch val := v.(type) {
	case nil:
		return nil
	case bson.D:
		b, err := json.Marshal(dToMap(val))
		if err == nil {
			return string(b)
		}
		return fmt.Sprint(val)
	case bson.A:
		b, err := json.Marshal(aToList(val))
		if err == nil {
			return string(b)
		}
		return fmt.Sprint(val)
	case bson.ObjectID:
		return val.Hex()
	case bson.DateTime:
		return val.Time().Format(time.RFC3339)
	default:
		return val
	}
}

func dToMap(d bson.D) interface{} {
	// 用 json 可序列化的形式
	m := make(map[string]interface{}, len(d))
	for _, kv := range d {
		m[kv.Key] = toPlain(kv.Value)
	}
	return m
}

func aToList(a bson.A) []interface{} {
	out := make([]interface{}, len(a))
	for i, v := range a {
		out[i] = toPlain(v)
	}
	return out
}

func toPlain(v interface{}) interface{} {
	switch val := v.(type) {
	case bson.D:
		return dToMap(val)
	case bson.A:
		return aToList(val)
	case bson.ObjectID:
		return val.Hex()
	case bson.DateTime:
		return val.Time().Format(time.RFC3339)
	default:
		return val
	}
}
