package store

import (
	"context"
	"encoding/json"
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
	scopes, err := json.Marshal(orEmptyScopes(ds.ApplyScopes))
	if err != nil {
		return 0, fmt.Errorf("marshal apply scopes: %w", err)
	}

	var id int64
	err = s.pool.QueryRow(ctx,
		`INSERT INTO datasource
		   (name, type, host, port, db_name, secret_ref, env,
		    apply_enabled, max_apply_days, apply_scopes, description)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING id`,
		ds.Name, string(ds.Type), ds.Host, ds.Port, ds.Database, ds.SecretRef, ds.Env,
		ds.ApplyEnabled, orDefaultInt(ds.MaxApplyDays, 7), scopes, ds.Description,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert datasource: %w", err)
	}
	return id, nil
}

// Update 更新数据源。名称唯一冲突会返回错误。
func (s *DatasourceStore) Update(ctx context.Context, ds *model.Datasource) error {
	scopes, err := json.Marshal(orEmptyScopes(ds.ApplyScopes))
	if err != nil {
		return fmt.Errorf("marshal apply scopes: %w", err)
	}

	tag, err := s.pool.Exec(ctx,
		`UPDATE datasource SET
		   name = $1, type = $2, host = $3, port = $4, db_name = $5,
		   secret_ref = $6, env = $7, apply_enabled = $8,
		   max_apply_days = $9, apply_scopes = $10, description = $11
		 WHERE id = $12`,
		ds.Name, string(ds.Type), ds.Host, ds.Port, ds.Database, ds.SecretRef, ds.Env,
		ds.ApplyEnabled, orDefaultInt(ds.MaxApplyDays, 7), scopes, ds.Description, ds.ID)
	if err != nil {
		return fmt.Errorf("update datasource: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("datasource %d not found", ds.ID)
	}
	return nil
}

// Delete 删除数据源。若存在关联申请或授权，数据库外键会拒绝。
func (s *DatasourceStore) Delete(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM datasource WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete datasource: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("datasource %d not found", id)
	}
	return nil
}

const datasourceSelect = `
	SELECT id, name, type, host, port, db_name, secret_ref, env, created_at,
	       apply_enabled, max_apply_days, apply_scopes, description
	FROM datasource`

func scanDatasource(row interface {
	Scan(dest ...any) error
}) (*model.Datasource, error) {
	var (
		d      model.Datasource
		typ    string
		scopes []byte
	)
	err := row.Scan(&d.ID, &d.Name, &typ, &d.Host, &d.Port, &d.Database,
		&d.SecretRef, &d.Env, &d.CreatedAt,
		&d.ApplyEnabled, &d.MaxApplyDays, &scopes, &d.Description)
	if err != nil {
		return nil, err
	}
	d.Type = model.DatasourceType(typ)

	var parsed []model.ApplyScope
	if len(scopes) > 0 {
		if err := json.Unmarshal(scopes, &parsed); err != nil {
			return nil, fmt.Errorf("unmarshal apply scopes: %w", err)
		}
	}
	d.ApplyScopes = orEmptyScopes(parsed)
	return &d, nil
}

func (s *DatasourceStore) List(ctx context.Context) ([]model.Datasource, error) {
	rows, err := s.pool.Query(ctx, datasourceSelect+` ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("query datasources: %w", err)
	}
	defer rows.Close()

	out := []model.Datasource{}
	for rows.Next() {
		d, err := scanDatasource(rows)
		if err != nil {
			return nil, fmt.Errorf("scan datasource: %w", err)
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// ListApplyable 返回对开发者开放申请的数据源，供申请页使用。
func (s *DatasourceStore) ListApplyable(ctx context.Context) ([]model.Datasource, error) {
	rows, err := s.pool.Query(ctx,
		datasourceSelect+` WHERE apply_enabled = true ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("query applyable datasources: %w", err)
	}
	defer rows.Close()

	out := []model.Datasource{}
	for rows.Next() {
		d, err := scanDatasource(rows)
		if err != nil {
			return nil, fmt.Errorf("scan datasource: %w", err)
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func (s *DatasourceStore) Get(ctx context.Context, id int64) (*model.Datasource, error) {
	row := s.pool.QueryRow(ctx, datasourceSelect+` WHERE id = $1`, id)
	d, err := scanDatasource(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("datasource %d not found", id)
		}
		return nil, fmt.Errorf("query datasource: %w", err)
	}
	return d, nil
}

func orEmptyScopes(s []model.ApplyScope) []model.ApplyScope {
	if s == nil {
		return []model.ApplyScope{}
	}
	return s
}

func orDefaultInt(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}
