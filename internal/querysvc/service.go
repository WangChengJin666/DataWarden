package querysvc

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/opensourceprj/datawarden/internal/adapter"
)

var (
	// ErrForbidden 表示用户对目标库无读权限。
	ErrForbidden = errors.New("forbidden")
	// ErrNoDatabase 表示请求未声明目标库，无法做库级鉴权。
	//
	// 曾经叫 ErrNoTables（逐表鉴权时代的命名）。授权粒度改成「数据源 + 库」
	// 后表清单不再参与鉴权，缺表是合法的、缺库才是错误，故更名。
	ErrNoDatabase = errors.New("no database declared in request")
	// ErrNoCollection 表示 MongoDB 查询未指定集合，无法定位查询目标。
	// 这是执行前置条件，不是权限错误。
	ErrNoCollection = errors.New("no collection declared in request")
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
	// Tables 是本次查询涉及的表，**不参与鉴权**，只写进审计便于事后追溯。
	// 传空合法：授权粒度是库，用户不需要为了执行一条 SQL 先去勾表。
	Tables       []string
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
//
// 授权粒度是「数据源 + 库」：用户对某个库有 read 权限，该库下的所有表
// 都可以查。鉴权因此只需要一次库级判定，不再逐表判定——
//
//	obj := ds:<id>:<库>:*
//
// 为什么不再逐表：一张一张表去勾选，对使用者是纯粹的负担（查一条 JOIN
// 就要把涉及的每张表都记得勾上，漏一张就是 403），对平台也不增加任何
// 安全性——表级 scope 和整库 scope 都由同一条 Casbin 策略表达，
// 区别只在「授权申请时要不要细化」，而现在产品上只申请到库。
//
// 保留 req.Tables 的用途只有审计：记下这次查了哪些表，便于事后追溯，
// 但它不参与鉴权，传空也合法。表名是用户自述的，不能拿来当权限依据——
// 真按它鉴权，用户只要故意少报一张表就能绕过（SQL 实际访问了哪张表
// 由数据库决定，不由请求体决定）。
func (s *Service) ExecuteSQL(ctx context.Context, req SQLRequest, ad adapter.Adapter) (*QueryResult, error) {
	if s.Rules == nil {
		s.Rules = DefaultRules()
	}

	// 1. 库级鉴权：任一库名缺失都无法判定，直接拒绝（fail-closed）
	//
	// 空库名**不是**安全的：拼出的 ds:<id>::* 会被 keyMatch 的 * 后缀
	// 命中管理角色的 ds:*:*:*，而 SQL 实际在哪执行取决于连接——
	// 鉴权对象与执行目标不一致。所以库名必须非空。
	//
	// req.Database 的非空由 handler 保证（httpapi.sqlQueryRequest 上有
	// binding:"required"），这里仍判一次：将来新增内部调用方
	// （批处理、CLI）时不能指望它们遵守 handler 的约束。
	if strings.TrimSpace(req.Database) == "" {
		s.audit(ctx, req, "query.denied", map[string]any{"reason": "no database declared"})
		return nil, ErrNoDatabase
	}

	obj := fmt.Sprintf("ds:%d:%s:*", req.DatasourceID, req.Database)
	if !s.Enforcer.Enforce(req.Subject, obj, "read") {
		// 不提示具体缺什么权限：库级的拒绝已经足够明确，
		// 再细说就等于告诉用户「这个数据源上有哪些库」
		s.audit(ctx, req, "query.denied", map[string]any{
			"reason":   "no grant",
			"database": req.Database,
		})
		return nil, ErrForbidden
	}

	// 2. 只读校验
	if !adapter.IsReadOnlySQL(req.SQL) {
		s.audit(ctx, req, "query.denied", map[string]any{"reason": "not read-only"})
		return nil, ErrNotReadOnly
	}

	// 3. 带超时执行
	//
	// 有库名就用 QueryInDatabase：MySQL 的连接串只带数据源配置的默认库，
	// 用户在查询页选的库从未参与连接，SQL 里写裸表名（补全会主动省掉
	// 库前缀）时直接报 Error 1046 No database selected。其它类型不实现
	// 该方法，走原来的 Query——它们的库名要么在连接串里，要么随命令传。
	limit := s.rowLimit(req.RowLimit)
	execCtx, cancel := context.WithTimeout(ctx, s.executeTimeout())
	defer cancel()

	start := time.Now()
	res, err := queryInDatabase(execCtx, ad, req.Database, req.SQL, limit)
	if err != nil {
		s.audit(ctx, req, "query.failed", map[string]any{
			"error":      err.Error(),
			"elapsed_ms": time.Since(start).Milliseconds(),
		})
		return nil, fmt.Errorf("execute query: %w", err)
	}

	// 4. 脱敏
	s.Rules.MaskResult(res)

	// 5. 审计成功
	s.audit(ctx, req, "query.execute", map[string]any{
		"row_count":  res.RowCount,
		"truncated":  res.Truncated,
		"elapsed_ms": res.ElapsedMs,
	})
	return res, nil
}

// queryInDatabase 在 req 声明的库下执行查询，适配器不支持时退回默认连接。
//
// 这里只做「怎么执行」，不做任何权限判断：库名已经在上一步按
// ds:<id>:<库>:<表> 鉴过权，能走到这里的库名都是用户有权访问的。
func queryInDatabase(ctx context.Context, ad adapter.Adapter, db, sqlText string, limit int) (*adapter.QueryResult, error) {
	if q, ok := ad.(adapter.DatabaseQuerier); ok && db != "" {
		return q.QueryInDatabase(ctx, db, sqlText, limit)
	}
	return ad.Query(ctx, sqlText, limit)
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
