package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/opensourceprj/datawarden/internal/model"
)

type DatasourceStore struct {
	pool *pgxpool.Pool
}

func NewDatasourceStore(pool *pgxpool.Pool) *DatasourceStore {
	return &DatasourceStore{pool: pool}
}

func (s *DatasourceStore) Create(ctx context.Context, ds *model.Datasource) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO datasource (name, type, host, port, db_name, secret_ref, env)
		 VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		ds.Name, string(ds.Type), ds.Host, ds.Port, ds.Database, ds.SecretRef, ds.Env,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert datasource: %w", err)
	}
	return id, nil
}

func (s *DatasourceStore) List(ctx context.Context) ([]model.Datasource, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, type, host, port, db_name, secret_ref, env, created_at
		 FROM datasource ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("query datasources: %w", err)
	}
	defer rows.Close()

	out := []model.Datasource{}
	for rows.Next() {
		var d model.Datasource
		var typ string
		if err := rows.Scan(&d.ID, &d.Name, &typ, &d.Host, &d.Port,
			&d.Database, &d.SecretRef, &d.Env, &d.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan datasource: %w", err)
		}
		d.Type = model.DatasourceType(typ)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *DatasourceStore) Get(ctx context.Context, id int64) (*model.Datasource, error) {
	var d model.Datasource
	var typ string
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, type, host, port, db_name, secret_ref, env, created_at
		 FROM datasource WHERE id = $1`, id,
	).Scan(&d.ID, &d.Name, &typ, &d.Host, &d.Port, &d.Database, &d.SecretRef, &d.Env, &d.CreatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("datasource %d not found", id)
		}
		return nil, fmt.Errorf("query datasource: %w", err)
	}
	d.Type = model.DatasourceType(typ)
	return &d, nil
}
