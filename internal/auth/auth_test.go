package auth

import (
	"strings"
	"testing"
	"time"
)

func TestHashAndVerify(t *testing.T) {
	hash, err := HashPassword("P@ssw0rd!")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if hash == "P@ssw0rd!" {
		t.Fatal("hash must not equal plaintext")
	}
	if !VerifyPassword(hash, "P@ssw0rd!") {
		t.Error("correct password should verify")
	}
	if VerifyPassword(hash, "wrong") {
		t.Error("wrong password must not verify")
	}
}

func TestIssueAndParseToken(t *testing.T) {
	issuer := NewTokenIssuer("test-secret", time.Hour)
	tok, err := issuer.Issue(42, "alice", "dba")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	claims, err := issuer.Parse(tok)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if claims.UserID != 42 {
		t.Errorf("UserID = %d, want 42", claims.UserID)
	}
	if claims.Username != "alice" {
		t.Errorf("Username = %q, want alice", claims.Username)
	}
	if claims.Role != "dba" {
		t.Errorf("Role = %q, want dba", claims.Role)
	}
}

func TestParseToken_RejectsWrongSecret(t *testing.T) {
	tok, err := NewTokenIssuer("secret-a", time.Hour).Issue(1, "bob", "developer")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := NewTokenIssuer("secret-b", time.Hour).Parse(tok); err == nil {
		t.Fatal("token signed with different secret must be rejected")
	}
}

func TestParseToken_RejectsExpired(t *testing.T) {
	issuer := NewTokenIssuer("test-secret", -time.Minute)
	tok, err := issuer.Issue(1, "bob", "developer")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := issuer.Parse(tok); err == nil {
		t.Fatal("expired token must be rejected")
	}
}

func TestParseToken_RejectsGarbage(t *testing.T) {
	issuer := NewTokenIssuer("test-secret", time.Hour)
	if _, err := issuer.Parse("not-a-jwt"); err == nil {
		t.Fatal("garbage token must be rejected")
	}
}

func TestGeneratePassword_LengthAndCharset(t *testing.T) {
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	allowed := map[rune]bool{}
	for _, r := range alphabet {
		allowed[r] = true
	}

	for i := 0; i < 50; i++ {
		pw, err := GeneratePassword(12)
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if len([]rune(pw)) != 12 {
			t.Fatalf("length = %d, want 12 (pw=%q)", len([]rune(pw)), pw)
		}
		for _, r := range pw {
			if !allowed[r] {
				t.Fatalf("password %q contains %q, which is outside the alphabet", pw, r)
			}
		}
	}
}

// 易混字符必须排除：初始密码要靠人工转交给用户，
// 0/O、1/l/I 这几个字一旦看错，用户会坚信「密码不对」。
func TestGeneratePassword_NoAmbiguousChars(t *testing.T) {
	for i := 0; i < 50; i++ {
		pw, err := GeneratePassword(16)
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		for _, bad := range "0O1lI" {
			if strings.ContainsRune(pw, bad) {
				t.Fatalf("password %q contains ambiguous char %q", pw, bad)
			}
		}
	}
}

func TestGeneratePassword_Unique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		pw, err := GeneratePassword(12)
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if seen[pw] {
			t.Fatalf("duplicate password generated: %q", pw)
		}
		seen[pw] = true
	}
}

func TestGeneratePassword_RejectsTinyLength(t *testing.T) {
	if _, err := GeneratePassword(0); err == nil {
		t.Fatal("length 0 must be rejected")
	}
}
