// Package issueactivitybackfill 提供有界、可续跑的批处理，用于从已有的
// updated_at 历史里为 issue.last_activity_at 播种。
package issueactivitybackfill

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultBatchSize 限制每条事务及其锁定的 issue 行数。运维可从命令包装层在
// 两次调用间加入延迟。
const DefaultBatchSize = 1000

// Options 控制一批回填。零值是生产默认值。
type Options struct {
	// BatchSize 覆盖 DefaultBatchSize。
	BatchSize int
	// AfterID 是当前这轮遍历的排他 keyset 水位。
	AfterID *string
	// Table 是仅测试使用的可信标识符覆盖项。
	Table string
}

// BatchResult 推进调用方的 keyset 遍历，无需重扫已完成的段。仅当这批没更新任何行时
// LastID 为空。
type BatchResult struct {
	Rows   int64
	LastID string
}

// Batch 至多为 BatchSize 行把 updated_at 拷入 last_activity_at。已被当前写者触碰过的
// 行不再命中外层条件，因此并发语义更新总能赢过历史重建。
func Batch(ctx context.Context, pool *pgxpool.Pool, opts Options) (BatchResult, error) {
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}
	table := opts.Table
	if table == "" {
		table = "issue"
	}

	query := fmt.Sprintf(`
WITH batch AS (
    SELECT id
    FROM %s
    WHERE last_activity_at IS NULL
      AND ($2::uuid IS NULL OR id > $2::uuid)
    ORDER BY id
    LIMIT $1
    FOR UPDATE SKIP LOCKED
), updated AS (
UPDATE %s i
SET last_activity_at = i.updated_at
FROM batch
WHERE i.id = batch.id
	AND i.last_activity_at IS NULL
	RETURNING i.id
)
SELECT COUNT(*)::bigint,
       COALESCE((SELECT id::text FROM updated ORDER BY id DESC LIMIT 1), '')
FROM updated`, table, table)
	var result BatchResult
	if err := pool.QueryRow(ctx, query, batchSize, opts.AfterID).Scan(&result.Rows, &result.LastID); err != nil {
		return BatchResult{}, fmt.Errorf("backfill issue last_activity_at batch: %w", err)
	}
	return result, nil
}

// CountRemaining 报告尚未重建的行数。
func CountRemaining(ctx context.Context, pool *pgxpool.Pool, table string) (int64, error) {
	if table == "" {
		table = "issue"
	}
	var count int64
	if err := pool.QueryRow(ctx, fmt.Sprintf(
		"SELECT count(*) FROM %s WHERE last_activity_at IS NULL", table,
	)).Scan(&count); err != nil {
		return 0, fmt.Errorf("count issue last_activity_at backlog: %w", err)
	}
	return count, nil
}
