package adapter

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

type mysqlAdapter struct {
	db  *sql.DB
	cfg ConnConfig
}

func newMySQL(cfg ConnConfig) (*mysqlAdapter, error) {
	// tls 参数之前不存在：云 MySQL 常常要求 TLS，
	// 硬编码不带 tls 会直接连不上，报错是含糊的握手失败。
	tlsParam := ""
	switch cfg.Options["tls"] {
	case "":
		// 不传，走驱动默认（不加密）
	case "true":
		tlsParam = "&tls=true"
	case "false":
		// 显式关闭与默认一致，不追加参数
	default:
		return nil, fmt.Errorf("MySQL 的 tls 参数只支持 true / false，当前为 %q", cfg.Options["tls"])
	}

	// 密码里含 @ / : / ? 时 DSN 会解析错位。驱动不提供转义函数，
	// 这里用 params 分离的方式规避不了，只能要求密码符合规范；
	// 至少把常见问题（密码含 @）提前暴露出来，而不是连不上还不知道为什么。
	if strings.Contains(cfg.Password, "@") || strings.Contains(cfg.Password, "/") {
		return nil, fmt.Errorf("MySQL 密码不能包含 @ 或 /，请为该数据源单独建一个不含这两个字符的只读账号")
	}

	// DSN 里**不写库名**，这是有意的。
	//
	// 查询页的「库」来自 SHOW DATABASES 下拉，与数据源配置里的 db_name
	// 通常是两个不同的库：配置填的是连上后兜底用的默认库（甚至可能是空的）。
	// 之前把 cfg.Database 拼进 DSN，于是「用户在下拉里选了 shop」这件事
	// 从未传达给 MySQL——连接默认库还是配置里那个，SQL 里写裸表名
	// （SELECT * FROM orders，补全还会帮用户省掉库前缀）就会报
	// Error 1046 (3D000): No database selected。
	//
	// 改成连接不带库、执行前按请求里的库建一条专用连接（见 withDedicatedConn），
	// 语义变成「执行哪条 SQL 就切到它声明的库」。
	//
	// 副作用：连接不再绑定库，需要库名的场景必须自己带上——
	// LIST_DATABASES / ListTables 走 information_schema，天然满足。
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/?parseTime=true&timeout=5s&readTimeout=30s&charset=utf8mb4%s",
		cfg.User, cfg.Password, cfg.Host, cfg.Port, tlsParam)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)
	return &mysqlAdapter{db: db, cfg: cfg}, nil
}

func (a *mysqlAdapter) Ping(ctx context.Context) error {
	return a.db.PingContext(ctx)
}

func (a *mysqlAdapter) ListDatabases(ctx context.Context) ([]string, error) {
	rows, err := a.db.QueryContext(ctx, "SHOW DATABASES")
	if err != nil {
		return nil, fmt.Errorf("show databases: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

func (a *mysqlAdapter) ListTables(ctx context.Context, db string) ([]string, error) {
	rows, err := a.db.QueryContext(ctx,
		"SELECT table_name FROM information_schema.tables WHERE table_schema = ?", db)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// ListSchema 一次性返回指定库下所有表（含视图）的字段名，供 SQL 编辑器做
// `别名.` 字段补全（原因见 adapter.SchemaReader 的注释）。
func (a *mysqlAdapter) ListSchema(ctx context.Context, db string) ([]TableColumns, error) {
	rows, err := a.db.QueryContext(ctx,
		`SELECT table_name, column_name FROM information_schema.columns
		 WHERE table_schema = ?
		 ORDER BY table_name, ordinal_position`, db)
	if err != nil {
		return nil, fmt.Errorf("list schema: %w", err)
	}
	defer rows.Close()

	return scanSchemaRows(rows)
}

func (a *mysqlAdapter) Query(ctx context.Context, sqlText string, rowLimit int) (*QueryResult, error) {
	return a.query(ctx, sqlText, nil, rowLimit)
}

// QueryWithArgs 执行带参数的查询，仅用于元数据查询。
func (a *mysqlAdapter) QueryWithArgs(ctx context.Context, sqlText string, args []any, rowLimit int) (*QueryResult, error) {
	return a.query(ctx, sqlText, args, rowLimit)
}

// QueryInDatabase 在指定库下执行查询，库名由 SQL 的发起方（查询页下拉）决定。
//
// 为什么需要单独一个方法：mysqlAdapter 是适配器层，按约定不做权限判断，
// 但它必须知道「在哪条连接上执行」。库名是数据库自身的 schema 概念，
// 而 pgx / mongo 的库名要么在连接串里、要么随命令一起传，所以只有 MySQL
// 需要在这里做一次会话级的 USE。
//
// 实现上的两个要点：
//
//   - 用 driver.Connector 建一条**专用连接**再 USE，而不是在连接池里
//     执行 "USE x"。池化连接的会话状态没有隔离：同时有两个人分别查
//     shop 和 pay 时，池里可能是同一条连接，互相把库切走，现象是
//     偶发「表不存在」——最难查的那类 bug。
//
//   - USE 的库名不能用参数占位符（MySQL 的 USE 不接受 prepared 参数），
//     只能拼字符串。因此这里必须做标识符校验，否则一个形如
//     `shop; DROP DATABASE x` 的库名就能变成第二条语句。
func (a *mysqlAdapter) QueryInDatabase(ctx context.Context, db, sqlText string, rowLimit int) (*QueryResult, error) {
	// 空库名回落到「不带库执行」：抛 1046 让用户看到明确的
	// 「没选库」而不是被 adapter 悄悄换成别的库。
	if db == "" {
		return a.Query(ctx, sqlText, rowLimit)
	}
	stmt := EnsureRowLimit(sqlText, rowLimit+1)

	db2, err := a.withDedicatedConn(ctx, db)
	if err != nil {
		return nil, err
	}
	defer db2.Close()

	start := time.Now()
	rows, err := db2.QueryContext(ctx, stmt)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()
	return scanRows(rows, rowLimit, start)
}

// withDedicatedConn 建一条已 USE 到 db 的专用连接（池大小 1）。
//
// 调用方负责 Close：连接一旦回收进池，下一个人拿到的就是「库已经被切过」
// 的连接，这正是要避免的状态。
func (a *mysqlAdapter) withDedicatedConn(ctx context.Context, db string) (*sql.DB, error) {
	cfg, err := a.connConfig(db)
	if err != nil {
		return nil, err
	}

	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, fmt.Errorf("build mysql connector for %q: %w", db, err)
	}
	db2 := sql.OpenDB(connector)
	// 只放一条连接：这条池子只服务当前这一次查询，多放连接没有意义。
	db2.SetMaxOpenConns(1)
	db2.SetMaxIdleConns(1)
	return db2, nil
}

// connConfig 按目标库构造一条连接的驱动配置。
//
// 单独抽出来是为了让它可测：连接参数的正确性（字符集怎么带、Params
// 里放什么）只有连上服务器才会暴露，而这类错误在单测里完全看不到。
// 之前把 charset 塞进 Params 就是踩在这上面——本地不连库的测试全绿，
// 一上线就 Error 1193。
func (a *mysqlAdapter) connConfig(db string) (*mysql.Config, error) {
	if !isSafeIdentifier(db) {
		return nil, fmt.Errorf("invalid database name %q", db)
	}

	cfg := mysql.NewConfig()
	cfg.User = a.cfg.User
	cfg.Passwd = a.cfg.Password
	cfg.Net = "tcp"
	cfg.Addr = fmt.Sprintf("%s:%d", a.cfg.Host, a.cfg.Port)
	cfg.DBName = db
	cfg.ParseTime = true
	cfg.Timeout = 5 * time.Second
	cfg.ReadTimeout = 30 * time.Second
	// 字符集必须走 Charset 选项，不能塞进 Params。
	//
	// Params 会被驱动拼成 `SET <key> = <value>` 逐条下发（见
	// go-sql-driver/mysql 的 handleParams），而 charset 根本不是系统变量，
	// 于是连接建立直接失败：
	//   Error 1193 (HY000): Unknown system variable 'charset'
	// DSN 里写 charset=utf8mb4 之所以没事，是因为 DSN 解析器把它
	// 单独收进未导出的 cfg.charsets，由驱动转成 `SET NAMES utf8mb4`。
	// 用 Config 直接建 connector 时没有解析器插手，charsets 又是未导出字段，
	// 只能通过 mysql.Charset 这个 Option 设置。
	if err := mysql.Charset("utf8mb4", "")(cfg); err != nil {
		return nil, fmt.Errorf("set mysql charset: %w", err)
	}
	if a.cfg.Options["tls"] == "true" {
		// 与 newMySQL 的 DSN 保持一致：只接受 true，具体证书校验
		// 交给驱动的默认 tls 配置（与 DSN 里 tls=true 等价）。
		cfg.TLSConfig = "true"
	}
	return cfg, nil
}

// isSafeIdentifier 只允许 MySQL 库名里合法的字符。
//
// USE 不能参数化，库名只能拼进 SQL，所以这里必须自己把关。
// 白名单刻意收得很紧（字母数字 + _ $ -）：库名真的带反引号或空格时，
// 由用户改库名或直接用全限定名 `库名.表名` 查询，比放开白名单安全。
func isSafeIdentifier(s string) bool {
	if s == "" || len(s) > 64 { // MySQL 标识符上限 64 字符
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == '$':
		default:
			return false
		}
	}
	return true
}

func (a *mysqlAdapter) query(ctx context.Context, sqlText string, args []any, rowLimit int) (*QueryResult, error) {
	start := time.Now()
	stmt := sqlText
	// 带参数的语句由调用方保证已经是完整 SQL（含 LIMIT），不再包裹：
	// 包成 SELECT * FROM (...) 后驱动无法把占位符正确绑定到外层。
	if len(args) == 0 {
		stmt = EnsureRowLimit(sqlText, rowLimit+1)
	}

	rows, err := a.db.QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()
	return scanRows(rows, rowLimit, start)
}

func (a *mysqlAdapter) Close() error { return a.db.Close() }

// scanRows 把 *sql.Rows 收敛成 QueryResult。
//
// 抽出来是因为现在有两条执行路径（默认连接 / 指定库的专用连接），
// 行扫描逻辑完全一样，复制一份迟早会走岔（比如只给其中一条加脱敏相关字段）。
func scanRows(rows *sql.Rows, rowLimit int, start time.Time) (*QueryResult, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("columns: %w", err)
	}

	res := &QueryResult{Columns: cols}
	for rows.Next() {
		if len(res.Rows) >= rowLimit {
			res.Truncated = true
			break
		}
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}
		res.Rows = append(res.Rows, normalizeRow(vals))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rows: %w", err)
	}
	res.RowCount = len(res.Rows)
	res.ElapsedMs = elapsed(start)
	return res, nil
}
