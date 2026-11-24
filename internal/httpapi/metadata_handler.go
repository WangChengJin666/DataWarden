package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/opensourceprj/datawarden/internal/adapter"
	"github.com/opensourceprj/datawarden/internal/model"
)

// MetadataHandler 暴露库 / 表 / 字段元数据，供查询页做下拉与编辑器补全。
//
// 为什么之前没有：adapter.Adapter 早就定义了 ListDatabases / ListTables，
// 但没有任何路由把它们接出来，于是前端只能让用户手打库名和表名。
// 这不只是「不好用」——SQL 查询的鉴权是按前端提交的 tables 逐表判定的，
// 手打字拼错一个字母的后果是 403「no permission on one or more tables」，
// 用户根本分不清是「打错了」还是「真的没权限」。
type MetadataHandler struct {
	Store DatasourceStore
	// Factory 按数据源 ID 构造 Adapter。
	Factory AdapterFactory
	// Enforcer 用于过滤：只返回当前用户有读权限的库表。
	// 为 nil 时不作过滤（仅用于测试）。
	Enforcer querysvcEnforcer
}

// querysvcEnforcer 是权限判定的最小接口，避免此处依赖整个 querysvc 包的类型。
type querysvcEnforcer interface {
	Enforce(subject, object, action string) bool
}

// catalogTimeout 限制元数据查询耗时：目标库不可达时不能把前端挂住。
const catalogTimeout = 8 * time.Second

// ListDatabases 列出数据源上可见的库。
//
// 返回的是「用户有权访问的库」：对每个库判断 ds:<id>:<db>:* 是否放行。
// 不过滤的话，一个只有 shop 库权限的用户也能看到全部库名——
// 库名本身就是内网信息（payments / user_center 这类名字足够说明业务）。
//
// dba / superadmin 命中内置策略 ds:*:*:*，因此这里对他们是全量放行。
// 这不是特例分支：放行范围完全由 Casbin 策略表决定，
// 加角色分支反而会出现「策略说能、代码说不」的第二套事实。
func (h *MetadataHandler) ListDatabases(c *gin.Context) {
	if h.Factory == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "metadata not configured"})
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	uid, username, _ := currentUser(c)

	ds, err := h.Store.Get(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "datasource not found"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), catalogTimeout)
	defer cancel()

	// 库列表失败一律报 502，不做「权限类降级成空列表」。
	//
	// 为什么不能降级：「空且 200」必须能确定表达「确实一个库都没有」。
	// 一旦把权限失败也变成空列表，这两种情况就再也分不开——
	// 用户看到空下拉时无从判断是没授权、账号权限被回收，
	// 还是配置写错了。让它在出错时报错，是唯一能保住这个语义的做法。
	names, err := h.databasesFor(ctx, ds, uid)
	if err != nil {
		auditEvent(c, uid, username, "datasource.databases", id, map[string]any{
			"error": err.Error(),
		})
		c.JSON(http.StatusBadGateway, gin.H{
			"error": "无法列出库：" + err.Error(),
		})
		return
	}

	auditEvent(c, uid, username, "datasource.databases", id, map[string]any{
		"count": len(names),
	})
	c.JSON(http.StatusOK, gin.H{"items": names})
}

// ListTables 列出指定库下的表 / 集合。
//
// 同样按权限过滤。Redis 没有表的概念，直接返回空数组而不是报错：
// 前端的调用是统一的，不该让调用方为「这个类型没有表」写分支。
func (h *MetadataHandler) ListTables(c *gin.Context) {
	if h.Factory == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "metadata not configured"})
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	db := strings.TrimSpace(c.Query("db"))
	if db == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "db query parameter is required"})
		return
	}

	ds, err := h.Store.Get(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "datasource not found"})
		return
	}
	if ds.Type == model.TypeRedis {
		c.JSON(http.StatusOK, gin.H{"items": []string{}})
		return
	}

	uid, _, _ := currentUser(c)
	ctx, cancel := context.WithTimeout(c.Request.Context(), catalogTimeout)
	defer cancel()

	// 表列表的失败不做「权限类忽略」处理：
	// 能走到这里说明 db 通配权限已经通过，ListTables 仍失败只可能是
	// 故障（连接断了、账号被回收、库被删）。吞成空数组会让用户
	// 以为「这个库没表」，而实际是连不上。
	tables, err := h.tablesFor(ctx, ds, uid, db)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "无法列出表：" + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": tables})
}

// Columns 返回表 / 集合的字段名，供补全使用。
//
// 用各库自己的 information_schema / 元数据命令，而不是解析 SELECT 的结果：
// 后者要求先跑一次查询，代价高且对空表无能为力。
func (h *MetadataHandler) Columns(c *gin.Context) {
	if h.Factory == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "metadata not configured"})
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	db := strings.TrimSpace(c.Query("db"))
	table := strings.TrimSpace(c.Query("table"))
	if db == "" || table == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "db and table query parameters are required"})
		return
	}

	ds, err := h.Store.Get(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "datasource not found"})
		return
	}

	uid, _, _ := currentUser(c)
	// 字段级也要鉴权：没读过这张表的人不需要知道它的列名
	if !h.allowed(uid, ds.ID, db, table) {
		c.JSON(http.StatusForbidden, gin.H{"error": "no permission on this table"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), catalogTimeout)
	defer cancel()

	cols, err := h.columnsFor(ctx, ds, db, table)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": cols})
}

// Schema 一次性返回整库的表与字段，供 SQL 编辑器做 `别名.` 字段补全。
//
// 为什么需要批量接口：编辑器要提示 `t.` 之后的字段时，必须先知道 `t`
// 是 FROM 子句里的哪张表。别名是随 SQL 改的，无法预先告知后端，
// 所以把整个库的字段一次取回本地，由前端解析别名后查表。
// 逐表调 /columns 也行，但一次输入会打出十几个请求，且每敲一个字母都要等。
//
// 与 /columns 一样按表过滤：只返回用户读过权限的表的字段。
// 不这么做的话，这个接口就成了「一次拿到整库表结构」的旁路——
// /tables 会按授权裁剪，这里却把裁剪掉的那些表的列名又吐回来。
func (h *MetadataHandler) Schema(c *gin.Context) {
	if h.Factory == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "metadata not configured"})
		return
	}
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	db := strings.TrimSpace(c.Query("db"))
	if db == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "db query parameter is required"})
		return
	}

	ds, err := h.Store.Get(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "datasource not found"})
		return
	}

	uid, _, _ := currentUser(c)
	// 整库无权限直接 403，不去连库：连库本身也有成本，
	// 而且能列出表名这件事本身就是信息泄露。
	if !h.allowed(uid, ds.ID, db, "*") {
		c.JSON(http.StatusForbidden, gin.H{"error": "no permission on this database"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), catalogTimeout)
	defer cancel()

	ad, err := h.Factory(ds.ID)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "无法读取表结构：" + err.Error()})
		return
	}
	defer ad.Close()

	// 不支持批量能力的适配器（Mongo / Redis）不报错，返回空清单：
	// 调用方（编辑器补全）本就不依赖它，报错只会让前端弹无意义的提示。
	sr, ok := ad.(adapter.SchemaReader)
	if !ok {
		c.JSON(http.StatusOK, gin.H{"items": []adapter.TableColumns{}})
		return
	}

	tables, err := sr.ListSchema(ctx, db)
	if err != nil {
		// 与库 / 表列表同理：这是故障（连不上、账号被回收），不是「没有表」，
		// 吞成空清单会让补全静默失效且无从排查。
		c.JSON(http.StatusBadGateway, gin.H{"error": "无法读取表结构：" + err.Error()})
		return
	}

	// 库级通配权限已通过，理论上表级也通过；这里仍逐表再判一次，
	// 是因为授权粒度将来若细化到表，裁剪逻辑必须已经就位。
	out := make([]adapter.TableColumns, 0, len(tables))
	for _, t := range tables {
		if h.allowed(uid, ds.ID, db, t.Table) {
			out = append(out, t)
		}
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// --- 内部实现 ---

// databasesFor 返回用户可见的库名，按名称排序。
//
// 返回 error 而不是一律吞成空数组：前端需要区分两种情况。
//   - 成功但为空：确实一个库都看不到（无授权 / 账号无权限），
//     此时前端退回手填是合理降级
//   - 失败：连不上、配置错、驱动报错——这必须让用户看到，
//     否则现象是「下拉永远是空的」，而原因是密码错了还是网络不通
//     完全无法判断。之前这里把所有错误都变成空数组，
//     等于把最需要排查信息的一类故障伪装成「没有数据」。
func (h *MetadataHandler) databasesFor(ctx context.Context, ds *model.Datasource, uid int64) ([]string, error) {
	ad, err := h.Factory(ds.ID)
	if err != nil {
		return nil, err
	}
	defer ad.Close()

	names, err := ad.ListDatabases(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]string, 0, len(names))
	for _, n := range names {
		if h.allowed(uid, ds.ID, n, "*") {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}

// tablesFor 返回用户在指定库下可见的表名。
func (h *MetadataHandler) tablesFor(ctx context.Context, ds *model.Datasource, uid int64, db string) ([]string, error) {
	// 整库通配授权（db:*）：可以直接列全部表
	if !h.allowed(uid, ds.ID, db, "*") {
		// 只授权了部分表：不能拿 ListTables（那会列出整个库的表名，
		// 等于泄露用户无权看到的表存在性），改为从授权 scope 里取明确授予的表。
		//
		// 断言失败必须报错而不是返回空列表：Store 没实现 scopeLister
		// 说明装配漏了，是部署缺陷；静默返回空会让「部分表授权」的用户
		// 看到空下拉，而排查方向会被引向权限配置本身。
		lister, ok := h.Store.(scopeLister)
		if !ok {
			return nil, fmt.Errorf("metadata store 未实现 GrantScopes，无法按部分表授权列出表名")
		}
		scopes, err := lister.GrantScopes(ctx, uid, ds.ID)
		if err != nil {
			return nil, err
		}
		out := []string{}
		for _, sc := range scopes {
			sdb, tables := splitScope(sc)
			if sdb != db {
				continue
			}
			out = append(out, tables...)
		}
		sort.Strings(out)
		return out, nil
	}

	ad, err := h.Factory(ds.ID)
	if err != nil {
		return nil, err
	}
	defer ad.Close()

	names, err := ad.ListTables(ctx, db)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

// columnsFor 按类型取字段名。
func (h *MetadataHandler) columnsFor(ctx context.Context, ds *model.Datasource, db, table string) ([]string, error) {
	ad, err := h.Factory(ds.ID)
	if err != nil {
		return nil, err
	}
	defer ad.Close()

	switch ds.Type {
	case model.TypePostgreSQL:
		return queryColumnNames(ctx, ad, connQuery{
			// PG 的「库」字段实际是 schema（连到哪个 database 由 DSN 决定，
			// 查询里写 schema.table）。所以必须按 table_schema 过滤：
			//
			// 之前只按 table_name 过滤，两个 schema 下都有 orders 表时会
			// 把两套列名合并返回——补全列表里出现重复项，更严重的是
			// 泄露了用户无权访问的另一个 schema 的同名表的列名。
			SQL: `SELECT column_name FROM information_schema.columns
			      WHERE table_schema = $1 AND table_name = $2
			      ORDER BY ordinal_position`,
			Args: []any{db, table},
		})
	case model.TypeMySQL:
		return queryColumnNames(ctx, ad, connQuery{
			SQL: `SELECT column_name FROM information_schema.columns
			      WHERE table_schema = ? AND table_name = ? ORDER BY ordinal_position`,
			Args: []any{db, table},
		})
	case model.TypeMongoDB:
		return mongoFieldNames(ctx, h.Factory, ds.ID, db, table)
	default:
		return []string{}, nil
	}
}

// connQuery 描述一次取列名的查询。
type connQuery struct {
	SQL  string
	Args []any
}

// queryColumnNames 执行取列名查询并整理成字符串切片。
func queryColumnNames(ctx context.Context, ad adapter.Adapter, q connQuery) ([]string, error) {
	res, err := ad.QueryWithArgs(ctx, q.SQL, q.Args, 2000)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(res.Rows))
	for _, row := range res.Rows {
		if len(row) == 0 {
			continue
		}
		if s, ok := row[0].(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

// mongoFieldNames 采样若干文档，把键名当作字段名。
//
// MongoDB 是无 schema 的：没有「列」的定义，只能采样。
//
// 用 $sample 而不是 find({}).limit(N)：
//   - find 从集合头部顺序取文档，若前几条恰好字段稀疏（比如刚插入的
//     骨架文档），补全会缺一大堆字段
//   - $sample 走随机采样，用 20 条能把「实际存在的字段」覆盖得比较全
//
// 采样条数控制在 20：这是补全，不是数据探查，没必要取更多；
// 而 $sample 对超大集合也是走随机游标，不会全表扫描。
func mongoFieldNames(ctx context.Context, factory AdapterFactory, dsID int64, db, coll string) ([]string, error) {
	ad, err := factory(dsID)
	if err != nil {
		return nil, err
	}
	defer ad.Close()

	const sampleSize = 20
	res, err := adapter.QueryMongo(ctx, ad, adapter.MongoQueryRequest{
		Database:   db,
		Collection: coll,
		Op:         "aggregate",
		Args:       fmt.Sprintf(`([{"$sample":{"size":%d}}])`, sampleSize),
		Limit:      sampleSize,
	})
	if err != nil {
		return nil, err
	}
	// 结果的 Columns 就是这些文档键的并集
	out := res.Columns
	// 空集合的 $sample 返回空文档集，Columns 会退化成 {"_id"}。
	// 那不算「有字段」，但也比报错好——前端会显示「无字段」。
	return out, nil
}

// allowed 判断用户对 db/table 是否有读权限。
func (h *MetadataHandler) allowed(uid int64, dsID int64, db, table string) bool {
	if h.Enforcer == nil {
		return true
	}
	obj := "ds:" + strconv.FormatInt(dsID, 10) + ":" + db + ":" + table
	return h.Enforcer.Enforce(SubjectOf(uid), obj, "read")
}

// scopeLister 是可选的存储能力：按用户列出授权 scope。
// 定义成接口而不是直接加进 DatasourceStore，是为了不强迫所有实现都支持它。
type scopeLister interface {
	GrantScopes(ctx context.Context, userID, datasourceID int64) ([]string, error)
}
