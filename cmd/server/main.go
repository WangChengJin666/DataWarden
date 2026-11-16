package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/opensourceprj/datawarden/internal/adapter"
	"github.com/opensourceprj/datawarden/internal/audit"
	"github.com/opensourceprj/datawarden/internal/auth"
	"github.com/opensourceprj/datawarden/internal/authz"
	"github.com/opensourceprj/datawarden/internal/config"
	"github.com/opensourceprj/datawarden/internal/db"
	"github.com/opensourceprj/datawarden/internal/httpapi"
	"github.com/opensourceprj/datawarden/internal/querysvc"
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
	enforcer.AddPolicy("dba", "ds:*", "read")
	enforcer.AddPolicy("superadmin", "ds:*", "read")

	querySvc := &querysvc.Service{
		Enforcer:        enforcer,
		Auditor:         auditLogger,
		Rules:           querysvc.DefaultRules(),
		Timeout:         time.Duration(cfg.QueryTimeoutSec) * time.Second,
		DefaultRowLimit: cfg.QueryRowLimit,
	}

	router := httpapi.NewRouter(httpapi.Deps{
		Issuer:      issuer,
		AuthHandler: &httpapi.AuthHandler{Users: userStore, Issuer: issuer},
		Datasource:  &httpapi.DatasourceHandler{Store: dsStore},
		Query: &httpapi.QueryHandler{
			Service: querySvc,
			Factory: makeAdapterFactory(ctx, dsStore, cfg),
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

// makeAdapterFactory 按数据源 ID 构造 SQL Adapter。
// 当前所有数据源共用一组环境变量账号，生产环境应改为按数据源从密钥管理读取。
func makeAdapterFactory(ctx context.Context, dsStore *store.DatasourceStore, cfg *config.Config) httpapi.AdapterFactory {
	return func(datasourceID int64) (adapter.Adapter, error) {
		ds, err := dsStore.Get(ctx, datasourceID)
		if err != nil {
			return nil, err
		}
		return adapter.New(adapter.ConnConfig{
			Type:     string(ds.Type),
			Host:     ds.Host,
			Port:     ds.Port,
			Database: ds.Database,
			User:     cfg.DBUserForQuery,
			Password: cfg.DBPasswordForQuery,
		})
	}
}
