package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestUserJSON_DoesNotLeakPasswordHash 保证密码哈希永远不会出现在 API 响应里。
func TestUserJSON_DoesNotLeakPasswordHash(t *testing.T) {
	u := User{
		ID:           1,
		Username:     "alice",
		Email:        "a@b.c",
		PasswordHash: "$2a$10$supersecrethash",
		Role:         RoleDBA,
		Status:       "active",
		CreatedAt:    time.Now(),
	}
	b, err := json.Marshal(u)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(b)

	if strings.Contains(out, "supersecrethash") {
		t.Fatal("password hash must never be serialized")
	}
	if strings.Contains(out, "PasswordHash") {
		t.Fatal("PasswordHash field must not appear in JSON")
	}
	if !strings.Contains(out, `"username":"alice"`) {
		t.Errorf("expected snake_case username field, got %s", out)
	}
}

func TestDatasourceJSON_UsesSnakeCase(t *testing.T) {
	ds := Datasource{
		ID: 1, Name: "shop", Type: TypePostgreSQL,
		Host: "h", Port: 5432, Database: "db",
		Username: "readonly", CreatedAt: time.Now(),
	}
	b, _ := json.Marshal(ds)
	out := string(b)

	for _, want := range []string{
		`"id":`, `"name":`, `"type":`, `"host":`, `"port":`,
		`"database":`, `"username":`, `"created_at":`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing json key %s in %s", want, out)
		}
	}
	// Go 默认大写字段名不应出现
	for _, bad := range []string{`"ID":`, `"Name":`, `"Username":`} {
		if strings.Contains(out, bad) {
			t.Errorf("unexpected Go-style key %s in %s", bad, out)
		}
	}
}

// 数据源的密码绝不能出现在 API 响应里。
// 密码虽然改成明文落库，但对外暴露的边界没有变：任何接口都不返回密码。
func TestDatasourceJSON_NeverLeaksPassword(t *testing.T) {
	ds := Datasource{
		ID: 1, Name: "shop", Type: TypePostgreSQL,
		Host: "h", Port: 5432, Database: "db",
		Username:    "readonly",
		Password:    "plain-secret-value",
		HasPassword: true,
	}
	b, err := json.Marshal(ds)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(b)

	if strings.Contains(out, "plain-secret-value") {
		t.Fatal("plaintext password must never be serialized")
	}
	// 字段名也不能出现，否则前端可能误以为能读到密码
	if strings.Contains(out, `"password":`) {
		t.Fatal("password key must not appear in JSON")
	}
	// 精确匹配字段名。"password" 作为子串会误命中合法的 has_password，不能这么查。
	for _, bad := range []string{`"Password":`, `"password":`, `"password_enc":`, `"PasswordEnc":`} {
		if strings.Contains(out, bad) {
			t.Errorf("password field %s must not appear in JSON: %s", bad, out)
		}
	}
	// 但要能告诉前端「已设置密码」
	if !strings.Contains(out, `"has_password":true`) {
		t.Errorf("has_password flag should be exposed, got %s", out)
	}
}

func TestGrantJSON_OmitsNilRevokedAt(t *testing.T) {
	g := Grant{
		ID: 1, UserID: 2, DatasourceID: 3,
		Scope: "db:*", Action: "read",
		ExpiresAt: time.Now(), CreatedAt: time.Now(),
	}
	b, _ := json.Marshal(g)
	if strings.Contains(string(b), "revoked_at") {
		t.Errorf("nil revoked_at should be omitted, got %s", string(b))
	}
}

// WithPlainPassword 只改副本，不改动调用方手里的原对象。
func TestDatasourceWithPlainPassword_DoesNotMutateOriginal(t *testing.T) {
	ds := Datasource{ID: 1, Password: "s3cr3t-prod-pw"}
	out := ds.WithPlainPassword()
	if ds.Password != "s3cr3t-prod-pw" {
		t.Fatalf("original password mutated: %q", ds.Password)
	}
	if ds.PlainPassword != nil {
		t.Fatal("original must not gain PlainPassword")
	}
	if out.PlainPassword == nil || *out.PlainPassword != "s3cr3t-prod-pw" {
		t.Fatalf("expected plaintext echo, got %v", out.PlainPassword)
	}
}

// 默认序列化绝不能带出密码：不调用 WithPlainPassword 就没有 password 这个 key。
func TestDatasourceJSON_NoPasswordUnlessEchoed(t *testing.T) {
	b, err := json.Marshal(Datasource{ID: 1, Password: "s3cr3t-prod-pw", HasPassword: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "s3cr3t-prod-pw") {
		t.Fatalf("plaintext leaked without explicit echo: %s", b)
	}
	if strings.Contains(string(b), `"password"`) {
		t.Fatalf("password key must be absent by default: %s", b)
	}

	// 显式回显时才出现，且就是明文
	b, err = json.Marshal(Datasource{ID: 1, Password: "s3cr3t-prod-pw"}.WithPlainPassword())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"password":"s3cr3t-prod-pw"`) {
		t.Fatalf("expected plaintext password field, got: %s", b)
	}
}

// 没有密码的数据源不该凭空多出 password 字段（否则前端会拿到空串误判为「已配置」）。
func TestDatasourceWithPlainPassword_NoPasswordNoField(t *testing.T) {
	b, err := json.Marshal(Datasource{ID: 1}.WithPlainPassword())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), `"password"`) {
		t.Fatalf("unexpected password field for empty password: %s", b)
	}
}
