package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func healthMigrationFixture(t *testing.T) (*pgxpool.Pool, runOptions) {
	t.Helper()
	admin := openTestPool(t)
	ctx := context.Background()
	schema := fmt.Sprintf("health_history_%d_%d", time.Now().UnixNano(), rand.Uint32())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Log(err)
		}
	})
	pool := openTestPoolWithSearchPath(t, schema)
	if _, err := pool.Exec(ctx, "CREATE TABLE chat_message(id uuid NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	return pool, runOptions{Direction: "up", SchemaMigrationsTable: schema + ".schema_migrations", AdvisoryLockKey: int64(rand.Uint64()&0x7fffffffffffffff) | 1, Conditions: conditionsForDirection("up")}
}

func healthSQL(t *testing.T, version string) string {
	t.Helper()
	files := realMigrationFiles(t, []string{version}, "up")
	sql, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	return string(sql)
}
func execHealth(t *testing.T, pool *pgxpool.Pool, sql string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql); err != nil {
		t.Fatal(err)
	}
}

func TestExistingHealthSchemaCreatesOrPreservesExactObjects(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint("existing=", existing), func(t *testing.T) {
			pool, opts := healthMigrationFixture(t)
			if existing {
				execHealth(t, pool, healthSQL(t, "501_health_data"))
				execHealth(t, pool, healthSQL(t, "508_chat_health_reports"))
				execHealth(t, pool, `INSERT INTO health_state(workspace_id,user_id,data) VALUES('00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000002','{"preserved":true}')`)
				execHealth(t, pool, `INSERT INTO chat_message(id,health_report_ids) VALUES('00000000-0000-0000-0000-000000000003','{00000000-0000-0000-0000-000000000004}')`)
			}
			opts.Files = realMigrationFiles(t, []string{"501_health_data", "508_chat_health_reports"}, "up")
			for range 2 {
				if err := runMigrations(context.Background(), pool, opts); err != nil {
					t.Fatal(err)
				}
			}
			var count int
			if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM "+opts.SchemaMigrationsTable).Scan(&count); err != nil || count != 2 {
				t.Fatalf("ledger count=%d err=%v", count, err)
			}
			if existing {
				var preserved bool
				if err := pool.QueryRow(context.Background(), `SELECT data='{"preserved":true}'::jsonb FROM health_state`).Scan(&preserved); err != nil || !preserved {
					t.Fatalf("health data changed: %v %v", preserved, err)
				}
				if err := pool.QueryRow(context.Background(), `SELECT health_report_ids='{00000000-0000-0000-0000-000000000004}'::uuid[] FROM chat_message`).Scan(&preserved); err != nil || !preserved {
					t.Fatalf("report ids changed: %v %v", preserved, err)
				}
			}
		})
	}
}

func TestExistingHealthSchemaRejectsPartialOrDifferentObjects(t *testing.T) {
	mutations := []string{
		"DROP TABLE health_state", "DROP TABLE health_observation", "DROP TABLE health_revision", "DROP TABLE health_report", "DROP TABLE health_report_result",
		"ALTER TABLE health_state ALTER COLUMN revision TYPE integer",
		"ALTER TABLE health_observation ALTER COLUMN data DROP NOT NULL",
		"ALTER TABLE health_state ALTER COLUMN data SET DEFAULT '{}'::jsonb",
		"ALTER TABLE health_report DROP COLUMN filename",
		"ALTER TABLE health_report ADD COLUMN extra text",
		"ALTER TABLE health_revision ADD CONSTRAINT unexpected CHECK(revision>=0)",
		"ALTER TABLE health_state ENABLE ROW LEVEL SECURITY",
	}
	for _, sql := range mutations {
		t.Run(sql, func(t *testing.T) {
			pool, opts := healthMigrationFixture(t)
			execHealth(t, pool, healthSQL(t, "501_health_data"))
			execHealth(t, pool, sql)
			opts.Files = realMigrationFiles(t, []string{"501_health_data"}, "up")
			if err := runMigrations(context.Background(), pool, opts); err == nil {
				t.Fatal("incorrect existing schema accepted")
			}
			var count int
			if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM "+opts.SchemaMigrationsTable).Scan(&count); err != nil || count != 0 {
				t.Fatalf("incorrect schema recorded count=%d err=%v", count, err)
			}
		})
	}
}

func TestExistingChatHealthColumnRejectsDifferences(t *testing.T) {
	for _, sql := range []string{
		"ALTER TABLE chat_message ALTER COLUMN health_report_ids TYPE text[]",
		"ALTER TABLE chat_message ALTER COLUMN health_report_ids DROP NOT NULL",
		"ALTER TABLE chat_message ALTER COLUMN health_report_ids SET DEFAULT '{00000000-0000-0000-0000-000000000001}'::uuid[]",
	} {
		t.Run(sql, func(t *testing.T) {
			pool, opts := healthMigrationFixture(t)
			execHealth(t, pool, healthSQL(t, "508_chat_health_reports"))
			execHealth(t, pool, sql)
			opts.Files = realMigrationFiles(t, []string{"508_chat_health_reports"}, "up")
			if err := runMigrations(context.Background(), pool, opts); err == nil {
				t.Fatal("incorrect report column accepted")
			}
			var count int
			if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM "+opts.SchemaMigrationsTable).Scan(&count); err != nil || count != 0 {
				t.Fatalf("incorrect column recorded count=%d err=%v", count, err)
			}
		})
	}
}

func TestExistingHealthSchemaRejectsChangedMigrationSQL(t *testing.T) {
	for _, version := range []string{"501_health_data", "508_chat_health_reports"} {
		t.Run(version, func(t *testing.T) {
			pool, opts := healthMigrationFixture(t)
			file := filepath.Join(t.TempDir(), version+".up.sql")
			if err := os.WriteFile(file, []byte(healthSQL(t, version)+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			opts.Files = []string{file}
			if err := runMigrations(context.Background(), pool, opts); err == nil {
				t.Fatal("changed historical SQL accepted")
			}
			var count int
			if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM "+opts.SchemaMigrationsTable).Scan(&count); err != nil || count != 0 {
				t.Fatalf("changed SQL recorded count=%d err=%v", count, err)
			}
		})
	}
}
