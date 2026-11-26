package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/opensourceprj/datawarden/internal/adapter"
	"github.com/opensourceprj/datawarden/internal/audit"
	"github.com/opensourceprj/datawarden/internal/auth"
	"github.com/opensourceprj/datawarden/internal/authz"
	"github.com/opensourceprj/datawarden/internal/config"
	"github.com/opensourceprj/datawarden/internal/db"
	"github.com/opensourceprj/datawarden/internal/httpapi"
	"github.com/opensourceprj/datawarden/internal/model"
	"github.com/opensourceprj/datawarden/internal/querysvc"
	"github.com/opensourceprj/datawarden/internal/redisq"
	"github.com/opensourceprj/datawarden/internal/store"
)

func main() {
	cfg, err := config.LoadFromOS()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if cfg.JWTSecret == "" {
		log.Fatal("JWT_SECRET is required")
	}
	if cfg.JWTSecret == "dev-only-secret-do-not-use-in-production" {
		log.Println("WARNING: using development JWT_SECRET, replace it before production")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	log.Println("database migrated")

	// 依赖装配
	userStore := store.NewUserStore(pool)
	dsStore := store.NewDatasourceStore(pool)
	grantStore := store.NewGrantStore(pool)
	grantReqStore := store.NewGrantRequestStore(pool)
	groupStore := store.NewGroupStore(pool)
	auditLogger := audit.NewLogger(pool)
	issuer := auth.NewTokenIssuer(cfg.JWTSecret, 12*time.Hour)

	enforcer, err := authz.NewEnforcer()
	if err != nil {
		log.Fatalf("init enforcer: %v", err)
	}
	if err := syncAuthz(ctx, enforcer, userStore, grantStore); err != nil {
		log.Fatalf("sync authz: %v", err)
	}
	log.Println("authz policies loaded")

	// 内置角色策略：dba 与 superadmin 可读全部数据源
	reAddBuiltinPolicies(enforcer)

	querySvc := &querysvc.Service{
		Enforcer:        enforcer,
		Auditor:         auditLogger,
		Rules:           querysvc.DefaultRules(),
		Timeout:         time.Duration(cfg.QueryTimeoutSec) * time.Second,
		DefaultRowLimit: cfg.QueryRowLimit,
	}

	// 审批通过后立即刷新 Casbin，无需重启
	refreshAuthz := func(ctx context.Context) {
		if err := syncAuthz(ctx, enforcer, userStore, grantStore); err != nil {
			log.Printf("refresh authz failed: %v", err)
			return
		}
		reAddBuiltinPolicies(enforcer)
		log.Println("authz refreshed after approval")
	}

	// 定时重建策略：授权带 expires_at，而过期只体现在数据库里。
	// 不刷的话，一条过期授权会一直生效到下次重启——这是真实越权窗口。
	// 审批通过走的 refreshAuthz 是「即时」路径，这里负责兜住「自然过期」。
	go func() {
		interval := time.Duration(cfg.AuthzRefreshSec) * time.Second
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := syncAuthz(ctx, enforcer, userStore, grantStore); err != nil {
					log.Printf("periodic authz refresh failed: %v", err)
					continue
				}
				reAddBuiltinPolicies(enforcer)
			}
		}
	}()

	router := httpapi.NewRouter(httpapi.Deps{
		Issuer:      issuer,
		Auditor:     auditLogger,
		AuthHandler: &httpapi.AuthHandler{Users: userStore, Issuer: issuer},
		Datasource: &httpapi.DatasourceHandler{
			Store:  dsStore,
			Tester: adapterProbe{},
		},
		Query: &httpapi.QueryHandler{
			Service: querySvc,
			Factory: makeAdapterFactory(ctx, dsStore),
		},
		Export: &httpapi.ExportHandler{
			Service: querySvc,
			Factory: makeAdapterFactory(ctx, dsStore),
			// 导出闸门读的是 grant_record.can_export（单个数据源一条授权），
			// 所以 A 源批了导出、B 源没批时，B 源的导出请求会在这里被拒。
			Grants: grantStore,
			// 只用于把「没查询权限」与「有查询权限但没批导出」区分开，
			// 给出不同的提示；不参与放行判断。
			Enforcer: enforcer,
			// Redis 导出复用查询的同一套连接与 db 号解析。
			RedisFactory: makeRedisTargetFactory(ctx, dsStore, cfg),
		},
		Redis: &httpapi.RedisHandler{
			Enforcer: enforcer,
			Factory:  makeRedisTargetFactory(ctx, dsStore, cfg),
		},
		GrantReq: &httpapi.GrantRequestHandler{
			Requests: grantReqStore,
			Datasrc:  dsStore,
			OnApprove: func(ctx context.Context, g *model.Grant) {
				refreshAuthz(ctx)
			},
		},
		Grant: &httpapi.GrantHandler{
			Store: grantStore,
			// 吊销后立即重建策略：收回权限是应急动作，不能等定时刷新。
			OnRevoke: func(ctx context.Context) {
				refreshAuthz(ctx)
			},
		},
		Metadata: &httpapi.MetadataHandler{
			// 元数据接口同时需要「数据源配置」和「授权 scope」，
			// 用 grantStore 实现 scopeLister，交给类型断言即可。
			Store:    metadataStore{ds: dsStore, grants: grantStore},
			Factory:  makeAdapterFactory(ctx, dsStore),
			Enforcer: enforcer,
		},
		Group: &httpapi.GroupHandler{Store: groupStore},
		// 「修改自己的密码」对所有角色开放。不传 OnPasswordChange：
		// 当前没有 token 吊销机制，改密后已签发的 token 仍会用满有效期
		// （与停用账号的口径一致）。将来做密码版本号时在这里接上即可。
		SelfPassword: &httpapi.SelfPasswordHandler{Users: userStore},
		User: &httpapi.UserHandler{
			Store: userStore,
			// 改角色 / 停用 / 删除后立刻重建策略：
			// 这些动作都是应急或安全相关的，等 60 秒的定时刷新不可接受。
			// 注意 refreshAuthz 内部已包含 reAddBuiltinPolicies，
			// 不会因为重建而清掉 dba/superadmin 的内置策略。
			OnUserChange: func(ctx context.Context) {
				refreshAuthz(ctx)
			},
		},
	})

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.Printf("listening on %s", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http server: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	log.Println("datawarden stopped")
}

// reAddBuiltinPolicies 重新挂上内置角色策略。
//
// SyncGrants 会先清空 p 段再按数据库记录重建，内置的 dba / superadmin 策略
// 不在数据库里，会被一起清掉。所以凡是调用 syncAuthz 之后都必须补一次，
// 否则刷新一次策略，DBA 自己就查不了任何数据了。
//
// 策略对象是 ds:*:*:*（四段全通配），意味着管理角色可以查任何数据源、
// 任何库、任何表，且不需要 grant_record——这是产品上明确的约定：
// 审批人不进审批流程。对象格式与「为什么不能写成 ds:*」见
// authz.BuiltinPolicyObject 的注释。
func reAddBuiltinPolicies(e *authz.Enforcer) {
	for _, sub := range authz.PrivilegedPolicySubjects {
		e.AddPolicy(sub, authz.BuiltinPolicyObject, "read")
	}
}

// metadataStore 把「数据源读取」和「授权 scope 查询」组合成元数据接口需要的存储。
//
// 拆成两个 store 是因为职责不同（一个是配置，一个是权限），
// 但元数据接口两个都要：读配置才知道连哪，读授权才知道能看哪些表。
// 用一个适配器组合起来比让 DatasourceStore 反依赖 GrantStore 更干净。
type metadataStore struct {
	ds     *store.DatasourceStore
	grants *store.GrantStore
}

func (m metadataStore) List(ctx context.Context) ([]model.Datasource, error) {
	return m.ds.List(ctx)
}

func (m metadataStore) Get(ctx context.Context, id int64) (*model.Datasource, error) {
	return m.ds.Get(ctx, id)
}

func (m metadataStore) Create(ctx context.Context, ds *model.Datasource) (int64, error) {
	return m.ds.Create(ctx, ds)
}

func (m metadataStore) Update(ctx context.Context, ds *model.Datasource) error {
	return m.ds.Update(ctx, ds)
}

func (m metadataStore) Delete(ctx context.Context, id int64) error {
	return m.ds.Delete(ctx, id)
}

func (m metadataStore) GrantScopes(ctx context.Context, userID, datasourceID int64) ([]string, error) {
	return m.grants.GrantScopes(ctx, userID, datasourceID)
}

// syncAuthz 用数据库现状重建 Casbin 策略。
// 顺序必须是先 SyncRoles 再 SyncGrants，否则 g 段为空导致角色权限失效。
func syncAuthz(ctx context.Context, e *authz.Enforcer, users *store.UserStore, grants *store.GrantStore) error {
	roles, err := users.AllRoles(ctx)
	if err != nil {
		return err
	}
	if err := e.SyncRoles(roles); err != nil {
		return err
	}

	active, err := grants.ActiveGrants(ctx)
	if err != nil {
		return err
	}
	policies := make([]authz.GrantPolicy, 0, len(active))
	for _, g := range active {
		policies = append(policies, authz.GrantPolicy{
			UserID:       g.UserID,
			DatasourceID: g.DatasourceID,
			Scope:        g.Scope,
			Action:       g.Action,
		})
	}
	return e.SyncGrants(policies)
}

// makeRedisTargetFactory 按数据源 ID 构造 Redis 查询目标。
// db 号来自数据源的 database 字段（Redis 存 db index），密码来自 secret_ref。
func makeRedisTargetFactory(ctx context.Context, dsStore *store.DatasourceStore, cfg *config.Config) httpapi.RedisTargetFactory {
	return func(ctx context.Context, datasourceID int64) (*httpapi.RedisTarget, error) {
		ds, err := dsStore.Get(ctx, datasourceID)
		if err != nil {
			return nil, err
		}
		if ds.Type != model.TypeRedis {
			return nil, fmt.Errorf("datasource %d is not a redis datasource", datasourceID)
		}

		dbIndex, err := redisq.ParseDBIndex(ds.Database)
		if err != nil {
			return nil, err
		}

		password := ds.Password

		// 缓存连接：相同数据源复用同一 client，避免每次查询重新握手
		client, err := redisClients.get(datasourceID, ds, password, dbIndex, cfg.RedisScanMaxKeys)
		if err != nil {
			return nil, err
		}

		return &httpapi.RedisTarget{
			Query:    redisq.NewQueryService(client, cfg.RedisScanMaxKeys),
			Database: dbIndex,
		}, nil
	}
}

// redisClientCache 复用 Redis 连接，避免每次查询都重新 AUTH。
type redisClientCache struct {
	mu      sync.Mutex
	clients map[int64]*redisq.Client
}

var redisClients = &redisClientCache{clients: map[int64]*redisq.Client{}}

func (c *redisClientCache) get(id int64, ds *model.Datasource, password string, dbIndex, maxKeys int) (*redisq.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if cl, ok := c.clients[id]; ok {
		return cl, nil
	}
	addr := fmt.Sprintf("%s:%d", ds.Host, ds.Port)
	cl, err := redisq.NewClient(addr, ds.Username, password, dbIndex, maxKeys)
	if err != nil {
		return nil, err
	}
	c.clients[id] = cl
	return cl, nil
}

// makeAdapterFactory 按数据源 ID 构造 SQL Adapter。
// 当前所有数据源共用一组环境变量账号，生产环境应改为按数据源从密钥管理读取。
func makeAdapterFactory(ctx context.Context, dsStore *store.DatasourceStore) httpapi.AdapterFactory {
	return func(datasourceID int64) (adapter.Adapter, error) {
		ds, err := dsStore.Get(ctx, datasourceID)
		if err != nil {
			return nil, err
		}

		// 账号密码都是数据源自己的配置，不再有全局回落。
		// 之前所有库共用 QUERY_DB_USER 一组账号，等于放弃最小权限。

		return adapter.New(adapter.ConnConfig{
			Type:       string(ds.Type),
			Host:       ds.Host,
			Port:       ds.Port,
			Database:   ds.Database,
			User:       ds.Username,
			Password:   ds.Password,
			AuthSource: ds.AuthSource,
			Options:    ds.Options,
		})
	}
}

// adapterProbe 把 adapter.Probe 适配成 httpapi.ConnTester。
// 单独包一层是为了让 httpapi 不直接依赖 adapter 的探测细节。
type adapterProbe struct{}

func (adapterProbe) Test(ctx context.Context, cfg httpapi.TestConnConfig) (*httpapi.TestConnResult, error) {
	res, err := adapter.Probe(ctx, adapter.ProbeConfig{
		Type:       cfg.Type,
		Host:       cfg.Host,
		Port:       cfg.Port,
		Database:   cfg.Database,
		User:       cfg.User,
		Password:   cfg.Password,
		AuthSource: cfg.AuthSource,
		Options:    cfg.Options,
	})
	if err != nil {
		return nil, err
	}
	return &httpapi.TestConnResult{
		OK:        res.OK,
		Message:   res.Message,
		ElapsedMs: res.ElapsedMs,
	}, nil
}
