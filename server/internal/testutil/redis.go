package testutil

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"
)

const (
	RedisDBAuth = 10 + iota
	RedisDBMiddleware
	RedisDBService
	RedisDBHandler
)

var redisTestDatabases = map[int]string{
	RedisDBAuth:       "auth",
	RedisDBMiddleware: "middleware",
	RedisDBService:    "service",
	RedisDBHandler:    "handler",
}

func redisTestOptions(rawURL string, database int) (*redis.Options, error) {
	owner, ok := redisTestDatabases[database]
	if !ok {
		return nil, fmt.Errorf("redis test DB %d is not reserved for a package", database)
	}
	opts, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse REDIS_TEST_URL for %s tests: %w", owner, err)
	}
	opts.DB = database
	return opts, nil
}

func NewRedisTestClient(t testing.TB, database int) *redis.Client {
	t.Helper()
	url := os.Getenv("REDIS_TEST_URL")
	if url == "" {
		t.Skip("REDIS_TEST_URL not set")
	}
	opts, err := redisTestOptions(url, database)
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(opts)
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("REDIS_TEST_URL unreachable: %v", err)
	}
	if err := rdb.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("flushdb: %v", err)
	}
	t.Cleanup(func() {
		rdb.FlushDB(context.Background())
		_ = rdb.Close()
	})
	return rdb
}
