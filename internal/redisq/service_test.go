package redisq

import (
	"context"
	"testing"

	"github.com/opensourceprj/datawarden/internal/testutil"
)

func TestParseDBIndex(t *testing.T) {
	cases := []struct {
		in   string
		want int
		err  bool
	}{
		{"", 0, false},
		{"37", 37, false},
		{"0", 0, false},
		{"host:6379/3", 3, false},
		{"  5  ", 5, false},
		{"abc", 0, true},
		{"99", 99, false},
		{"37", 37, false},
		{"-1", 0, true},
	}
	for _, c := range cases {
		got, err := ParseDBIndex(c.in)
		if c.err {
			if err == nil {
				t.Errorf("ParseDBIndex(%q) should error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseDBIndex(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseDBIndex(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func setupService(t *testing.T, maxKeys int) *QueryService {
	t.Helper()
	addr := testutil.RedisAddr(t)
	testutil.CleanTestKeys(t, addr)
	t.Cleanup(func() { testutil.CleanTestKeys(t, addr) })

	c, err := NewClient(addr, "", testutil.RedisPassword(), testutil.RedisDB(t), maxKeys)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return NewQueryService(c, maxKeys)
}

func TestQueryService_ExactKey(t *testing.T) {
	svc := setupService(t, 50)
	ctx := context.Background()

	key := testutil.TestKeyPrefix + "user:1:name"
	if err := svc.Client.SetRaw(ctx, key, "alice", 0); err != nil {
		t.Fatalf("seed: %v", err)
	}

	res, err := svc.Query(ctx, QueryRequest{Key: key})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !res.Exact {
		t.Error("Exact should be true")
	}
	if len(res.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(res.Items))
	}
	if res.Items[0].Value != "alice" {
		t.Errorf("value = %q", res.Items[0].Value)
	}
}

func TestQueryService_PatternScan(t *testing.T) {
	svc := setupService(t, 50)
	ctx := context.Background()

	for _, k := range []string{
		testutil.TestKeyPrefix + "order:1:a",
		testutil.TestKeyPrefix + "order:1:b",
		testutil.TestKeyPrefix + "user:9:z",
	} {
		if err := svc.Client.SetRaw(ctx, k, "v", 0); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	res, err := svc.Query(ctx, QueryRequest{Pattern: testutil.TestKeyPrefix + "order:1:*"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if res.Exact {
		t.Error("Exact should be false for pattern query")
	}
	if len(res.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(res.Items))
	}
}

func TestQueryService_RejectsBothKeyAndPattern(t *testing.T) {
	svc := setupService(t, 50)
	_, err := svc.Query(context.Background(), QueryRequest{Key: "a", Pattern: "b:*"})
	if err == nil {
		t.Fatal("must reject when both key and pattern are set")
	}
}

func TestQueryService_RejectsNeither(t *testing.T) {
	svc := setupService(t, 50)
	_, err := svc.Query(context.Background(), QueryRequest{})
	if err == nil {
		t.Fatal("must reject when neither key nor pattern is set")
	}
}

func TestQueryService_RejectsWildcardInExactKey(t *testing.T) {
	svc := setupService(t, 50)
	_, err := svc.Query(context.Background(), QueryRequest{Key: "user:*"})
	if err == nil {
		t.Fatal("exact query must reject wildcards")
	}
}

func TestQueryService_RejectsBroadPattern(t *testing.T) {
	svc := setupService(t, 50)
	for _, p := range []string{"*", "a:*", ""} {
		if _, err := svc.Query(context.Background(), QueryRequest{Pattern: p}); err == nil {
			t.Errorf("pattern %q must be rejected", p)
		}
	}
}

func TestQueryService_RespectsRowLimit(t *testing.T) {
	svc := setupService(t, 50)
	ctx := context.Background()

	for _, k := range []string{
		testutil.TestKeyPrefix + "lim:1",
		testutil.TestKeyPrefix + "lim:2",
		testutil.TestKeyPrefix + "lim:3",
		testutil.TestKeyPrefix + "lim:4",
	} {
		if err := svc.Client.SetRaw(ctx, k, "v", 0); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	res, err := svc.Query(ctx, QueryRequest{
		Pattern:  testutil.TestKeyPrefix + "lim:*",
		RowLimit: 2,
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(res.Items) != 2 {
		t.Errorf("expected 2 items with RowLimit=2, got %d", len(res.Items))
	}
	if !res.Truncated {
		t.Error("Truncated should be true when limit cuts results")
	}
}

func TestQueryService_ExactMissingKey(t *testing.T) {
	svc := setupService(t, 50)
	res, err := svc.Query(context.Background(), QueryRequest{
		Key: testutil.TestKeyPrefix + "nope",
	})
	if err != nil {
		t.Fatalf("query missing key should not error: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].Type != "none" {
		t.Errorf("expected single item with type none, got %+v", res.Items)
	}
}
