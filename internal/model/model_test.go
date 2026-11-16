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
		SecretRef: "s", Env: "prod", CreatedAt: time.Now(),
	}
	b, _ := json.Marshal(ds)
	out := string(b)

	for _, want := range []string{
		`"id":`, `"name":`, `"type":`, `"host":`, `"port":`,
		`"database":`, `"secret_ref":`, `"env":`, `"created_at":`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing json key %s in %s", want, out)
		}
	}
	// Go 默认大写字段名不应出现
	for _, bad := range []string{`"ID":`, `"Name":`, `"SecretRef":`} {
		if strings.Contains(out, bad) {
			t.Errorf("unexpected Go-style key %s in %s", bad, out)
		}
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
