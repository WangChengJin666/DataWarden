package audit

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/opensourceprj/datawarden/internal/querysvc"
)

// Logger 把审计事件写入 audit_log 表。
// 表上有 BEFORE UPDATE OR DELETE 触发器，写入后不可篡改。
type Logger struct {
	pool *pgxpool.Pool
}

func NewLogger(pool *pgxpool.Pool) *Logger {
	return &Logger{pool: pool}
}

// Record 实现 querysvc.Auditor 接口。
func (l *Logger) Record(ctx context.Context, e querysvc.AuditEvent) error {
	detail, err := json.Marshal(e.Detail)
	if err != nil {
		return fmt.Errorf("marshal audit detail: %w", err)
	}
	if len(detail) == 0 {
		detail = []byte("{}")
	}

	_, err = l.pool.Exec(ctx,
		`INSERT INTO audit_log (actor_id, actor_name, action, object, detail, ip, user_agent, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, now())`,
		e.ActorID, e.ActorName, e.Action, e.Object, detail, e.IP, e.UserAgent,
	)
	if err != nil {
		return fmt.Errorf("insert audit log: %w", err)
	}
	return nil
}
