package audit

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/opensourceprj/datawarden/internal/db"
	"github.com/opensourceprj/datawarden/internal/querysvc"
	"github.com/opensourceprj/datawarden/internal/testutil"
)

func setupPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	testutil.SkipIfShort(t)
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestLogger_RecordsEvent(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()

	logger := NewLogger(pool)
	actorID := time.Now().UnixNano() // 唯一标识，避免与其他测试数据混在一起

	err := logger.Record(ctx, querysvc.AuditEvent{
		ActorID:   actorID,
		ActorName: "alice",
		Action:    "query.execute",
		Object:    "datasource:7",
		Detail:    map[string]any{"sql": "SELECT 1", "row_count": 1},
		IP:        "10.0.0.1",
		UserAgent: "test-agent",
		At:        time.Now(),
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}

	var (
		actorName string
		action    string
		detail    []byte
	)
	err = pool.QueryRow(ctx,
		`SELECT actor_name, action, detail FROM audit_log
		 WHERE actor_id = $1 ORDER BY id DESC LIMIT 1`, actorID,
	).Scan(&actorName, &action, &detail)
	if err != nil {
		t.Fatalf("query back: %v", err)
	}
	if actorName != "alice" {
		t.Errorf("actor_name = %q", actorName)
	}
	if action != "query.execute" {
		t.Errorf("action = %q", action)
	}

	var parsed map[string]any
	if err := json.Unmarshal(detail, &parsed); err != nil {
		t.Fatalf("detail is not valid json: %v", err)
	}
	if parsed["sql"] != "SELECT 1" {
		t.Errorf("detail.sql = %v", parsed["sql"])
	}
	if parsed["row_count"] != float64(1) {
		t.Errorf("detail.row_count = %v", parsed["row_count"])
	}
}

func TestLogger_HandlesNilDetail(t *testing.T) {
	pool := setupPool(t)
	logger := NewLogger(pool)

	err := logger.Record(context.Background(), querysvc.AuditEvent{
		ActorID: time.Now().UnixNano(),
		Action:  "test.nil_detail",
		Detail:  nil,
		At:      time.Now(),
	})
	if err != nil {
		t.Fatalf("nil detail should not error: %v", err)
	}
}

func TestLogger_AuditLogIsImmutable(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	logger := NewLogger(pool)

	actorID := time.Now().UnixNano()
	if err := logger.Record(ctx, querysvc.AuditEvent{
		ActorID: actorID,
		Action:  "test.immutable",
		At:      time.Now(),
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	var id int64
	if err := pool.QueryRow(ctx,
		`SELECT id FROM audit_log WHERE actor_id = $1`, actorID,
	).Scan(&id); err != nil {
		t.Fatalf("select id: %v", err)
	}

	// 触发器必须拦截 UPDATE 与 DELETE
	if _, err := pool.Exec(ctx,
		`UPDATE audit_log SET action = 'tampered' WHERE id = $1`, id); err == nil {
		t.Error("UPDATE on audit_log must be blocked by trigger")
	}
	if _, err := pool.Exec(ctx,
		`DELETE FROM audit_log WHERE id = $1`, id); err == nil {
		t.Error("DELETE on audit_log must be blocked by trigger")
	}

	// 确认原值未变
	var action string
	if err := pool.QueryRow(ctx,
		`SELECT action FROM audit_log WHERE id = $1`, id).Scan(&action); err != nil {
		t.Fatalf("re-select: %v", err)
	}
	if action != "test.immutable" {
		t.Errorf("action should be unchanged, got %q", action)
	}
}
