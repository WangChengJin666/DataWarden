package adapter

import (
	"strings"
	"testing"
)

// MySQL 的 DSN 是 user:pass@tcp(host:port)/db，密码里出现 @ 或 /
// 会让解析错位，最终报错含糊（"invalid DSN" 或连到错误主机）。
// 提前明确报错，比让用户对着「连接失败」猜半天更好。
func TestNewMySQL_RejectsAmbiguousPassword(t *testing.T) {
	for _, pw := range []string{"pa@ss", "pa/ss"} {
		_, err := newMySQL(ConnConfig{
			Type: "mysql", Host: "127.0.0.1", Port: 3306,
			Database: "shop", User: "ro", Password: pw,
		})
		if err == nil {
			t.Errorf("password %q should be rejected", pw)
			continue
		}
		if !strings.Contains(err.Error(), "只读账号") {
			t.Errorf("error should suggest a dedicated account, got: %v", err)
		}
	}
}

// tls 参数只接受 true/false，其他值必须报错而不是静默忽略。
func TestNewMySQL_TLSParameter(t *testing.T) {
	ad, err := newMySQL(ConnConfig{
		Type: "mysql", Host: "127.0.0.1", Port: 3306,
		Database: "shop", User: "ro", Password: "pw",
		Options: map[string]string{"tls": "true"},
	})
	if err != nil {
		t.Fatalf("tls=true should be accepted: %v", err)
	}
	_ = ad.Close()

	if _, err := newMySQL(ConnConfig{
		Type: "mysql", Host: "127.0.0.1", Port: 3306,
		Database: "shop", User: "ro", Password: "pw",
		Options: map[string]string{"tls": "maybe"},
	}); err == nil {
		t.Fatal("tls=maybe must be rejected")
	}
}
