package adapter

import "testing"

// 认证库决定 MongoDB 能否认证成功，且失败时驱动只回一句
// Authentication failed，看不出是「密码错」还是「认证库错」。
// 所以这里逐个覆盖取值规则。
//
// 默认值选 admin 而不是「连接串里的库」：MongoDB 官方工具的行为就是这样
// （驱动 URI 不带 authSource 时用 admin，mongosh 的
// --authenticationDatabase 默认值也是 admin）。连接串路径里的库只决定
// 「连上后默认切到哪个库」，不参与认证。
func TestResolveAuthSource(t *testing.T) {
	cases := []struct {
		name       string
		cfgAuthSrc string
		database   string
		want       string
		why        string
	}{
		{
			name:       "未填时回落 admin",
			cfgAuthSrc: "",
			database:   "project_cooper",
			want:       "admin",
			why:        "与官方工具一致：留空就是 admin，账号建在业务库的才需要填",
		},
		{
			name:       "显式填 admin",
			cfgAuthSrc: "admin",
			database:   "project_cooper",
			want:       "admin",
			why:        "全局管理员账号建在 admin 库",
		},
		{
			name:       "显式填业务库",
			cfgAuthSrc: "project_cooper",
			database:   "project_cooper",
			want:       "project_cooper",
			why:        "账号建在业务库时，认证库必须显式填该库名",
		},
		{
			name:       "显式填其他库",
			cfgAuthSrc: "test",
			database:   "project_cooper",
			want:       "test",
			why:        "认证库与查询库完全解耦，允许任意组合",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveAuthSource(c.cfgAuthSrc, c.database); got != c.want {
				t.Errorf("resolveAuthSource(%q, %q) = %q, want %q (%s)",
					c.cfgAuthSrc, c.database, got, c.want, c.why)
			}
		})
	}
}

// 回归测试：默认库不得影响认证库的取值。
//
// 曾经按「连接串里的库」认证，导致账号建在 admin 的实例一律
// AuthenticationFailed——而 mongosh 用同一套凭据能连上，用户只能怀疑平台。
func TestResolveAuthSource_IgnoresDatabase(t *testing.T) {
	for _, db := range []string{"project_cooper", "shop", "test", ""} {
		if got := resolveAuthSource("", db); got != "admin" {
			t.Errorf("resolveAuthSource(\"\", %q) = %q, want admin", db, got)
		}
	}
}

// 显式指定必须优先于默认值，否则界面上填的认证库等于没用。
func TestResolveAuthSource_ExplicitWins(t *testing.T) {
	if got := resolveAuthSource("shop", "project_cooper"); got != "shop" {
		t.Fatalf("explicit auth_source must win, got %q", got)
	}
}
