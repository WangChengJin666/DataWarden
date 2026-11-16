package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/opensourceprj/datawarden/internal/auth"
	"github.com/opensourceprj/datawarden/internal/db"
	"github.com/opensourceprj/datawarden/internal/model"
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
	// 清理业务表，保证可重复执行。audit_log 有触发器，不清理。
	_, _ = pool.Exec(ctx, "DELETE FROM grant_record")
	_, _ = pool.Exec(ctx, "DELETE FROM datasource")
	_, _ = pool.Exec(ctx, "DELETE FROM app_user")
	t.Cleanup(pool.Close)
	return pool
}

func TestUserStore_CreateAndFind(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	s := NewUserStore(pool)

	hash, _ := auth.HashPassword("P@ssw0rd!")
	uid, err := s.Create(ctx, &model.User{
		Username:     "alice",
		Email:        "alice@example.com",
		PasswordHash: hash,
		Role:         model.RoleDBA,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if uid == 0 {
		t.Fatal("expected non-zero id")
	}

	id, gotHash, role, err := s.FindByUsername(ctx, "alice")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if id != uid {
		t.Errorf("id = %d, want %d", id, uid)
	}
	if !auth.VerifyPassword(gotHash, "P@ssw0rd!") {
		t.Error("stored hash should verify")
	}
	if role != string(model.RoleDBA) {
		t.Errorf("role = %q, want dba", role)
	}
}

func TestUserStore_FindMissingReturnsError(t *testing.T) {
	pool := setupPool(t)
	s := NewUserStore(pool)

	if _, _, _, err := s.FindByUsername(context.Background(), "nobody"); err == nil {
		t.Fatal("expected error for missing user")
	}
}

func TestUserStore_DuplicateUsernameRejected(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	s := NewUserStore(pool)

	hash, _ := auth.HashPassword("x")
	_, err := s.Create(ctx, &model.User{Username: "bob", PasswordHash: hash, Role: model.RoleDeveloper})
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := s.Create(ctx, &model.User{Username: "bob", PasswordHash: hash, Role: model.RoleDeveloper}); err == nil {
		t.Fatal("duplicate username must be rejected")
	}
}

func TestUserStore_AllRoles(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	s := NewUserStore(pool)

	hash, _ := auth.HashPassword("x")
	id1, _ := s.Create(ctx, &model.User{Username: "u1", PasswordHash: hash, Role: model.RoleDBA})
	id2, _ := s.Create(ctx, &model.User{Username: "u2", PasswordHash: hash, Role: model.RoleDeveloper})

	roles, err := s.AllRoles(ctx)
	if err != nil {
		t.Fatalf("all roles: %v", err)
	}
	if roles[id1] != string(model.RoleDBA) {
		t.Errorf("u1 role = %q", roles[id1])
	}
	if roles[id2] != string(model.RoleDeveloper) {
		t.Errorf("u2 role = %q", roles[id2])
	}
}

func TestDatasourceStore_CreateAndList(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()
	s := NewDatasourceStore(pool)

	id, err := s.Create(ctx, &model.Datasource{
		Name:     "shop-prod",
		Type:     model.TypePostgreSQL,
		Host:     "10.0.0.5",
		Port:     5432,
		Database: "shop",
		Username: "readonly",
		Password: "plain-pass",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	items, err := s.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 datasource, got %d", len(items))
	}
	if items[0].ID != id || items[0].Name != "shop-prod" {
		t.Errorf("unexpected datasource: %+v", items[0])
	}
	if items[0].Type != model.TypePostgreSQL {
		t.Errorf("type = %q", items[0].Type)
	}

	got, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Host != "10.0.0.5" {
		t.Errorf("host = %q", got.Host)
	}
}

func TestDatasourceStore_GetMissing(t *testing.T) {
	pool := setupPool(t)
	s := NewDatasourceStore(pool)
	if _, err := s.Get(context.Background(), 999999); err == nil {
		t.Fatal("expected error for missing datasource")
	}
}

func TestGrantStore_ActiveGrantsExcludesExpired(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()

	users := NewUserStore(pool)
	uid, _ := users.Create(ctx, &model.User{Username: "bob", PasswordHash: "x", Role: model.RoleDeveloper})

	dss := NewDatasourceStore(pool)
	dsID, _ := dss.Create(ctx, &model.Datasource{
		Name: "ds1", Type: model.TypePostgreSQL, Host: "h", Port: 5432, Username: "u", Password: "e",
	})

	gs := NewGrantStore(pool)

	if _, err := gs.Create(ctx, &model.Grant{
		UserID: uid, DatasourceID: dsID, Scope: "shop:*", Action: "read",
		ExpiresAt: futureTime(),
	}); err != nil {
		t.Fatalf("create active: %v", err)
	}
	if _, err := gs.Create(ctx, &model.Grant{
		UserID: uid, DatasourceID: dsID, Scope: "other:*", Action: "read",
		ExpiresAt: pastTime(),
	}); err != nil {
		t.Fatalf("create expired: %v", err)
	}

	active, err := gs.ActiveGrants(ctx)
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("expected 1 active grant, got %d", len(active))
	}
	if active[0].Scope != "shop:*" {
		t.Errorf("scope = %q", active[0].Scope)
	}
}

func TestGrantStore_RevokeIdempotent(t *testing.T) {
	pool := setupPool(t)
	ctx := context.Background()

	users := NewUserStore(pool)
	uid, _ := users.Create(ctx, &model.User{Username: "carol", PasswordHash: "x", Role: model.RoleDeveloper})
	dss := NewDatasourceStore(pool)
	dsID, _ := dss.Create(ctx, &model.Datasource{
		Name: "ds2", Type: model.TypePostgreSQL, Host: "h", Port: 5432, Username: "u", Password: "e",
	})

	gs := NewGrantStore(pool)
	gid, _ := gs.Create(ctx, &model.Grant{
		UserID: uid, DatasourceID: dsID, Scope: "shop:*", Action: "read", ExpiresAt: futureTime(),
	})

	if err := gs.Revoke(ctx, gid); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	// 二次撤销不应报错
	if err := gs.Revoke(ctx, gid); err != nil {
		t.Fatalf("revoke should be idempotent: %v", err)
	}

	active, _ := gs.ActiveGrants(ctx)
	if len(active) != 0 {
		t.Errorf("revoked grant must not be active, got %d", len(active))
	}
}

func futureTime() time.Time { return time.Now().Add(24 * time.Hour) }
func pastTime() time.Time   { return time.Now().Add(-24 * time.Hour) }
