// Package delegatedrecoverybackfill 提供有界、可续跑的批处理，用来清退那些
// 已被终端送达回执关闭、但仍挂着"委托失败恢复"评论的历史记录。
//
// 迁移 444/445 新增了 comment.recovery_settled_at 及只包含"未结算恢复评论"的
// 部分索引；TaskService 会在每条终端任务事务里写该标记。但标记存在之前写下的历史
// 记录覆盖不到：这些行仍命中索引条件，sweeper 的 outbox 扫描每次 tick 都会反复
// 证明它们已结算。本遍历给这些行补一次标记。
//
// 请在滚动发布里所有后端都带上写入代码之后再运行。提前运行也无害——进行中的任务
// 只会被最先到达其终端回执的那条路径标记——但会留下一些行等第二轮清理。
package delegatedrecoverybackfill

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultBatchSize 限制每条语句检查的候选评论数。
//
// 在合成数据（50 万评论 / 20 万任务 / 5 万未结算恢复）上实测：批大小不变时总遍历
// 时间基本持平——1000 时 50x228ms、5000 时 10x1016ms、10000 时 5x2199ms，都约 11 秒——
// 因为每批的 agent_task_queue 读取不是主要成本。因此批大小只影响单条语句持有 comment
// 行锁的时长，取这些吞吐相等的选项中最小者即可。命令的批间延迟才是真正决定遍历节奏的。
const DefaultBatchSize = 1000

// Cursor 是一次遍历的 keyset 位置。用 (created_at, id) 而不是只用 id，是为了匹配
// idx_comment_delegated_failure_unsettled 索引——仅按 id 的 keyset 无法让该索引同时
// 服务条件与排序，这正是整个改动要消除的"LIMIT 只限制返回、不限制扫描"的问题。
type Cursor struct {
	CreatedAt time.Time
	ID        string
}

// Options 控制一批回填。零值是生产默认值。
type Options struct {
	// BatchSize 覆盖 DefaultBatchSize。
	BatchSize int
	// After 是排他性的 keyset 水位。nil 表示从头开始。
	After *Cursor
	// Schema 是仅测试使用的可信标识符覆盖项。
	Schema string
}

// BatchResult 推进调用方的 keyset 遍历。
//
// Last 记录的是扫描过的候选而非已结算者：一条真正仍挂起的恢复——或只是被某可逆条件
// （如 issue 处于 'done'）暂时排除的——绝不能被打标记，也不能卡住遍历。只有整批找不到
// 任何候选时 Last 才是 nil，调用方以此判断遍历结束。
type BatchResult struct {
	Scanned int64
	Settled int64
	Last    *Cursor
}

// qualify 给表名加 schema 前缀（测试用隔离 schema），空 schema 则原样返回。
func qualify(schema, table string) string {
	if schema == "" {
		return table
	}
	return fmt.Sprintf("%q.%s", schema, table)
}

// Batch 至多结算 BatchSize 条候选恢复评论。
//
// 只有某条终端任务已经把它写进 delivered_comment_ids 时才结算——这是
// ListPendingDelegatedFailureRecoveries 里唯一不可撤回的排除项。该查询应用的其余
// 排除都可逆，所以因为其他原因被跳过的行在这里刻意保持挂起。
//
// 回执查询与运行时查询方向相反：不是逐条候选问"有没有终端任务持有这条评论"——那会为
// 每条被查的行反复读数据库最大表 agent_task_queue——而是每批读一次 agent_task_queue，
// 再把 delivered 的 id 与候选做 hash join。两个 CTE 都 MATERIALIZED，无论规划器如何
// 内联都保持该行为。
func Batch(ctx context.Context, pool *pgxpool.Pool, opts Options) (BatchResult, error) {
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}
	comment := qualify(opts.Schema, "comment")
	task := qualify(opts.Schema, "agent_task_queue")

	var afterCreatedAt any
	var afterID any
	if opts.After != nil {
		afterCreatedAt = opts.After.CreatedAt
		afterID = opts.After.ID
	}

	query := fmt.Sprintf(`
WITH batch AS MATERIALIZED (
    SELECT id, created_at
    FROM %s
    WHERE author_type = 'system'
      AND type = 'progress_update'
      AND source_task_id IS NOT NULL
      AND recovery_settled_at IS NULL
      AND ($2::timestamptz IS NULL OR (created_at, id) > ($2::timestamptz, $3::uuid))
    ORDER BY created_at, id
    LIMIT $1
), receipts AS MATERIALIZED (
    SELECT DISTINCT batch.id
    FROM %s covering
    CROSS JOIN LATERAL unnest(covering.delivered_comment_ids) AS delivered(id)
    JOIN batch ON batch.id = delivered.id
    WHERE covering.status IN ('completed', 'failed', 'cancelled')
      AND covering.delivered_comment_ids && (SELECT array_agg(id) FROM batch)
), settled AS (
    UPDATE %s recovery
    SET recovery_settled_at = now()
    WHERE recovery.id IN (SELECT id FROM receipts)
      AND recovery.recovery_settled_at IS NULL
    RETURNING recovery.id
)
SELECT (SELECT count(*)::bigint FROM batch),
       (SELECT count(*)::bigint FROM settled),
       (SELECT created_at FROM batch ORDER BY created_at DESC, id DESC LIMIT 1),
       COALESCE((SELECT id::text FROM batch ORDER BY created_at DESC, id DESC LIMIT 1), '')`,
		comment, task, comment)

	var result BatchResult
	var lastCreatedAt *time.Time
	var lastID string
	if err := pool.QueryRow(ctx, query, batchSize, afterCreatedAt, afterID).
		Scan(&result.Scanned, &result.Settled, &lastCreatedAt, &lastID); err != nil {
		return BatchResult{}, fmt.Errorf("backfill delegated failure recovery settled batch: %w", err)
	}
	if lastCreatedAt != nil && lastID != "" {
		result.Last = &Cursor{CreatedAt: *lastCreatedAt, ID: lastID}
	}
	return result, nil
}

// CountRemaining 报告仍未结算的恢复评论数，它正好等于
// idx_comment_delegated_failure_unsettled 的行数。完整遍历后，这个值应当是真正
// 挂起的积压量，而不是历史遗留量。
func CountRemaining(ctx context.Context, pool *pgxpool.Pool, schema string) (int64, error) {
	var count int64
	if err := pool.QueryRow(ctx, fmt.Sprintf(`
SELECT count(*)
FROM %s
WHERE author_type = 'system'
  AND type = 'progress_update'
  AND source_task_id IS NOT NULL
  AND recovery_settled_at IS NULL`, qualify(schema, "comment"))).Scan(&count); err != nil {
		return 0, fmt.Errorf("count unsettled delegated failure recoveries: %w", err)
	}
	return count, nil
}
