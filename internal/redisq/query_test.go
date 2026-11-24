package redisq

import (
	"context"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"

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

func TestFetchExactKey_CollectionTypesReturnItems(t *testing.T) {
	c := setupClient(t)
	ctx := context.Background()

	// list / set / zset 三种集合类型的值都走 Items 字段。
	// 前端曾经只渲染 fields 与 value，导致这三类"有 key 没值"，
	// 这里把「后端确实返回了 items」钉住，防止再退化成空。
	listKey := testutil.TestKeyPrefix + "coll:list"
	setKey := testutil.TestKeyPrefix + "coll:set"
	zsetKey := testutil.TestKeyPrefix + "coll:zset"

	if err := c.rdb.RPush(ctx, listKey, "a", "b").Err(); err != nil {
		t.Fatalf("seed list: %v", err)
	}
	if err := c.rdb.SAdd(ctx, setKey, "x", "y").Err(); err != nil {
		t.Fatalf("seed set: %v", err)
	}
	// member 故意带冒号：验证 member 与 score 的分隔不会被误切
	if err := c.rdb.ZAdd(ctx, zsetKey,
		redis.Z{Score: 120, Member: "app_user"},
		redis.Z{Score: 0.1 + 0.2, Member: "ns:order_item"},
	).Err(); err != nil {
		t.Fatalf("seed zset: %v", err)
	}

	cases := []struct {
		key string
		typ string
	}{
		{listKey, "list"},
		{setKey, "set"},
		{zsetKey, "zset"},
	}
	for _, tc := range cases {
		got, err := c.Fetch(ctx, tc.key)
		if err != nil {
			t.Fatalf("fetch %s: %v", tc.key, err)
		}
		if got.Type != tc.typ {
			t.Errorf("%s: Type = %q, want %q", tc.key, got.Type, tc.typ)
		}
		if len(got.Items) != 2 {
			t.Fatalf("%s: expected 2 items, got %d (%v)", tc.key, len(got.Items), got.Items)
		}
	}

	// zset 元素形态固定为 "member:score"，前端按最后一个冒号切分。
	// ZRANGE 按分数升序，所以 0.3 的排在前、120 的排在后。
	z, err := c.Fetch(ctx, zsetKey)
	if err != nil {
		t.Fatalf("fetch zset: %v", err)
	}
	// 浮点误差不能长尾暴露给前端（0.1+0.2 直接 %v 会得到 0.30000000000000004）
	if z.Items[0] != "ns:order_item:0.3" {
		t.Errorf("zset item[0] = %q, want ns:order_item:0.3", z.Items[0])
	}
	if z.Items[1] != "app_user:120" {
		t.Errorf("zset item[1] = %q, want app_user:120", z.Items[1])
	}
	// 按最后一个冒号切分后，member 应还原成 ns:order_item（而不是 ns）
	last := strings.LastIndex(z.Items[0], ":")
	if z.Items[0][:last] != "ns:order_item" {
		t.Errorf("member should be ns:order_item, got %q", z.Items[0][:last])
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
