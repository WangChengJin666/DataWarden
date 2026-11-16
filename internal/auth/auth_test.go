package auth

import (
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
