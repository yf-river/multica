package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/migrations"
)

func TestRepositoryMigrationAndRecovery(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL is required")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("repository_migration_test_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	exec := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE TABLE workspace(id uuid, repos jsonb); CREATE TABLE project_resource(id uuid,workspace_id uuid,resource_type text,resource_ref jsonb);CREATE TABLE schema_migrations(version text);`)
	exec(`INSERT INTO workspace VALUES ('00000000-0000-0000-0000-000000000001','[{"url":"https://github.com/Test/Same.git","provider":"github","ref":"main"}]');
 INSERT INTO project_resource VALUES
 ('00000000-0000-0000-0000-000000000011','00000000-0000-0000-0000-000000000001','github_repo','{"url":"git@github.com:test/same.git","ref":"feature-a"}'),
 ('00000000-0000-0000-0000-000000000012','00000000-0000-0000-0000-000000000001','github_repo','{"url":"https://github.com/Test/other","branch":"feature-b"}');`)
	directory, err := migrations.ResolveDir()
	if err != nil {
		t.Fatal(err)
	}
	apply := func(version string) {
		t.Helper()
		body, err := os.ReadFile(filepath.Join(directory, version+".up.sql"))
		if err != nil {
			t.Fatal(err)
		}
		exec(string(body))
		_, err = pool.Exec(ctx, "INSERT INTO schema_migrations VALUES($1)", version)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []string{"519_repositories", "520_repository_id", "521_repository_identity", "522_repository_location_id", "523_repository_location_daemon", "524_repository_backfill"} {
		apply(v)
	}
	// 模拟回填已完成、写入保护迁移尚未执行时进程退出。
	if err = repositoryTransition(ctx, pool, "restore-repositories"); err != nil {
		t.Fatal(err)
	}
	apply("524_repository_backfill")
	apply("525_repository_migration_guard")
	var count int
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM repository").Scan(&count); err != nil || count != 2 {
		t.Fatalf("deduplicated count=%d error=%v", count, err)
	}
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM repository WHERE default_for_tasks").Scan(&count); err != nil || count != 1 {
		t.Fatalf("default count=%d error=%v", count, err)
	}
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM project_resource WHERE resource_ref ? 'repository_id' AND resource_ref->>'ref' IN ('feature-a','feature-b') AND NOT resource_ref ? 'url'").Scan(&count); err != nil || count != 2 {
		t.Fatalf("bindings=%d error=%v", count, err)
	}
	if _, err = pool.Exec(ctx, "UPDATE repository SET revision=2"); err == nil {
		t.Fatal("migration did not pause repository writes")
	}
	if _, err = pool.Exec(ctx, "DELETE FROM project_resource"); err == nil {
		t.Fatal("migration did not pause project binding writes")
	}
	if err = repositoryTransition(ctx, pool, "restore-repositories"); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM project_resource WHERE resource_ref ? 'url'").Scan(&count); err != nil || count != 2 {
		t.Fatalf("restored=%d error=%v", count, err)
	}
	apply("524_repository_backfill")
	apply("525_repository_migration_guard")
	if err = repositoryTransition(ctx, pool, "accept-repositories"); err != nil {
		t.Fatal(err)
	}
	exec("UPDATE repository SET revision=2")
	if err = repositoryTransition(ctx, pool, "restore-repositories"); err == nil {
		t.Fatal("accepted data may not be overwritten by old backup")
	}
}
