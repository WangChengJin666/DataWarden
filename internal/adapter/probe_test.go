package adapter

import (
	"context"
	"strings"
	"testing"
	"time"
)

// PostgreSQL 的库名是连接串的一部分，留空必然失败。
// 这里要保证给出的是「必须填写库名」这种可执行的提示，
// 而不是驱动抛出的 "database \"x\" does not exist"。
func TestProbe_PostgresRequiresDatabase(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := Probe(ctx, ProbeConfig{
		Type: "postgresql", Host: "127.0.0.1", Port: 5432,
		Database: "", User: "u", Password: "p",
	})
	if err == nil {
		t.Fatal("expected error when postgres database is empty")
	}
	if !strings.Contains(err.Error(), "必须填写库名") {
		t.Errorf("error should tell the user to fill the database, got %v", err)
	}
}

// MySQL 允许不指定库，因此不能因为库名为空就在探测阶段直接拒绝。
// 这里用不存在的端口验证：它会因「连不上」失败，而不是因「库名为空」失败。
func TestProbe_MySQLAllowsEmptyDatabase(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := Probe(ctx, ProbeConfig{
		Type: "mysql", Host: "127.0.0.1", Port: 5999,
		Database: "", User: "u", Password: "p",
	})
	if err == nil {
		t.Fatal("expected connection error on unused port")
	}
	if strings.Contains(err.Error(), "必须填写库名") {
		t.Errorf("mysql must not require a database, got %v", err)
	}
}

// Redis 的 database 字段是 db 号，非整数要明确拒绝。
func TestProbe_RedisRejectsNonNumericDB(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := Probe(ctx, ProbeConfig{
		Type: "redis", Host: "127.0.0.1", Port: 6379,
		Database: "abc",
	})
	if err == nil {
		t.Fatal("expected error for non-numeric db")
	}
	if !strings.Contains(err.Error(), "必须是整数") {
		t.Errorf("unexpected error: %v", err)
	}
}

// MongoDB 现在已支持探测，但默认库必填：不指定库无法确定查哪个库。
func TestProbe_MongoDBRequiresDatabase(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := Probe(ctx, ProbeConfig{
		Type: "mongodb", Host: "127.0.0.1", Port: 27017,
	})
	if err == nil {
		t.Fatal("mongodb probe without database must fail")
	}
	if !strings.Contains(err.Error(), "默认库") {
		t.Errorf("error should mention the missing database, got: %v", err)
	}
}

// 连不上时必须返回错误，不能假装成功。
func TestProbe_MongoDBUnreachableIsError(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping network probe in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	// 用一个几乎不可能有 MongoDB 的端口
	_, err := Probe(ctx, ProbeConfig{
		Type: "mongodb", Host: "127.0.0.1", Port: 5999, Database: "test",
	})
	if err == nil {
		t.Fatal("mongodb probe against a dead port must not succeed")
	}
}

func TestProbe_RejectsUnknownType(t *testing.T) {
	ctx := context.Background()
	_, err := Probe(ctx, ProbeConfig{Type: "oracle"})
	if err == nil {
		t.Fatal("expected error for unknown type")
	}
}
