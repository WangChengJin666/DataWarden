package authz

import (
	"fmt"
	"sync"
	"testing"
)

// --- NormalizeScope：整库授权失效的根因修复 ---

func TestNormalizeScope(t *testing.T) {
	cases := []struct{ in, want, why string }{
		{"shop", "shop:*", "裸库名必须补成 库:*，否则策略只有两段、永远匹配不上"},
		{"shop:*", "shop:*", "已经是整库形态，幂等"},
		{"shop:orders", "shop:orders", "表级授权不变"},
		{"shop:orders,users", "shop:orders,users", "多表清单不变"},
		{"  shop  ", "shop:*", "两端空白要清掉"},
		{"", "", "空串原样返回，由调用方决定怎么处理"},
	}
	for _, c := range cases {
		if got := NormalizeScope(c.in); got != c.want {
			t.Errorf("NormalizeScope(%q) = %q, want %q (%s)", c.in, got, c.want, c.why)
		}
	}
}

// 幂等：规范化两次结果不变。scopeAllowed 会在比较前再规范一次，
// 不幂等的话「已规范的 scope 被再加工」会出错。
func TestNormalizeScope_Idempotent(t *testing.T) {
	for _, in := range []string{"shop", "shop:*", "shop:orders"} {
		once := NormalizeScope(in)
		twice := NormalizeScope(once)
		if once != twice {
			t.Errorf("NormalizeScope not idempotent for %q: %q -> %q", in, once, twice)
		}
	}
}

// --- C3 核心回归：整库授权必须真的能查 ---

// 这是本次最严重 bug 的回归测试：
//
// 库里存的是裸库名 "shop"（历史数据 / 绕过 handler 直接写入），
// 若 SyncGrants 不做规范化，生成策略 ds:1:shop，
// 而鉴权请求是 ds:1:shop:orders —— keyMatch 不匹配，整库授权静默失效。
func TestSyncGrants_NormalizesBareDatabaseScope(t *testing.T) {
	e, err := NewEnforcer()
	if err != nil {
		t.Fatalf("new enforcer: %v", err)
	}

	// 模拟历史脏数据：scope 只有库名
	if err := e.SyncGrants([]GrantPolicy{
		{UserID: 1, DatasourceID: 2, Scope: "shop", Action: "read"},
	}); err != nil {
		t.Fatalf("sync: %v", err)
	}

	if !e.Enforce("u:1", "ds:2:shop:orders", "read") {
		t.Error("整库授权（裸库名）必须能查库下任意表；" +
			"若失败说明出口没有规范化，策略只有两段匹配不上三段请求")
	}
	if !e.Enforce("u:1", "ds:2:shop:users", "read") {
		t.Error("整库授权必须覆盖同库其他表")
	}
	// 不能越权到别的库
	if e.Enforce("u:1", "ds:2:other:orders", "read") {
		t.Error("整库授权不能跨库生效")
	}
}

// 规范化后仍不能跨数据源：scope 只描述库/表，数据源由 ds:<id> 隔离。
func TestSyncGrants_NormalizedScopeStaysWithinDatasource(t *testing.T) {
	e, _ := NewEnforcer()
	if err := e.SyncGrants([]GrantPolicy{
		{UserID: 5, DatasourceID: 2, Scope: "shop", Action: "read"},
	}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if e.Enforce("u:5", "ds:3:shop:orders", "read") {
		t.Error("授权只对指定数据源生效，不能跨数据源")
	}
}

// --- 并发安全：定时刷新与 Enforce 同时进行不能让进程崩 ---

// 普通 casbin.Enforcer 没有锁。本项目有 60 秒定时任务重建策略
// （先 RemovePolicy 再 AddPolicy），同一时刻 HTTP 请求在调 Enforce。
// 不加锁的话 Go 的 map 并发读写会直接 panic。
//
// 这个测试在非 -race 下也能抓到「策略为空的中间态」：
// 断言读到 false 的次数必须为 0。
func TestEnforcer_ConcurrentSyncAndEnforce(t *testing.T) {
	e, err := NewEnforcer()
	if err != nil {
		t.Fatalf("new enforcer: %v", err)
	}
	grants := []GrantPolicy{
		{UserID: 1, DatasourceID: 1, Scope: "shop", Action: "read"},
	}

	// 先同步一次建立初始状态
	if err := e.SyncGrants(grants); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	denied := 0
	const readers = 8
	const rounds = 200

	// 写：持续重建策略
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			if err := e.SyncGrants(grants); err != nil {
				t.Errorf("sync: %v", err)
				return
			}
		}
	}()

	// 读：持续判定
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				if !e.Enforce("u:1", "ds:1:shop:orders", "read") {
					mu.Lock()
					denied++
					mu.Unlock()
				}
			}
		}()
	}

	wg.Wait()

	// 策略在整个过程中应当是稳定的：同一份授权反复重建，
	// 任何一次读到 false 都说明某个请求看到了「策略被清空」的中间态。
	if denied != 0 {
		t.Errorf("并发重建期间出现 %d 次误拒绝："+
			"说明 Enforce 与 SyncGrants 没有互斥，读到了被清空的中间态", denied)
	}
}

// 角色同步与授权同步并发时也不能崩。
func TestEnforcer_ConcurrentSyncRolesAndGrants(t *testing.T) {
	e, err := NewEnforcer()
	if err != nil {
		t.Fatalf("new enforcer: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = e.SyncRoles(map[int64]string{1: "dba"})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = e.SyncGrants([]GrantPolicy{
				{UserID: 1, DatasourceID: 1, Scope: "shop", Action: "read"},
			})
		}
	}()
	// 同时读角色绑定
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = e.Enforce("u:1", "ds:*", "read")
		}
	}()
	wg.Wait()
}

// 内置角色策略被 SyncGrants 清空后必须能重新加回来（reAddBuiltinPolicies 的前提）：
// SyncGrants 只清 p 段，所以 reAddBuiltinPolicies 必须每次都被调用。
func TestSyncGrants_ClearsBuiltinPolicies(t *testing.T) {
	e, _ := NewEnforcer()
	e.AddPolicy("dba", "ds:*", "read")

	if !e.Enforce("dba", "ds:1:shop:orders", "read") {
		t.Fatal("builtin policy should work before sync")
	}

	// SyncGrants 会清空整个 p 段——这是已知行为，
	// 所以调用方必须在之后补 reAddBuiltinPolicies。
	if err := e.SyncGrants(nil); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if e.Enforce("dba", "ds:1:shop:orders", "read") {
		t.Fatal("SyncGrants 应当清空 p 段（含内置策略）；" +
			"若这里仍能通过，说明清理逻辑改了，reAddBuiltinPolicies 的必要性需重新评估")
	}

	e.AddPolicy("dba", "ds:*", "read")
	if !e.Enforce("dba", "ds:1:shop:orders", "read") {
		t.Error("重新添加内置策略后必须恢复")
	}
}

// 空授权列表是合法输入（没人有授权），不应报错。
func TestSyncGrants_EmptyIsValid(t *testing.T) {
	e, _ := NewEnforcer()
	if err := e.SyncGrants(nil); err != nil {
		t.Fatalf("empty grants should not error: %v", err)
	}
	if err := e.SyncGrants([]GrantPolicy{}); err != nil {
		t.Fatalf("empty slice should not error: %v", err)
	}
}

// 同一用户在同一数据源上的多条授权应当全部生效。
func TestSyncGrants_MultipleScopesSameDatasource(t *testing.T) {
	e, _ := NewEnforcer()
	if err := e.SyncGrants([]GrantPolicy{
		{UserID: 1, DatasourceID: 1, Scope: "shop:orders", Action: "read"},
		{UserID: 1, DatasourceID: 1, Scope: "shop:users", Action: "read"},
		{UserID: 1, DatasourceID: 1, Scope: "pay", Action: "read"}, // 裸库名，需规范化
	}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	for _, obj := range []string{"ds:1:shop:orders", "ds:1:shop:users", "ds:1:pay:trades"} {
		if !e.Enforce("u:1", obj, "read") {
			t.Errorf("%s should be allowed", obj)
		}
	}
	// 未授权的表仍拒绝
	if e.Enforce("u:1", "ds:1:shop:payments", "read") {
		t.Error("未在 scope 里的表必须拒绝")
	}
}

// --- 权限模型的边界 ---

// keyMatch 的 * 不能跨段：授权 ds:1:shop 不得匹配 ds:1:other:orders。
// 这条是 modelText 用 keyMatch 而非 keyMatch2 的理由，必须有测试守住。
func TestEnforce_WildcardDoesNotCrossSegments(t *testing.T) {
	e, _ := NewEnforcer()
	e.AddPolicy("u:1", "ds:1:shop", "read")

	if e.Enforce("u:1", "ds:1:shop:orders", "read") {
		t.Error("两段式策略不应匹配三段对象（这正是整库授权失效的表现）")
	}
	if e.Enforce("u:1", "ds:1:other:orders", "read") {
		t.Error("不能跨库命中")
	}
	if e.Enforce("u:1", "ds:11:shop", "read") {
		t.Error("数据源 ID 前缀不能被误当匹配")
	}
}

func TestEnforce_DatabaseWildcard(t *testing.T) {
	e, _ := NewEnforcer()
	e.AddPolicy("u:1", "ds:1:shop:*", "read")

	if !e.Enforce("u:1", "ds:1:shop:orders", "read") {
		t.Error("库级通配应覆盖该库所有表")
	}
	if e.Enforce("u:1", "ds:1:other:orders", "read") {
		t.Error("库级通配不能跨库")
	}
}

// 失败关闭：Enforce 内部出错时返回 false，不放大权限。
//
// 只断言对外行为：参数不合法（这里用空 subject）时必须返回 false 而不是 panic
// 或放行。内部 casbin 的报错形态不作为契约，避免随版本变化让测试失效。
func TestEnforce_FailClosed(t *testing.T) {
	e, _ := NewEnforcer()
	e.AddPolicy("u:1", "ds:1:shop:orders", "read")

	// 合法请求应当通过，确认测试前提成立
	if !e.Enforce("u:1", "ds:1:shop:orders", "read") {
		t.Fatal("测试前提不成立：合法请求未通过")
	}
	// 空 subject 不匹配任何策略，必须拒绝
	if e.Enforce("", "ds:1:shop:orders", "read") {
		t.Error("空 subject 必须拒绝")
	}
	// 空对象同理
	if e.Enforce("u:1", "", "read") {
		t.Error("空 object 必须拒绝")
	}
}

func ExampleNormalizeScope() {
	fmt.Println(NormalizeScope("shop"))
	fmt.Println(NormalizeScope("shop:orders"))
	// Output:
	// shop:*
	// shop:orders
}

// 畸形 scope 必须被拒绝，不能「尽力修复」。
//
// 这些输入若被放行去拼策略，会产生永远匹配不上、或语义重复的授权：
//   ":orders"  → ds:1::orders（库名为空，四段里有一段是空）
//   "a:b:c"    → ds:1:a:b:c（五段，永远不匹配四段对象）
//   "**"/"*:*" → 整源授权的另一种写法，与 ScopeAllDatabases 重复
//   "shop:ord*"→ 通配符进入策略，会意外匹配 orders2
//
// 注意 "*" 不在此列：它现在是「整个数据源」的正规写法，
// 由 TestNormalizeScope_AllDatabases 单独覆盖。
func TestNormalizeScope_RejectsMalformed(t *testing.T) {
	bad := []struct{ in, why string }{
		{"", "空串"},
		{"   ", "纯空白"},
		{":orders", "库名为空"},
		{":", "只有冒号"},
		{"**", "整段通配只认 * 一种写法"},
		{"*:*", "整源授权只认 * 一种写法"},
		{"a:b:c", "多余的冒号，拼出来是五段"},
		{"shop:ord*", "表名含通配符"},
		{"shop:orders?", "表名含 ?"},
		{"shop:[abc]", "表名含字符集"},
		{".shop", "库名以点开头会被拆库误判"},
		{"shop.", "库名以点结尾"},
		{"sh op", "库名含空格"},
	}
	for _, c := range bad {
		if got := NormalizeScope(c.in); got != "" {
			t.Errorf("NormalizeScope(%q) = %q, want \"\" (%s)", c.in, got, c.why)
		}
	}
}

// ScopeAllDatabases 必须归一成两段 "*:*"，并且真的覆盖该数据源下全部库表。
//
// 这是「申请不再选库」的核心前提：申请落库的 scope 就是它。
// 若它归一失败，所有新申请审批通过后都是一条空授权（查不了任何东西，
// 且界面显示已授权）；若它拼成 ds:<id>:* 少一段，同样永远匹配不上。
func TestNormalizeScope_AllDatabases(t *testing.T) {
	// 先钉死常量本身的值：下游（前端 isAllDatabasesScope、迁移脚本、
	// 审计明细）都按字面量 "*" / "*:*" 判断，常量一改就会全线错位。
	// 用字面量断言而不是常量比较，否则常量被改错时测试会跟着错。
	if ScopeAllDatabases != "*" {
		t.Fatalf("ScopeAllDatabases = %q, want \"*\"", ScopeAllDatabases)
	}
	if ScopeDatabaseAll != "*:*" {
		t.Fatalf("ScopeDatabaseAll = %q, want \"*:*\"", ScopeDatabaseAll)
	}
	if got := NormalizeScope(ScopeAllDatabases); got != ScopeDatabaseAll {
		t.Fatalf("NormalizeScope(%q) = %q, want %q",
			ScopeAllDatabases, got, ScopeDatabaseAll)
	}

	e, err := NewEnforcer()
	if err != nil {
		t.Fatalf("new enforcer: %v", err)
	}
	// 模拟审批通过后的落库值 → 重建策略
	if err := e.SyncGrants([]GrantPolicy{
		{UserID: 1, DatasourceID: 1, Scope: ScopeAllDatabases, Action: "read"},
	}); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// 覆盖三种数据源的对象形态：SQL 的库/表、Redis 的数字 db、Mongo 的集合
	allow := []string{
		"ds:1:shop:orders",
		"ds:1:shop:users",
		"ds:1:payment:trades",   // 换个库也要放行
		"ds:1:37:user:1001:name", // Redis：db 号 + 多段 key
		"ds:1:project_cooper:users",
	}
	for _, obj := range allow {
		if !e.Enforce("u:1", obj, "read") {
			t.Errorf("%s 应当放行（整数据源授权）", obj)
		}
	}

	// 不能越出本数据源
	for _, obj := range []string{
		"ds:2:shop:orders", // 别的数据源
		"ds:11:shop:orders",
	} {
		if e.Enforce("u:1", obj, "read") {
			t.Errorf("%s 不应放行（授权只覆盖数据源 1）", obj)
		}
	}
	// 写操作仍然拒绝
	if e.Enforce("u:1", "ds:1:shop:orders", "write") {
		t.Error("整数据源授权只应包含 read")
	}
	// 其他用户不受影响
	if e.Enforce("u:2", "ds:1:shop:orders", "read") {
		t.Error("授权不能外溢到其他用户")
	}
}

// 合法输入仍要正确归一，不能因为收紧校验误伤。
func TestNormalizeScope_StillAcceptsValid(t *testing.T) {
	good := []struct{ in, want string }{
		{"shop", "shop:*"},
		{"shop:*", "shop:*"},
		{"shop:orders", "shop:orders"},
		{"shop:orders,users", "shop:orders,users"},
		{"  shop  ", "shop:*"},
		{"shop:", "shop:*"},           // 尾冒号按整库
		{"my.data:events", "my.data:events"}, // 库名含点是合法的，表名照常保留
		{"shop:order_items", "shop:order_items"},
	}
	for _, c := range good {
		if got := NormalizeScope(c.in); got != c.want {
			t.Errorf("NormalizeScope(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// 非法 scope 在 SyncGrants 里必须被跳过，绝不能拼进策略。
//
// 这条防的是「一条脏数据让某人拿到全数据源权限」或
// 「生成一条永远无效、却显示为已授权的策略」。
func TestSyncGrants_SkipsMalformedScope(t *testing.T) {
	e, err := NewEnforcer()
	if err != nil {
		t.Fatalf("new enforcer: %v", err)
	}
	if err := e.SyncGrants([]GrantPolicy{
		{UserID: 1, DatasourceID: 1, Scope: "**", Action: "read"},     // 畸形
		{UserID: 2, DatasourceID: 1, Scope: ":orders", Action: "read"}, // 畸形
		{UserID: 3, DatasourceID: 1, Scope: "shop", Action: "read"},    // 合法
	}); err != nil {
		t.Fatalf("sync 不应因脏数据失败: %v", err)
	}

	// u:1 的畸形 scope 必须没有生成任何可用策略
	if e.Enforce("u:1", "ds:1:other:orders", "read") {
		t.Error("畸形 scope 绝不能扩大成大范围授权")
	}
	if e.Enforce("u:1", "ds:1:shop:orders", "read") {
		t.Error("畸形 scope 应被完全跳过")
	}
	// u:2 的畸形 scope 同样无效
	if e.Enforce("u:2", "ds:1::orders", "read") {
		t.Error("空库名的畸形 scope 不应生成策略")
	}
	// u:3 的合法授权必须仍然生效（跳过脏数据不能影响正常数据）
	if !e.Enforce("u:3", "ds:1:shop:orders", "read") {
		t.Error("合法授权应仍然生效")
	}
}
