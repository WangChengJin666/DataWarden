package authz

import "testing"

func TestEnforce_ExactScope(t *testing.T) {
	e, err := NewEnforcer()
	if err != nil {
		t.Fatalf("new enforcer: %v", err)
	}
	e.AddPolicy("u:alice", "ds:1:shop:orders", "read")

	if !e.Enforce("u:alice", "ds:1:shop:orders", "read") {
		t.Error("should allow read on granted table")
	}
	if e.Enforce("u:alice", "ds:1:shop:users", "read") {
		t.Error("must deny read on table outside grant")
	}
	if e.Enforce("u:bob", "ds:1:shop:orders", "read") {
		t.Error("must deny user with no grant")
	}
}

func TestEnforce_WildcardScope(t *testing.T) {
	e, err := NewEnforcer()
	if err != nil {
		t.Fatalf("new enforcer: %v", err)
	}
	e.AddPolicy("u:alice", "ds:1:shop:*", "read")

	if !e.Enforce("u:alice", "ds:1:shop:orders", "read") {
		t.Error("wildcard scope should cover any table in db")
	}
	if e.Enforce("u:alice", "ds:1:other:orders", "read") {
		t.Error("wildcard must not cross database boundary")
	}
}

func TestEnforce_WrongAction(t *testing.T) {
	e, err := NewEnforcer()
	if err != nil {
		t.Fatalf("new enforcer: %v", err)
	}
	e.AddPolicy("u:alice", "ds:1:shop:*", "read")

	if e.Enforce("u:alice", "ds:1:shop:orders", "write") {
		t.Error("read grant must not allow write")
	}
}

func TestEnforce_RoleGrant(t *testing.T) {
	e, err := NewEnforcer()
	if err != nil {
		t.Fatalf("new enforcer: %v", err)
	}
	if err := e.AddRoleForUser("u:dave", "dba"); err != nil {
		t.Fatalf("add role: %v", err)
	}
	e.AddPolicy("dba", "ds:*", "read")

	if !e.Enforce("u:dave", "ds:9:any_db:any_tbl", "read") {
		t.Error("dba role should read any datasource")
	}
}

func TestSyncGrants(t *testing.T) {
	e, err := NewEnforcer()
	if err != nil {
		t.Fatalf("new enforcer: %v", err)
	}
	grants := []GrantPolicy{
		{UserID: 1, DatasourceID: 2, Scope: "shop:orders", Action: "read"},
		{UserID: 3, DatasourceID: 2, Scope: "shop:*", Action: "read"},
	}
	if err := e.SyncGrants(grants); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if !e.Enforce("u:1", "ds:2:shop:orders", "read") {
		t.Error("grant from DB should be effective")
	}
	if !e.Enforce("u:3", "ds:2:shop:users", "read") {
		t.Error("wildcard grant from DB should be effective")
	}
	if e.Enforce("u:1", "ds:2:shop:users", "read") {
		t.Error("non-wildcard grant must not cover other tables")
	}
}

// TestSyncGrants_DoesNotWipeRoles 是关键回归测试：
// 早期实现用 ClearPolicy() 会把角色绑定一起清掉，导致 DBA 权限失效。
// 真实启动顺序是：SyncRoles → SyncGrants → 添加内置角色策略。
func TestSyncGrants_DoesNotWipeRoles(t *testing.T) {
	e, err := NewEnforcer()
	if err != nil {
		t.Fatalf("new enforcer: %v", err)
	}
	if err := e.SyncRoles(map[int64]string{9: "dba"}); err != nil {
		t.Fatalf("sync roles: %v", err)
	}

	// SyncGrants 清的是 p 段（用户直接授权），不应影响 g 段（角色绑定）
	if err := e.SyncGrants([]GrantPolicy{
		{UserID: 1, DatasourceID: 2, Scope: "shop:orders", Action: "read"},
	}); err != nil {
		t.Fatalf("sync grants: %v", err)
	}

	// 角色绑定必须仍然存在
	roles, err := e.e.GetRolesForUser("u:9")
	if err != nil {
		t.Fatalf("get roles: %v", err)
	}
	if len(roles) != 1 || roles[0] != "dba" {
		t.Fatalf("role binding should survive SyncGrants, got %v", roles)
	}

	// 加上内置角色策略后，dba 权限必须生效
	e.AddPolicy("dba", "ds:*", "read")
	if !e.Enforce("u:9", "ds:5:any:thing", "read") {
		t.Fatal("dba role should grant read after SyncRoles + SyncGrants + AddPolicy")
	}

	// 用户直接授权也应保留
	if !e.Enforce("u:1", "ds:2:shop:orders", "read") {
		t.Error("user grant should still be effective")
	}
}

func TestSyncGrants_ReplacesOldGrants(t *testing.T) {
	e, err := NewEnforcer()
	if err != nil {
		t.Fatalf("new enforcer: %v", err)
	}
	_ = e.SyncGrants([]GrantPolicy{{UserID: 1, DatasourceID: 2, Scope: "shop:orders", Action: "read"}})
	if !e.Enforce("u:1", "ds:2:shop:orders", "read") {
		t.Fatal("first grant should work")
	}

	// 换成另一条，旧的必须失效（授权过期/撤销的场景）
	_ = e.SyncGrants([]GrantPolicy{{UserID: 1, DatasourceID: 2, Scope: "shop:users", Action: "read"}})
	if e.Enforce("u:1", "ds:2:shop:orders", "read") {
		t.Error("old grant must be removed after sync")
	}
	if !e.Enforce("u:1", "ds:2:shop:users", "read") {
		t.Error("new grant should be effective")
	}
}
