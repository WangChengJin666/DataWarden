package adapter

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

type mysqlAdapter struct {
	db  *sql.DB
	cfg ConnConfig
}

func newMySQL(cfg ConnConfig) (*mysqlAdapter, error) {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?parseTime=true&timeout=5s&readTimeout=30s&charset=utf8mb4",
		cfg.User, cfg.Password, cfg.Host, cfg.Port, cfg.Database)
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
	start := time.Now()
	stmt := EnsureRowLimit(sqlText, rowLimit+1)

	rows, err := a.db.QueryContext(ctx, stmt)
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
