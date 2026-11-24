package authz

import "testing"

// 管理角色的内置策略必须是「全数据源、全库、全表」。
//
// 这条测试存在的原因是一个真实事故：策略写的是 ds:*（三段），
// 而鉴权对象是 ds:<id>:<库>:<表>（四段）。表面上 keyMatch 的前缀匹配
// 能让它侥幸命中，但一旦有人调整 object 拼装方式（比如给 Redis 加类型前缀），
// 这条策略就会静默失效，现象是「管理员突然查不了任何数据」——
// 报错还是 403，看起来和「没授权」完全一样，排查会直接跑偏。
func TestBuiltinPoliciesAllowEverything(t *testing.T) {
	e, err := NewEnforcer()
	if err != nil {
		t.Fatalf("new enforcer: %v", err)
	}
	for _, sub := range PrivilegedPolicySubjects {
		if ok := e.AddPolicy(sub, BuiltinPolicyObject, "read"); !ok {
			t.Fatalf("add policy for %s failed", sub)
		}
	}

	// 覆盖三种数据源的对象形态：SQL 的 schema/表、Redis 的数字 db、Mongo 的集合
	objects := []string{
		"ds:1:public:orders",
		"ds:99:shop:order_items",
		"ds:2:37:user:1001",
		"ds:14:project_cooper:users",
		"ds:13::dw_probe",
	}
	subjects := append(PrivilegedPolicySubjects, "u:3", "developer")
	for _, sub := range subjects {
		isPrivileged := sub != "u:3" && sub != "developer"
		for _, obj := range objects {
			got := e.Enforce(sub, obj, "read")
			if got != isPrivileged {
				t.Errorf("Enforce(%q, %q) = %v, want %v", sub, obj, got, isPrivileged)
			}
		}
	}
}

// 普通用户不能因为内置策略而获得任何权限。
func TestBuiltinPoliciesDoNotLeakToDevelopers(t *testing.T) {
	e, err := NewEnforcer()
	if err != nil {
		t.Fatalf("new enforcer: %v", err)
	}
	for _, sub := range PrivilegedPolicySubjects {
		e.AddPolicy(sub, BuiltinPolicyObject, "read")
	}
	if e.Enforce("u:3", "ds:1:public:orders", "read") {
		t.Fatal("developer must not be granted by builtin policies")
	}
}
