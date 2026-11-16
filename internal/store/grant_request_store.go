package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/opensourceprj/datawarden/internal/model"
)

type GrantRequestStore struct {
	pool *pgxpool.Pool
}

func NewGrantRequestStore(pool *pgxpool.Pool) *GrantRequestStore {
	return &GrantRequestStore{pool: pool}
}

// Create 插入申请单，状态默认 pending。
func (s *GrantRequestStore) Create(ctx context.Context, r *model.GrantRequest) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO grant_request (user_id, datasource_id, scope, days, reason, status)
		 VALUES ($1, $2, $3, $4, $5, 'pending') RETURNING id`,
		r.UserID, r.DatasourceID, r.Scope, r.Days, r.Reason,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert grant request: %w", err)
	}
	return id, nil
}

const grantRequestSelect = `
	SELECT gr.id, gr.user_id, u.username, gr.datasource_id, d.name,
	       gr.scope, gr.days, gr.reason, gr.status,
	       gr.reviewer_id, gr.review_comment, gr.reviewed_at, gr.grant_id, gr.created_at
	FROM grant_request gr
	JOIN app_user u ON u.id = gr.user_id
	JOIN datasource d ON d.id = gr.datasource_id`

func scanGrantRequest(row interface {
	Scan(dest ...any) error
}) (*model.GrantRequest, error) {
	var r model.GrantRequest
	err := row.Scan(
		&r.ID, &r.UserID, &r.Username, &r.DatasourceID, &r.DatasourceName,
		&r.Scope, &r.Days, &r.Reason, &r.Status,
		&r.ReviewerID, &r.ReviewComment, &r.ReviewedAt, &r.GrantID, &r.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *GrantRequestStore) Get(ctx context.Context, id int64) (*model.GrantRequest, error) {
	row := s.pool.QueryRow(ctx, grantRequestSelect+" WHERE gr.id = $1", id)
	r, err := scanGrantRequest(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("grant request %d not found", id)
		}
		return nil, fmt.Errorf("query grant request: %w", err)
	}
	return r, nil
}

// ListByStatus 按状态列出申请单。status 为空表示全部。
func (s *GrantRequestStore) ListByStatus(ctx context.Context, status string, limit int) ([]model.GrantRequest, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	var (
		rows pgx.Rows
		err  error
	)
	if status == "" {
		rows, err = s.pool.Query(ctx,
			grantRequestSelect+` ORDER BY gr.created_at DESC LIMIT $1`, limit)
	} else {
		rows, err = s.pool.Query(ctx,
			grantRequestSelect+` WHERE gr.status = $1 ORDER BY gr.created_at DESC LIMIT $2`,
			status, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("list grant requests: %w", err)
	}
	defer rows.Close()

	out := []model.GrantRequest{}
	for rows.Next() {
		r, err := scanGrantRequest(rows)
		if err != nil {
			return nil, fmt.Errorf("scan grant request: %w", err)
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// ListByUser 列出某个用户自己的申请单。
func (s *GrantRequestStore) ListByUser(ctx context.Context, userID int64, limit int) ([]model.GrantRequest, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		grantRequestSelect+` WHERE gr.user_id = $1 ORDER BY gr.created_at DESC LIMIT $2`,
		userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list user grant requests: %w", err)
	}
	defer rows.Close()

	out := []model.GrantRequest{}
	for rows.Next() {
		r, err := scanGrantRequest(rows)
		if err != nil {
			return nil, fmt.Errorf("scan grant request: %w", err)
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// Approve 审批通过：在同一事务内创建 grant_record 并回填申请单。
// 使用条件更新保证只有一个审批者能生效（避免并发重复审批）。
func (s *GrantRequestStore) Approve(ctx context.Context, reqID, reviewerID int64, comment string) (*model.Grant, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		userID, dsID int64
		scope        string
		days         int
		status       string
	)
	err = tx.QueryRow(ctx,
		`SELECT user_id, datasource_id, scope, days, status
		 FROM grant_request WHERE id = $1 FOR UPDATE`, reqID,
	).Scan(&userID, &dsID, &scope, &days, &status)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("grant request %d not found", reqID)
		}
		return nil, fmt.Errorf("lock grant request: %w", err)
	}
	if status != string(model.RequestPending) {
		return nil, fmt.Errorf("grant request %d is not pending (status=%s)", reqID, status)
	}

	g := &model.Grant{
		UserID:       userID,
		DatasourceID: dsID,
		Scope:        scope,
		Action:       "read",
		ExpiresAt:    time.Now().AddDate(0, 0, days),
	}
	var grantID int64
	err = tx.QueryRow(ctx,
		`INSERT INTO grant_record (user_id, datasource_id, scope, action, expires_at)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		g.UserID, g.DatasourceID, g.Scope, g.Action, g.ExpiresAt,
	).Scan(&grantID)
	if err != nil {
		return nil, fmt.Errorf("insert grant: %w", err)
	}
	g.ID = grantID

	_, err = tx.Exec(ctx,
		`UPDATE grant_request
		 SET status = 'approved', reviewer_id = $1, review_comment = $2,
		     reviewed_at = now(), grant_id = $3
		 WHERE id = $4`,
		reviewerID, comment, grantID, reqID)
	if err != nil {
		return nil, fmt.Errorf("update grant request: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return g, nil
}

// Reject 驳回申请。
func (s *GrantRequestStore) Reject(ctx context.Context, reqID, reviewerID int64, comment string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE grant_request
		 SET status = 'rejected', reviewer_id = $1, review_comment = $2, reviewed_at = now()
		 WHERE id = $3 AND status = 'pending'`,
		reviewerID, comment, reqID)
	if err != nil {
		return fmt.Errorf("reject grant request: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("grant request %d is not pending or not found", reqID)
	}
	return nil
}

// Cancel 申请人在审批前撤销。
func (s *GrantRequestStore) Cancel(ctx context.Context, reqID, userID int64) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE grant_request SET status = 'canceled'
		 WHERE id = $1 AND user_id = $2 AND status = 'pending'`,
		reqID, userID)
	if err != nil {
		return fmt.Errorf("cancel grant request: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("grant request %d is not pending or not owned by user", reqID)
	}
	return nil
}
