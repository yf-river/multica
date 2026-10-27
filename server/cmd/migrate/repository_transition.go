package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/migrations"
)

// 仓库迁移只在尚未开放写入时允许恢复；已验收后的配置不能被旧备份覆盖。
func repositoryTransition(ctx context.Context, pool *pgxpool.Pool, action string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var exists bool
	if err = tx.QueryRow(ctx, "SELECT to_regclass('repository_migration_state') IS NOT NULL").Scan(&exists); err != nil {
		return err
	}
	var accepted bool
	if exists {
		if err = tx.QueryRow(ctx, "SELECT accepted FROM repository_migration_state FOR UPDATE").Scan(&accepted); err != nil {
			return err
		}
	} else {
		if action == "accept-repositories" {
			return fmt.Errorf("仓库迁移尚未完成")
		}
		var backup bool
		if err = tx.QueryRow(ctx, "SELECT to_regclass('repository_migration_backup') IS NOT NULL").Scan(&backup); err != nil {
			return err
		}
		if !backup {
			return nil
		}
	}
	if action == "accept-repositories" {
		if _, err = tx.Exec(ctx, "UPDATE repository_migration_state SET accepted=true"); err != nil {
			return err
		}
	} else {
		if accepted {
			return fmt.Errorf("仓库迁移已验收，拒绝用旧配置覆盖后续修改")
		}
		directory, e := migrations.ResolveDir()
		if e != nil {
			return e
		}
		for _, version := range []string{"525_repository_migration_guard", "524_repository_backfill"} {
			path := filepath.Join(directory, version+".down.sql")
			if _, statErr := os.Stat(path); statErr != nil {
				// 历史迁移已收敛到 archive/，恢复路径仍要能找到当时的回滚脚本。
				path = filepath.Join(directory, "archive", version+".down.sql")
			}
			sql, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			if _, err = tx.Exec(ctx, string(sql)); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, "DELETE FROM schema_migrations WHERE version=$1", version); err != nil {
				return err
			}
		}
		// 这些表在暂停写入期间仅含回填数据；下次迁移从保留的原始配置重新生成。
		if _, err = tx.Exec(ctx, "DELETE FROM repository_location; DELETE FROM repository"); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
