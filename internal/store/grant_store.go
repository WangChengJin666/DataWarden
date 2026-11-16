package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/opensourceprj/datawarden/internal/model"
)

type GrantStore struct {
	pool *pgxpool.Pool
}

func NewGrantStore(pool *pgxpool.Pool) *GrantStore {
	return &GrantStore{pool: pool}
}

func (s *GrantStore) Create(ctx context.Context, g *model.Grant) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO grant_record (user_id, datasource_id, scope, action, expires_at)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		g.UserID, g.DatasourceID, g.Scope, g.Action, g.ExpiresAt,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert grant: %w", err)
	}
	return id, nil
}

// ActiveGrants 返回未撤销且未过期的授权，供 Casbin 重建策略。
func (s *GrantStore) ActiveGrants(ctx context.Context) ([]model.Grant, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, user_id, datasource_id, scope, action, expires_at, revoked_at, created_at
		 FROM grant_record
		 WHERE revoked_at IS NULL AND expires_at > $1
		 ORDER BY id`, time.Now())
	if err != nil {
		return nil, fmt.Errorf("query active grants: %w", err)
	}
	defer rows.Close()

	out := []model.Grant{}
	for rows.Next() {
		var g model.Grant
		if err := rows.Scan(&g.ID, &g.UserID, &g.DatasourceID, &g.Scope,
			&g.Action, &g.ExpiresAt, &g.RevokedAt, &g.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan grant: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Revoke 撤销一条授权。幂等：已撤销的再次调用不报错。
func (s *GrantStore) Revoke(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE grant_record SET revoked_at = now()
		 WHERE id = $1 AND revoked_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("revoke grant: %w", err)
	}
	return nil
}
