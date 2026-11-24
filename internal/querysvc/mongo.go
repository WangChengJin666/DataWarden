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
// 鉴权粒度与 SQL 一致：按「数据源 + 库」判定，object 形如 ds:<id>:<db>:*。
// 有整库授权就能查该库下任何集合，与 SQL 侧「选了库就能查该库所有表」对齐；
// 只授了部分集合的用户仍拿到 ds:<id>:<db>:<coll> 形态的策略，
// 由 keyMatch 逐条命中——细粒度授权没有丢，只是执行时不再强制逐集合声明。
type MongoRequest struct {
	Subject      string
	ActorID      int64
	ActorName    string
	DatasourceID int64
	Database     string
	// Collection 是集合名。没有它就无法定位查询目标，因此仍然必填；
	// 但它不参与鉴权（鉴权在库级完成）。
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

	// 1. 集合必须声明，否则无法定位查询目标
	//
	// 注意这不再是鉴权步骤：授权粒度是「数据源 + 库」，鉴权在下面按
	// ds:<id>:<库>:* 做。集合名缺失只是没法执行，不是权限问题。
	if req.Collection == "" {
		s.auditMongo(ctx, req, "mongo.denied", map[string]any{"reason": "no collection declared"})
		return nil, ErrNoCollection
	}

	// 2. 库级鉴权：ds:<id>:<db>:*
	//
	// 集合名可能带库前缀（适配器支持 "shop.users" 形式），所以先拆出
	// 真正的库名。service 层自己做这一步，不依赖 handler 先处理好——
	// 有多条调用路径时，校验放在最靠近数据的地方才不会被绕过。
	authzDB, _, err := SplitDBCollection(req.Collection, req.Database)
	if err != nil {
		s.auditMongo(ctx, req, "mongo.denied", map[string]any{
			"reason":     "invalid collection",
			"collection": req.Collection,
			"error":      err.Error(),
		})
		return nil, ErrNoDatabase
	}
	if authzDB == "" {
		s.auditMongo(ctx, req, "mongo.denied", map[string]any{"reason": "no database declared"})
		return nil, ErrNoDatabase
	}
	obj := fmt.Sprintf("ds:%d:%s:*", req.DatasourceID, authzDB)
	if !s.Enforcer.Enforce(req.Subject, obj, "read") {
		s.auditMongo(ctx, req, "mongo.denied", map[string]any{
			"reason":     "no grant",
			"collection": req.Collection,
			"db":         authzDB,
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

// SplitDBCollection 把集合名拆成 (库, 集合)，供鉴权拼 object 使用。
//
// 直接转调 adapter.SplitDBCollection，不在这里再写一份实现：
// 执行层和鉴权层必须拆出完全相同的结果，两份实现会各自漂移，
// 一旦不一致就可能「鉴权放行了 A 库、实际查到 B 库」。
// 保留这个转发函数是为了让 service 层的调用点读起来自洽，
// 同时把 «adapter 的拆分是唯一事实来源» 这件事写在调用路径上。
//
// 错误必须原样上抛，不能吞掉：
// 早先这里在 err 时退回 (database, collection) 原值，等于给鉴权层
// 留了一条与执行层不一致的退路——拆分失败时鉴权用一套值、执行用另一套，
// 正是要消除的问题。调用方已经有空集合校验，走不到这里；
// 真走到了说明入参异常，宁可整条查询失败。
func SplitDBCollection(collection, database string) (db, coll string, err error) {
	return adapter.SplitDBCollection(collection, database)
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
