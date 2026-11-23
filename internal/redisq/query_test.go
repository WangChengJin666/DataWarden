package redisq

import (
	"context"
	"testing"

	"github.com/opensourceprj/datawarden/internal/testutil"
)

func setupClient(t *testing.T) *Client {
	t.Helper()
	addr := testutil.RedisAddr(t)
	testutil.CleanTestKeys(t, addr)
	t.Cleanup(func() { testutil.CleanTestKeys(t, addr) })

	c, err := NewClient(addr, "", testutil.RedisPassword(), testutil.RedisDB(t), 200)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestFetchExactKey_StringValue(t *testing.T) {
	c := setupClient(t)
	ctx := context.Background()
	key := testutil.TestKeyPrefix + "user:1001:name"
	if err := c.SetRaw(ctx, key, "alice", 0); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err := c.Fetch(ctx, key)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got.Type != "string" {
		t.Errorf("Type = %q, want string", got.Type)
	}
	if got.Value != "alice" {
		t.Errorf("Value = %q, want alice", got.Value)
	}
}

func TestFetchExactKey_HashValue(t *testing.T) {
	c := setupClient(t)
	ctx := context.Background()
	key := testutil.TestKeyPrefix + "user:1001:profile"
	if err := c.HSetRaw(ctx, key, map[string]string{"name": "alice", "city": "SH"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err := c.Fetch(ctx, key)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got.Type != "hash" {
		t.Errorf("Type = %q, want hash", got.Type)
	}
	if len(got.Fields) != 2 {
		t.Errorf("expected 2 fields, got %d", len(got.Fields))
	}
	if got.Fields["name"] != "alice" {
		t.Errorf("name = %q, want alice", got.Fields["name"])
	}
}

func TestFetchExactKey_ReturnsTTL(t *testing.T) {
	c := setupClient(t)
	ctx := context.Background()
	key := testutil.TestKeyPrefix + "session:abc"
	if err := c.SetRaw(ctx, key, "x", 3600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err := c.Fetch(ctx, key)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got.TTL <= 0 || got.TTL > 3600 {
		t.Errorf("TTL = %d, want in (0, 3600]", got.TTL)
	}
}

func TestFetchExactKey_MissingKey(t *testing.T) {
	c := setupClient(t)
	got, err := c.Fetch(context.Background(), testutil.TestKeyPrefix+"nope")
	if err != nil {
		t.Fatalf("fetch missing key should not error: %v", err)
	}
	if got.Type != "none" {
		t.Errorf("Type = %q, want none", got.Type)
	}
}

func TestFetch_RejectsPattern(t *testing.T) {
	c := setupClient(t)
	if _, err := c.Fetch(context.Background(), "user:*"); err == nil {
		t.Fatal("Fetch must reject wildcard pattern")
	}
}

func TestScan_ReturnsMatchingKeys(t *testing.T) {
	c := setupClient(t)
	ctx := context.Background()
	for _, k := range []string{
		testutil.TestKeyPrefix + "order:1:a",
		testutil.TestKeyPrefix + "order:1:b",
		testutil.TestKeyPrefix + "order:2:c",
		testutil.TestKeyPrefix + "user:1:x",
	} {
		if err := c.SetRaw(ctx, k, "v", 0); err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
	}

	keys, truncated, err := c.Scan(ctx, testutil.TestKeyPrefix+"order:1:*")
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if truncated {
		t.Error("should not be truncated with 2 matches and limit 200")
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d: %v", len(keys), keys)
	}
}

func TestScan_TruncatesAtLimit(t *testing.T) {
	addr := testutil.RedisAddr(t)
	testutil.CleanTestKeys(t, addr)
	t.Cleanup(func() { testutil.CleanTestKeys(t, addr) })

	db := testutil.RedisDB(t)
	pw := testutil.RedisPassword()
	ctx := context.Background()

	c, err := NewClient(addr, "", pw, db, 200)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer c.Close()

	for _, k := range []string{
		testutil.TestKeyPrefix + "k:1",
		testutil.TestKeyPrefix + "k:2",
		testutil.TestKeyPrefix + "k:3",
		testutil.TestKeyPrefix + "k:4",
		testutil.TestKeyPrefix + "k:5",
	} {
		if err := c.SetRaw(ctx, k, "v", 0); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	limited, err := NewClient(addr, "", pw, db, 3)
	if err != nil {
		t.Fatalf("new limited client: %v", err)
	}
	defer limited.Close()

	keys, truncated, err := limited.Scan(ctx, testutil.TestKeyPrefix+"k:*")
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !truncated {
		t.Error("expected truncated=true when matches exceed limit")
	}
	if len(keys) != 3 {
		t.Errorf("expected 3 keys, got %d", len(keys))
	}
}

func TestScan_RejectsBroadPattern(t *testing.T) {
	c := setupClient(t)
	// 纯通配、空串、前缀不足 2 个有效字符，全部必须拒绝
	for _, p := range []string{"*", "", "a:*", ":*", "*:x"} {
		if _, _, err := c.Scan(context.Background(), p); err == nil {
			t.Errorf("Scan must reject broad pattern %q", p)
		}
	}
}
