package secret

import "testing"

func TestEnvResolver_ExplicitVar(t *testing.T) {
	t.Setenv("MY_SECRET", "s3cret")
	r := NewEnvResolver()
	got, err := r.Password("env:MY_SECRET")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "s3cret" {
		t.Errorf("got %q, want s3cret", got)
	}
}

func TestEnvResolver_DefaultVar(t *testing.T) {
	t.Setenv("QUERY_DB_PASSWORD", "default-pw")
	r := NewEnvResolver()
	got, err := r.Password("env")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "default-pw" {
		t.Errorf("got %q", got)
	}
}

func TestEnvResolver_EmptyRefReturnsEmpty(t *testing.T) {
	r := NewEnvResolver()
	got, err := r.Password("")
	if err != nil {
		t.Fatalf("empty ref should not error: %v", err)
	}
	if got != "" {
		t.Errorf("empty ref should return empty password, got %q", got)
	}
}

func TestEnvResolver_MissingVarReturnsEmpty(t *testing.T) {
	r := NewEnvResolver()
	got, err := r.Password("env:DEFINITELY_NOT_SET_XYZ")
	if err != nil {
		t.Fatalf("missing var should not error: %v", err)
	}
	if got != "" {
		t.Errorf("missing var should return empty, got %q", got)
	}
}

func TestEnvResolver_RejectsUnknownScheme(t *testing.T) {
	r := NewEnvResolver()
	if _, err := r.Password("vault:secret/data/foo"); err == nil {
		t.Fatal("unsupported scheme must return an error, not silently return empty")
	}
}

func TestEnvResolver_EmptyVarName(t *testing.T) {
	r := NewEnvResolver()
	if _, err := r.Password("env:"); err == nil {
		t.Fatal("empty var name must be rejected")
	}
}

func TestEnvResolver_User(t *testing.T) {
	t.Setenv("QUERY_DB_USER", "readonly")
	r := NewEnvResolver()
	if got := r.User(); got != "readonly" {
		t.Errorf("User() = %q", got)
	}
}

func TestEnvResolver_TrimsWhitespace(t *testing.T) {
	t.Setenv("PAD_VAR", "pw")
	r := NewEnvResolver()
	got, err := r.Password("  env:PAD_VAR  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "pw" {
		t.Errorf("got %q", got)
	}
}
