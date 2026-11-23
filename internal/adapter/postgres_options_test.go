package adapter

import (
	"strings"
	"testing"
)

// DSN 拼接必须处理引号与反斜杠，否则密码里带空格 / 单引号时
// libpq 解析会失败，报错却是含糊的「invalid connection string」。
func TestQuoteDSNValue(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "''"},
		{"simple", "simple"},
		{"with space", "'with space'"},
		{"it's", `'it\'s'`},
		{`back\slash`, `'back\\slash'`},
		{`a'b\c d`, `'a\'b\\c d'`},
	}
	for _, c := range cases {
		if got := quoteDSNValue(c.in); got != c.want {
			t.Errorf("quoteDSNValue(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// 非法 sslmode 必须报错并点出取值问题：
// 静默当作 disable 会让「以为开了 SSL」的实例一直明文传输。
func TestNewPostgres_RejectsInvalidSSLMode(t *testing.T) {
	_, err := newPostgres(ConnConfig{
		Type:     "postgresql",
		Host:     "127.0.0.1",
		Port:     5432,
		Database: "shop",
		Options:  map[string]string{"sslmode": "yes"},
	})
	if err == nil {
		t.Fatal("invalid sslmode must be rejected")
	}
	if !strings.Contains(err.Error(), "sslmode") {
		t.Errorf("error should name sslmode, got: %v", err)
	}
}

// 各种合法的 libpq sslmode 都要接受。
func TestNewPostgres_AcceptsValidSSLMode(t *testing.T) {
	for _, m := range []string{"disable", "allow", "prefer", "require", "verify-ca", "verify-full"} {
		ad, err := newPostgres(ConnConfig{
			Type:     "postgresql",
			Host:     "127.0.0.1",
			Port:     5432,
			Database: "shop",
			Options:  map[string]string{"sslmode": m},
		})
		if err != nil {
			t.Errorf("sslmode=%s should be accepted, got %v", m, err)
			continue
		}
		// sql.Open 是懒连接，这里不会真的建连；关掉避免泄漏
		_ = ad.Close()
	}
}
