package querysvc

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/opensourceprj/datawarden/internal/adapter"
)

// MongoRequest 是一次受控的 MongoDB 查询请求。
//
// 鉴权粒度与 SQL 一致：按「数据源 + 库 + 集合」判定，
// object 形如 ds:<id>:<db>:<collection>。
type MongoRequest struct {
	Subject      string
	ActorID      int64
	ActorName    string
	DatasourceID int64
	Database     string
	// Collection 是集合名，只能有一个：一次查一个集合，便于逐集合鉴权。
	Collection string
	Op         string
	Args       string
	RowLimit   int
	IP         string
	UserAgent  string
}

// ExecuteMongo 执行受控 MongoDB 查询。
// 流程与 ExecuteSQL 对齐：鉴权 → 操作白名单 → 执行 → 脱敏 → 审计。
func (s *Service) ExecuteMongo(ctx context.Context, req MongoRequest, ad adapter.Adapter) (*QueryResult, error) {
	if s.Rules == nil {
		s.Rules = DefaultRules()
	}

	// 1. 集合必须声明，否则无法鉴权
	if req.Collection == "" {
		s.auditMongo(ctx, req, "mongo.denied", map[string]any{"reason": "no collection declared"})
		return nil, ErrNoTables
	}

	// 2. 鉴权：ds:<id>:<db>:<collection>
	obj := fmt.Sprintf("ds:%d:%s:%s", req.DatasourceID, req.Database, req.Collection)
	if !s.Enforcer.Enforce(req.Subject, obj, "read") {
		s.auditMongo(ctx, req, "mongo.denied", map[string]any{
			"reason":     "no grant",
			"collection": req.Collection,
		})
		return nil, ErrForbidden
	}

	// 3. 执行（操作白名单在 adapter.QueryMongo 内再次校验）
	limit := s.rowLimit(req.RowLimit)
	execCtx, cancel := context.WithTimeout(ctx, s.executeTimeout())
	defer cancel()

	start := time.Now()
	res, err := adapter.QueryMongo(execCtx, ad, adapter.MongoQueryRequest{
		Database:   req.Database,
		Collection: req.Collection,
		Op:         req.Op,
		Args:       req.Args,
		Limit:      limit,
	})
	if err != nil {
		s.auditMongo(ctx, req, "mongo.failed", map[string]any{
			"error":      err.Error(),
			"elapsed_ms": time.Since(start).Milliseconds(),
		})
		return nil, fmt.Errorf("execute mongo query: %w", err)
	}

	// 4. 脱敏：Mongo 的文档字段名同样按关键字匹配
	s.Rules.MaskResult(res)

	// 5. 审计
	s.auditMongo(ctx, req, "mongo.execute", map[string]any{
		"op":         req.Op,
		"row_count":  res.RowCount,
		"truncated":  res.Truncated,
		"elapsed_ms": res.ElapsedMs,
	})
	return res, nil
}

func (s *Service) auditMongo(ctx context.Context, req MongoRequest, action string, detail map[string]any) {
	if s.Auditor == nil {
		return
	}
	if detail == nil {
		detail = map[string]any{}
	}
	detail["datasource_id"] = req.DatasourceID
	detail["database"] = req.Database
	detail["collection"] = req.Collection
	detail["op"] = req.Op
	detail["args"] = req.Args

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
