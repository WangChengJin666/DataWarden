package adapter

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
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

// resolveAuthSource 决定 MongoDB 连接该用哪个认证库。
//
//   - 显式填了就用填的（账号建在业务库时填该库名）
//   - 没填则回落到 admin
//
// 为什么默认 admin 而不是「默认库」：
//
// MongoDB 官方工具的默认值就是 admin。URI 不带 authSource 且路径里有库名时，
// 驱动用的仍然是 admin（路径里的库只决定「连上后默认切到哪个库」，
// 不参与认证）；mongosh 的 --authenticationDatabase 默认值同样是 admin。
//
// 之前这里按「默认库」认证，理由是「与 mongosh 行为一致」——但这个前提是错的。
// 后果很具体：账号建在 admin 的实例，运维凭记忆留空认证库，
// 平台拿业务库去认证 → 服务端回 AuthenticationFailed → 报「密码错」，
// 而 mongosh 用同一套凭据能连上，用户无从判断到底是哪边的问题。
//
// 改成默认 admin 后，两种常见部署都能直接连：
//   - admin 建的全局管理员：留空即可（与官方工具一致）
//   - 业务库建的专属账号：认证库填该库名，界面上有明确提示
//
// 这个函数单独抽出来是为了能直接测：它决定了认证成败，
// 而失败时驱动只会回一句 Authentication failed，很难从集成测试里定位。
func resolveAuthSource(cfgAuthSource, database string) string {
	if cfgAuthSource != "" {
		return cfgAuthSource
	}
	// database 参数保留但不再参与判断：认证库与「默认库」完全解耦。
	// 留着它只为让调用处读起来是「认证库由 (authSource, database) 共同决定」，
	// 避免以后有人又把它当成回落后备。
	_ = database
	return "admin"
}

// mongoAllowedOptions 是允许通过「高级参数」透传的 MongoDB 连接选项。
//
// 用白名单而不是全量透传：这些参数会改变连接语义，
// 让运维能填 replicaSet / tls 是为了连得上，
// 而不是为了让人把连接指向别处（那应该通过 host 配置来做）。
var mongoAllowedOptions = map[string]bool{
	"replicaSet":       true,
	"directConnection": true,
	"tls":              true,
	"tlsCAFile":        true,
	"tlsInsecure":      true,
	"readPreference":   true,
	"authMechanism":    true,
	"appName":          true,
}

// newMongo 构造 MongoDB 适配器。
//
// host 支持逗号分隔的多个节点（副本集的常见形态）：驱动要求把
// 全部成员写进 seed list，只填一个节点在多数情况下也能连上（发现机制会补齐），
// 但当节点恰好不可达时会直接失败，所以允许手写多个。
func newMongo(cfg ConnConfig) (Adapter, error) {
	if cfg.Database == "" {
		return nil, fmt.Errorf("MongoDB 必须填写默认库，否则无法确定要查哪个库")
	}
	if cfg.Port == 0 {
		cfg.Port = 27017
	}

	// 认证库（authSource）决定「账号建在哪个库里」，与要查的库是两回事：
	//   - admin 建的管理员账号：authSource=admin（留空即此值）
	//   - 建在某业务库的专属账号：authSource=该库
	// 认错了库，服务端会回 AuthenticationFailed，
	// 看起来像「密码错」，实际是认证库选错。
	authSource := resolveAuthSource(cfg.AuthSource, cfg.Database)

	hosts, err := parseMongoHosts(cfg.Host, cfg.Port)
	if err != nil {
		return nil, err
	}

	opts := options.Client().
		SetHosts(hosts).
		SetServerSelectionTimeout(8 * time.Second).
		SetConnectTimeout(8 * time.Second)

	// 高级参数：先聚合再应用（见 applyMongoOptions 的说明）。
	// 未知键直接报错而不是静默忽略：用户填了 replicaSet 却没生效，
	// 比明确报「不支持该参数」更难排查。
	if err := applyMongoOptions(opts, cfg.Options); err != nil {
		return nil, err
	}

	if cfg.User != "" {
		cred := options.Credential{
			Username:   cfg.User,
			Password:   cfg.Password,
			AuthSource: authSource,
		}
		// authMechanism 要落在 Credential 上，不是客户端选项
		if m := cfg.Options["authMechanism"]; m != "" {
			cred.AuthMechanism = m
		}
		opts.SetAuth(cred)
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
		// 认证失败时把认证库带上，否则用户只会看到「认证失败」，
		// 根本不知道平台是按哪个库去认证的，也就无从判断该填 admin 还是业务库。
		if isMongoAuthError(err) {
			return nil, fmt.Errorf("mongo ping (authSource=%s): %w", authSource, err)
		}
		return nil, fmt.Errorf("mongo ping: %w", err)
	}

	return &mongoAdapter{client: client, dbName: cfg.Database}, nil
}

// parseMongoHosts 把 host 字段拆成驱动的 seed list。
//
// 三种写法都接受：
//
//	10.0.0.1               单机，用配置里的 port
//	10.0.0.1,10.0.0.2      副本集多成员，各自用配置里的 port
//	10.0.0.1:27018         显式端口
//	[::1] / [::1]:27017    IPv6（必须加方括号）
//
// IPv6 必须加方括号：裸写 ::1:27017 无法区分最后一段是端口还是地址的第 8 段，
// 驱动会当成非法地址。之前用 fmt.Sprintf("%s:%d") 拼单地址，IPv6 直接拼不出来。
func parseMongoHosts(host string, port int) ([]string, error) {
	parts := strings.Split(host, ",")
	hosts := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}

		if strings.HasPrefix(p, "[") {
			// 方括号写法：找到 ]，后面要么没东西（补端口），要么是 :port
			end := strings.Index(p, "]")
			if end < 0 {
				return nil, fmt.Errorf("IPv6 地址 %q 缺少右方括号", p)
			}
			rest := p[end+1:]
			switch {
			case rest == "":
				hosts = append(hosts, fmt.Sprintf("%s:%d", p, port))
			case strings.HasPrefix(rest, ":"):
				hosts = append(hosts, p)
			default:
				return nil, fmt.Errorf("IPv6 地址 %q 的端口写法不正确，应为 [地址]:端口", p)
			}
			continue
		}

		// 非方括号写法里冒号多于一个，说明是没加括号的 IPv6
		if strings.Count(p, ":") > 1 {
			return nil, fmt.Errorf(
				"IPv6 地址 %q 需要加方括号，例如 [::1]:%d", p, port)
		}
		if strings.Contains(p, ":") {
			hosts = append(hosts, p) // 已带端口
			continue
		}
		hosts = append(hosts, fmt.Sprintf("%s:%d", p, port))
	}
	if len(hosts) == 0 {
		return nil, fmt.Errorf("MongoDB 主机不能为空")
	}
	return hosts, nil
}

// applyMongoOptions 应用全部高级参数。
//
// TLS 相关的三个参数必须**先聚合再一次性应用**，不能各自 SetTLSConfig：
// options.ClientOptions.SetTLSConfig 是整体替换，而 map 遍历顺序是随机的。
// 若 tls=true 和 tlsCAFile 同时填了，后者先执行、前者后执行时，
// CA 池会被一个空的 tls.Config 覆盖掉——表现为「限时偶发
// x509: certificate signed by unknown authority」，重启一次可能又好了，
// 是最难排查的一类问题。
//
// 所以这里分两遍：第一遍处理非 TLS 参数并收集 TLS 意图，
// 第二遍合成一个完整 tls.Config 再设置。
func applyMongoOptions(opts *options.ClientOptions, kv map[string]string) error {
	var (
		wantTLS  bool
		tlsSet   bool // 是否显式写了 tls=true/false
		insecure bool
		caFile   string
	)

	for k, v := range kv {
		if v == "" {
			continue
		}
		if !mongoAllowedOptions[k] {
			return fmt.Errorf("不支持的 MongoDB 连接参数 %q", k)
		}
		switch k {
		case "replicaSet":
			opts.SetReplicaSet(v)
		case "directConnection":
			b, err := strconv.ParseBool(v)
			if err != nil {
				return fmt.Errorf("directConnection 需要 true 或 false，当前为 %q", v)
			}
			opts.SetDirect(b)
		case "readPreference":
			rp, err := parseReadPreference(v)
			if err != nil {
				return err
			}
			opts.SetReadPreference(rp)
		case "appName":
			opts.SetAppName(v)
		case "authMechanism":
			// 由 newMongo 直接从 cfg.Options 读进 Credential，这里不处理。
			// 不能在这里"顺便"设置：Credential 在 SetAuth 时整体生效，
			// 而本函数拿不到要修改的 Credential。
		case "tls":
			b, err := strconv.ParseBool(v)
			if err != nil {
				return fmt.Errorf("tls 需要 true 或 false，当前为 %q", v)
			}
			wantTLS, tlsSet = b, true
		case "tlsInsecure":
			b, err := strconv.ParseBool(v)
			if err != nil {
				return fmt.Errorf("tlsInsecure 需要 true 或 false，当前为 %q", v)
			}
			insecure = b
		case "tlsCAFile":
			caFile = v
		}
	}

	// 第二遍：合成 TLS 配置
	if tlsSet && !wantTLS {
		// 显式关闭 TLS。此时若还填了 tlsCAFile / tlsInsecure，
		// 属于配置自相矛盾：静默忽略会让用户以为 CA 生效了。
		if caFile != "" || insecure {
			return fmt.Errorf("tls=false 与 tlsCAFile/tlsInsecure 冲突：" +
				"要启用 TLS 请把 tls 设为 true，或清空其余 TLS 参数")
		}
		opts.SetTLSConfig(nil)
		return nil
	}
	if !tlsSet && caFile == "" && !insecure {
		// 没提到 TLS，保持驱动默认
		return nil
	}

	cfg := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: insecure,
	}
	if caFile != "" {
		ca, err := readCACert(caFile)
		if err != nil {
			return fmt.Errorf("读取 CA 证书 %q 失败: %w", caFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(ca) {
			return fmt.Errorf("CA 证书 %q 不是合法的 PEM", caFile)
		}
		cfg.RootCAs = pool
	}
	opts.SetTLSConfig(cfg)
	return nil
}

// applyMongoOption 是单参数入口，保留给测试与一次性调用场景。
// 注意：多次单独调用会互相覆盖 TLS 配置；需要组合 TLS 参数时用 applyMongoOptions。
func applyMongoOption(opts *options.ClientOptions, key, val string) error {
	return applyMongoOptions(opts, map[string]string{key: val})
}

// maxCACertSize 限制 CA 证书文件大小。
//
// 不限制的风险：tlsCAFile 的路径来自数据源配置，而 DBA 可能手滑填成
// /var/log/syslog 或 /dev/zero——前者会把几百 MB 读进内存，
// 后者会一直读下去直到 OOM。证书文件正常都在几十 KB 以内。
const maxCACertSize = 1 << 20 // 1 MiB

// readCACert 有上限地读取 CA 证书文件。
func readCACert(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// 用 LimitReader 而不是先 Stat：Stat 与 Read 之间有 TOCTOU 窗口，
	// 而且对 /proc、管道这类非常规文件 Stat 的 size 不可信。
	limited := io.LimitReader(f, maxCACertSize+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(data) > maxCACertSize {
		return nil, fmt.Errorf("文件超过 %d 字节，不像证书", maxCACertSize)
	}
	return data, nil
}

// parseReadPreference 把字符串映射成驱动的读偏好对象。
func parseReadPreference(val string) (*readpref.ReadPref, error) {
	var mode readpref.Mode
	switch strings.ToLower(val) {
	case "primary":
		mode = readpref.PrimaryMode
	case "primarypreferred":
		mode = readpref.PrimaryPreferredMode
	case "secondary":
		mode = readpref.SecondaryMode
	case "secondarypreferred":
		mode = readpref.SecondaryPreferredMode
	case "nearest":
		mode = readpref.NearestMode
	default:
		return nil, fmt.Errorf(
			"readPreference 只支持 primary / primaryPreferred / secondary / secondaryPreferred / nearest，当前为 %q", val)
	}
	rp, err := readpref.New(mode)
	if err != nil {
		return nil, fmt.Errorf("readPreference %q: %w", val, err)
	}
	return rp, nil
}

// isMongoAuthError 判断是否为认证类错误。
//
// 用错误文本匹配而不是类型断言：驱动的 ServerError 有多个包装层级
// （握手错误 → sasl 错误 → ServerError），逐层断言容易随驱动版本失效。
// 这里只用于决定「要不要在错误里补充上下文」，误判也只是多带一个前缀，不影响正确性。
func isMongoAuthError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "authentication failed") ||
		strings.Contains(s, "auth error") ||
		strings.Contains(s, "unauthorized")
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

// QueryWithArgs 同样不支持：MongoDB 没有 SQL，元数据走 QueryMongo 采样。
func (a *mongoAdapter) QueryWithArgs(ctx context.Context, sql string, args []any, rowLimit int) (*QueryResult, error) {
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
	// 支持 shop.users 这种带库前缀的写法。
	// 用 SplitDBCollection 而不是就地拆：鉴权层必须按完全相同的规则拆，
	// 否则「鉴权认的库」和「实际查的库」会不一致——那正是越权的入口。
	dbName, collName, err := SplitDBCollection(req.Collection, req.Database)
	if err != nil {
		return nil, err
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
