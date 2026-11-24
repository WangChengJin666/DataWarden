package adapter

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type pgAdapter struct {
	db  *sql.DB
	cfg ConnConfig
}

func newPostgres(cfg ConnConfig) (*pgAdapter, error) {
	// sslmode 之前硬编码成 disable：云厂商的 PG（RDS / ApsaraDB / TencentDB）
	// 基本强制 SSL，这条写死的 disable 会让它们一律连不上，
	// 报错还很难懂（server does not support SSL ... 的反向版本）。
	// 现在默认仍是 disable（内网直连最常见），但允许通过高级参数覆盖。
	sslMode := cfg.Options["sslmode"]
	if sslMode == "" {
		sslMode = "disable"
	}
	switch sslMode {
	case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
		// 合法的 libpq sslmode
	default:
		return nil, fmt.Errorf(
			"sslmode 只支持 disable / allow / prefer / require / verify-ca / verify-full，当前为 %q", sslMode)
	}

	// dbname 与「查询时用的库」是两回事，不能共用一个字段。
	//
	// PostgreSQL 的一条连接绑定一个 database（DSN 的 dbname），
	// 而用户在查询页选的「库」其实是 schema（pg_namespace），
	// 用于拼 schema.table 和 information_schema.table_schema。
	//
	// 数据源表单只有一个「默认库」输入框，语义是**连接到的 database**；
	// 查询页的 db 参数是 schema，由元数据接口（ListDatabases 返回 nspname）
	// 提供下拉，不经过这里。
	//
	// 若将来需要「连接 datawarden 库、默认查 biz schema」这类组合，
	// 用高级参数 connect_db 覆盖 dbname 即可——但默认两者一致，
	// 保持与表单单一输入框的直觉相符。
	connectDB := cfg.Options["connect_db"]
	if connectDB == "" {
		connectDB = cfg.Database
	}

	// 用 key=value 形式而不是 URL：密码里出现 @ / : / ? 时 URL 需要转义，
	// 拼字符串很容易拼出连不上的 DSN，而 key=value 只需转义引号和反斜杠。
	dsn := fmt.Sprintf(
		"host=%s port=%d dbname=%s user=%s password=%s sslmode=%s",
		quoteDSNValue(cfg.Host), cfg.Port, quoteDSNValue(connectDB),
		quoteDSNValue(cfg.User), quoteDSNValue(cfg.Password), sslMode,
	)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)
	return &pgAdapter{db: db, cfg: cfg}, nil
}

// quoteDSNValue 给 key=value 形式的 DSN 值加引号并转义。
//
// libpq 的规则是：值里含空格或引号时用单引号包裹，内部单引号和反斜杠
// 用反斜杠转义。不处理的话，密码里有空格就解析失败。
func quoteDSNValue(v string) string {
	if v == "" {
		return "''"
	}
	if !strings.ContainsAny(v, " '\\") {
		return v
	}
	r := strings.NewReplacer(`\`, `\\`, `'`, `\'`)
	return "'" + r.Replace(v) + "'"
}

func (a *pgAdapter) Ping(ctx context.Context) error {
	return a.db.PingContext(ctx)
}

// ListDatabases 列出当前连上的 database 下可访问的 schema。
//
// 为什么返回 schema 而不是 database 名：
// PostgreSQL 的一条连接只绑定一个 database（DSN 里固定），
// 用户真正需要选的是「表在哪，schema.table 的那个 schema」。
// 之前这里返回 pg_database 里的 database 名，而 ListTables / 列名查询
// 都按 schema 过滤——两者概念不一致，导致：
//   - 下拉里显示的是 database 名（通常是 datawarden / postgres 之类）
//   - 拿它去查 information_schema.tables WHERE table_schema = <database 名>
//     永远匹配不到，于是「表」下拉恒为空
//
// 排除 pg_catalog / information_schema：它们是系统 schema，
// 在补全里列出来只会干扰用户（且基本都是无关内容）。
func (a *pgAdapter) ListDatabases(ctx context.Context) ([]string, error) {
	rows, err := a.db.QueryContext(ctx,
		`SELECT nspname FROM pg_namespace
		 WHERE nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
		   AND nspname NOT LIKE 'pg_temp%'
		   AND nspname NOT LIKE 'pg_toast_temp%'
		 ORDER BY nspname`)
	if err != nil {
		return nil, fmt.Errorf("list databases: %w", err)
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

// ListTables 列出指定 schema 下的表与视图。
//
// db 参数语义是 schema（与 ListDatabases 的返回值一致）。
// 显式限定 table_type：只排除系统表，保留 VIEW / MATERIALIZED VIEW，
// 因为只读查询里查视图是常态，补全列表里没有它反而奇怪。
func (a *pgAdapter) ListTables(ctx context.Context, db string) ([]string, error) {
	rows, err := a.db.QueryContext(ctx,
		`SELECT table_name FROM information_schema.tables
		 WHERE table_schema = $1
		   AND table_type IN ('BASE TABLE', 'VIEW', 'MATERIALIZED VIEW')
		 ORDER BY table_name`, db)
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

// ListSchema 一次性返回指定 schema 下所有表（含视图）的字段名。
//
// 查询页的 SQL 编辑器要靠它做 `别名.` 的字段补全：别名指向哪张表只有
// 解析完语句才知道，逐表按需请求跟不上输入。这里一条 information_schema
// 查询就能覆盖整个 schema，代价远低于循环调用 Columns。
func (a *pgAdapter) ListSchema(ctx context.Context, db string) ([]TableColumns, error) {
	rows, err := a.db.QueryContext(ctx,
		`SELECT table_name, column_name FROM information_schema.columns
		 WHERE table_schema = $1
		 ORDER BY table_name, ordinal_position`, db)
	if err != nil {
		return nil, fmt.Errorf("list schema: %w", err)
	}
	defer rows.Close()

	return scanSchemaRows(rows)
}

func (a *pgAdapter) Query(ctx context.Context, sqlText string, rowLimit int) (*QueryResult, error) {
	return a.query(ctx, sqlText, nil, rowLimit)
}

// QueryWithArgs 执行带参数的查询，仅用于元数据查询。
func (a *pgAdapter) QueryWithArgs(ctx context.Context, sqlText string, args []any, rowLimit int) (*QueryResult, error) {
	return a.query(ctx, sqlText, args, rowLimit)
}

func (a *pgAdapter) query(ctx context.Context, sqlText string, args []any, rowLimit int) (*QueryResult, error) {
	start := time.Now()
	stmt := sqlText
	// 带参数的语句不追加外层包裹：占位符绑定发生在最外层语句上，
	// 包一层子查询后 pgx 的 $1 解析会错位。
	if len(args) == 0 {
		stmt = EnsureRowLimit(sqlText, rowLimit+1)
	}

	rows, err := a.db.QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

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

func (a *pgAdapter) Close() error { return a.db.Close() }
