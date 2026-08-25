package testutil

import "testing"

func TestRedisTestOptionsAssignsReservedDatabase(t *testing.T) {
	t.Parallel()

	for database, owner := range redisTestDatabases {
		database, owner := database, owner
		t.Run(owner, func(t *testing.T) {
			opts, err := redisTestOptions("redis://localhost:6379/1", database)
			if err != nil {
				t.Fatalf("redisTestOptions: %v", err)
			}
			if opts.DB != database {
				t.Fatalf("DB = %d, want reserved DB %d", opts.DB, database)
			}
		})
	}
}

func TestRedisTestOptionsRejectsUnreservedDatabase(t *testing.T) {
	t.Parallel()

	if _, err := redisTestOptions("redis://localhost:6379/1", 0); err == nil {
		t.Fatal("expected DB 0 to be rejected")
	}
}

func TestRedisTestOptionsRejectsInvalidURL(t *testing.T) {
	t.Parallel()

	if _, err := redisTestOptions("://invalid", RedisDBAuth); err == nil {
		t.Fatal("expected invalid Redis URL to be rejected")
	}
}
