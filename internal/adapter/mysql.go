package adapter

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
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

	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?parseTime=true&timeout=5s&readTimeout=30s&charset=utf8mb4%s",
		cfg.User, cfg.Password, cfg.Host, cfg.Port, cfg.Database, tlsParam)
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

func (a *mysqlAdapter) Query(ctx context.Context, sqlText string, rowLimit int) (*QueryResult, error) {
	return a.query(ctx, sqlText, nil, rowLimit)
}

// QueryWithArgs 执行带参数的查询，仅用于元数据查询。
func (a *mysqlAdapter) QueryWithArgs(ctx context.Context, sqlText string, args []any, rowLimit int) (*QueryResult, error) {
	return a.query(ctx, sqlText, args, rowLimit)
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

func (a *mysqlAdapter) Close() error { return a.db.Close() }
