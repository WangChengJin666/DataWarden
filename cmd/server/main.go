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
	"github.com/opensourceprj/datawarden/internal/secret"
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
	auditLogger := audit.NewLogger(pool)
	// 主密钥用于加解密数据源密码。缺失时 config.Load 已经报错退出，
	// 这里再做一次构建校验，避免把非法长度的密钥带进运行期。
	cipher, err := secret.NewCipher(cfg.MasterKey)
	if err != nil {
		log.Fatalf("init password cipher: %v", err)
	}
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
	enforcer.AddPolicy("dba", "ds:*", "read")
	enforcer.AddPolicy("superadmin", "ds:*", "read")

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
		enforcer.AddPolicy("dba", "ds:*", "read")
		enforcer.AddPolicy("superadmin", "ds:*", "read")
		log.Println("authz refreshed after approval")
	}

	router := httpapi.NewRouter(httpapi.Deps{
		Issuer:      issuer,
		Auditor:     auditLogger,
		AuthHandler: &httpapi.AuthHandler{Users: userStore, Issuer: issuer},
		Datasource: &httpapi.DatasourceHandler{
			Store:  dsStore,
			Cipher: cipher,
			Tester: adapterProbe{},
		},
		Query: &httpapi.QueryHandler{
			Service: querySvc,
			Factory: makeAdapterFactory(ctx, dsStore, cipher),
		},
		Redis: &httpapi.RedisHandler{
			Enforcer: enforcer,
			Factory:  makeRedisTargetFactory(ctx, dsStore, cipher, cfg),
		},
		GrantReq: &httpapi.GrantRequestHandler{
			Requests: grantReqStore,
			Datasrc:  dsStore,
			OnApprove: func(ctx context.Context, g *model.Grant) {
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
func makeRedisTargetFactory(ctx context.Context, dsStore *store.DatasourceStore, cipher *secret.Cipher, cfg *config.Config) httpapi.RedisTargetFactory {
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

		// 解密该数据源自己的密码。解密失败说明主密钥换了，
		// 必须明确报错，不能静默当成空密码去连。
		password, err := cipher.Decrypt(ds.PasswordEnc)
		if err != nil {
			return nil, fmt.Errorf("datasource %d: %w", datasourceID, err)
		}

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
	cl, err := redisq.NewClient(addr, password, dbIndex, maxKeys)
	if err != nil {
		return nil, err
	}
	c.clients[id] = cl
	return cl, nil
}

// makeAdapterFactory 按数据源 ID 构造 SQL Adapter。
// 当前所有数据源共用一组环境变量账号，生产环境应改为按数据源从密钥管理读取。
func makeAdapterFactory(ctx context.Context, dsStore *store.DatasourceStore, cipher *secret.Cipher) httpapi.AdapterFactory {
	return func(datasourceID int64) (adapter.Adapter, error) {
		ds, err := dsStore.Get(ctx, datasourceID)
		if err != nil {
			return nil, err
		}

		// 账号密码都是数据源自己的配置，不再有全局回落。
		// 之前所有库共用 QUERY_DB_USER 一组账号，等于放弃最小权限。
		password, err := cipher.Decrypt(ds.PasswordEnc)
		if err != nil {
			return nil, fmt.Errorf("datasource %d: %w", datasourceID, err)
		}

		return adapter.New(adapter.ConnConfig{
			Type:     string(ds.Type),
			Host:     ds.Host,
			Port:     ds.Port,
			Database: ds.Database,
			User:     ds.Username,
			Password: password,
		})
	}
}

// adapterProbe 把 adapter.Probe 适配成 httpapi.ConnTester。
// 单独包一层是为了让 httpapi 不直接依赖 adapter 的探测细节。
type adapterProbe struct{}

func (adapterProbe) Test(ctx context.Context, cfg httpapi.TestConnConfig) (*httpapi.TestConnResult, error) {
	res, err := adapter.Probe(ctx, adapter.ProbeConfig{
		Type:     cfg.Type,
		Host:     cfg.Host,
		Port:     cfg.Port,
		Database: cfg.Database,
		User:     cfg.User,
		Password: cfg.Password,
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
