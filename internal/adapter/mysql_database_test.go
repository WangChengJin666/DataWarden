package adapter

import (
	"context"
	"strings"
	"testing"
)

// USE 不能参数化，库名只能拼进 SQL，所以必须靠白名单挡住注入。
//
// `shop; DROP DATABASE x` 这种名字一旦拼进去就是第二条语句；
// 反引号则能提前闭合 `USE \`...\`` 的引号边界，同样是注入。
func TestIsSafeIdentifier(t *testing.T) {
	ok := []string{"shop", "pay_2024", "user-center", "a", "db$1"}
	bad := []string{
		"", "shop;drop database x", "shop`", "shop order", "shop\n",
		"a.b", "库名", strings.Repeat("x", 65), "shop/**/x",
	}
	for _, s := range ok {
		if !isSafeIdentifier(s) {
			t.Errorf("%q should be accepted", s)
		}
	}
	for _, s := range bad {
		if isSafeIdentifier(s) {
			t.Errorf("%q must be rejected", s)
		}
	}
}

// 库名非法时必须在建连接之前就拒绝，不能带着可疑字符串去连库。
func TestQueryInDatabase_RejectsUnsafeDatabase(t *testing.T) {
	ad, err := newMySQL(ConnConfig{
		Type: "mysql", Host: "127.0.0.1", Port: 3306,
		User: "ro", Password: "pw",
	})
	if err != nil {
		t.Fatalf("newMySQL: %v", err)
	}
	defer ad.Close()

	_, err = ad.QueryInDatabase(context.Background(), "shop;drop database x", "SELECT 1", 10)
	if err == nil {
		t.Fatal("unsafe database name must be rejected")
	}
}

// 按请求库建出的连接配置必须把字符集放在 charsets 里，而不是 Params。
//
// 回归 Error 1193 (HY000): Unknown system variable 'charset'：
// Params 会被驱动拼成 `SET charset = utf8mb4` 在握手后下发，
// 而 charset 不是系统变量，连接直接建不起来。
func TestConnConfig_CharsetIsNotSentAsSystemVariable(t *testing.T) {
	ad, err := newMySQL(ConnConfig{
		Type: "mysql", Host: "127.0.0.1", Port: 3306,
		User: "ro", Password: "pw",
	})
	if err != nil {
		t.Fatalf("newMySQL: %v", err)
	}
	defer ad.Close()

	cfg, err := ad.connConfig("shop")
	if err != nil {
		t.Fatalf("connConfig: %v", err)
	}

	if _, ok := cfg.Params["charset"]; ok {
		t.Errorf("charset must not go into Params (it is not a system variable), got %v", cfg.Params)
	}
	if got := cfg.DBName; got != "shop" {
		t.Errorf("DBName = %q, want shop", got)
	}

	// FormatDSN 会带上驱动自己的 charset 参数——这是 DSN 解析器能识别的写法，
	// 说明 Charset 选项确实生效了（否则 set names 根本不会被下发）。
	if dsn := cfg.FormatDSN(); !strings.Contains(dsn, "charset=utf8mb4") {
		t.Errorf("connector config should carry charset, got DSN: %s", dsn)
	}
}

// tls=true 必须原样带到专用连接的配置里，否则这条路径比主连接弱。
func TestConnConfig_CarriesTLS(t *testing.T) {
	ad, err := newMySQL(ConnConfig{
		Type: "mysql", Host: "127.0.0.1", Port: 3306,
		User: "ro", Password: "pw",
		Options: map[string]string{"tls": "true"},
	})
	if err != nil {
		t.Fatalf("newMySQL: %v", err)
	}
	defer ad.Close()

	cfg, err := ad.connConfig("shop")
	if err != nil {
		t.Fatalf("connConfig: %v", err)
	}
	if cfg.TLSConfig != "true" {
		t.Errorf("TLSConfig = %q, want true", cfg.TLSConfig)
	}
}

// 空库名走普通 Query：让 MySQL 自己报 1046，而不是被 adapter 悄悄换库。
func TestQueryInDatabase_EmptyFallsBackToDefault(t *testing.T) {
	ad, err := newMySQL(ConnConfig{
		Type: "mysql", Host: "127.0.0.1", Port: 3306,
		User: "ro", Password: "pw",
	})
	if err != nil {
		t.Fatalf("newMySQL: %v", err)
	}
	defer ad.Close()

	// 连不上是预期的（本地没有 MySQL），关键是错误必须来自连接阶段，
	// 而不是 "invalid database name" 这类入参校验。
	_, err = ad.QueryInDatabase(context.Background(), "", "SELECT 1", 10)
	if err == nil {
		t.Skip("unexpected success: a local mysql seems reachable")
	}
	if msg := err.Error(); strings.Contains(msg, "invalid database name") {
		t.Errorf("empty db should not be treated as invalid, got: %v", err)
	}
}
