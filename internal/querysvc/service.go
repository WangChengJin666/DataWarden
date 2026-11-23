package querysvc

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/opensourceprj/datawarden/internal/adapter"
)

var (
	// ErrForbidden 表示用户对某张表无读权限，或未提供表清单。
	ErrForbidden = errors.New("forbidden")
	// ErrNoTables 表示请求未声明涉及的表，无法做逐表鉴权。
	ErrNoTables = errors.New("no tables declared in request")
	// ErrNotReadOnly 表示语句不是只读的。
	ErrNotReadOnly = errors.New("statement is not read-only")
)

// Enforcer 是权限判定的最小接口，便于测试替换。
type Enforcer interface {
	Enforce(subject, object, action string) bool
}

// AuditEvent 是一次审计记录。
type AuditEvent struct {
	ActorID   int64
	ActorName string
	Action    string
	Object    string
	Detail    map[string]any
	IP        string
	UserAgent string
	At        time.Time
}

// Auditor 负责落库审计。
type Auditor interface {
	Record(ctx context.Context, e AuditEvent) error
}

// SQLRequest 是一次受控查询请求。
type SQLRequest struct {
	Subject      string // "u:42"
	ActorID      int64
	ActorName    string
	DatasourceID int64
	Database     string
	Tables       []string // 查询涉及的表，用于逐表鉴权
	SQL          string
	RowLimit     int
	IP           string
	UserAgent    string
}

// Service 编排查询流程：鉴权 → 只读校验 → 限行 → 执行 → 脱敏 → 审计。
type Service struct {
	Enforcer Enforcer
	Auditor  Auditor
	Rules    Rules
	Timeout  time.Duration
	// DefaultRowLimit 为 0 时使用 500。
	DefaultRowLimit int
}

func (s *Service) executeTimeout() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	return 30 * time.Second
}

func (s *Service) rowLimit(req int) int {
	if req > 0 {
		return req
	}
	if s.DefaultRowLimit > 0 {
		return s.DefaultRowLimit
	}
	return 500
}

// ExecuteSQL 执行受控查询。ad 由调用方按数据源构造，便于测试注入。
func (s *Service) ExecuteSQL(ctx context.Context, req SQLRequest, ad adapter.Adapter) (*QueryResult, error) {
	if s.Rules == nil {
		s.Rules = DefaultRules()
	}

	// 1. 必须声明涉及的表，否则无法鉴权
	if len(req.Tables) == 0 {
		s.audit(ctx, req, "query.denied", map[string]any{"reason": "no tables declared"})
		return nil, ErrNoTables
	}

	// 2. 逐表鉴权：任一表无权限则整条拒绝，且不暴露是哪张表
	//
	// req.Database 的非空由 handler 保证（httpapi.sqlQueryRequest 上有
	// binding:"required"，唯一调用点 query_handler.go），这里不重复判空。
	//
	// 但要注意空库名**不是**安全的：拼出的 ds:<id>::<tbl> 会被
	// keyMatch 的 * 后缀通配命中，即「有整库授权（ds:<id>:*）的用户
	// 传空库名时鉴权会放行」，而 SQL 实际在连接默认库上执行——
	// 鉴权对象与执行目标不一致。对表级授权才是 fail-closed。
	// 因此这里的安全完全依赖上游的必填校验；若将来新增内部调用方
	// （批处理、CLI），必须自己保证 Database 非空。
	for _, tbl := range req.Tables {
		obj := fmt.Sprintf("ds:%d:%s:%s", req.DatasourceID, req.Database, tbl)
		if !s.Enforcer.Enforce(req.Subject, obj, "read") {
			s.audit(ctx, req, "query.denied", map[string]any{
				"reason": "no grant",
				"table":  tbl,
			})
			return nil, ErrForbidden
		}
	}

	// 3. 只读校验
	if !adapter.IsReadOnlySQL(req.SQL) {
		s.audit(ctx, req, "query.denied", map[string]any{"reason": "not read-only"})
		return nil, ErrNotReadOnly
	}

	// 4. 带超时执行
	limit := s.rowLimit(req.RowLimit)
	execCtx, cancel := context.WithTimeout(ctx, s.executeTimeout())
	defer cancel()

	start := time.Now()
	res, err := ad.Query(execCtx, req.SQL, limit)
	if err != nil {
		s.audit(ctx, req, "query.failed", map[string]any{
			"error":      err.Error(),
			"elapsed_ms": time.Since(start).Milliseconds(),
		})
		return nil, fmt.Errorf("execute query: %w", err)
	}

	// 5. 脱敏
	s.Rules.MaskResult(res)

	// 6. 审计成功
	s.audit(ctx, req, "query.execute", map[string]any{
		"row_count":  res.RowCount,
		"truncated":  res.Truncated,
		"elapsed_ms": res.ElapsedMs,
	})
	return res, nil
}

// audit 写入审计事件。审计失败只记录日志，不阻断业务
// —— 否则数据库抖动会让所有查询不可用。
func (s *Service) audit(ctx context.Context, req SQLRequest, action string, detail map[string]any) {
	if s.Auditor == nil {
		return
	}
	if detail == nil {
		detail = map[string]any{}
	}
	detail["sql"] = req.SQL
	detail["datasource_id"] = req.DatasourceID
	detail["database"] = req.Database
	detail["tables"] = req.Tables

	err := s.Auditor.Record(ctx, AuditEvent{
		ActorID:   req.ActorID,
		ActorName: req.ActorName,
		Action:    action,
		Object:    fmt.Sprintf("datasource:%d", req.DatasourceID),
		Detail:    detail,
		IP:        req.IP,
		UserAgent: req.UserAgent,
		At:        time.Now(),
	})
	if err != nil {
		log.Printf("audit write failed (action=%s ds=%d): %v", action, req.DatasourceID, err)
	}
}
