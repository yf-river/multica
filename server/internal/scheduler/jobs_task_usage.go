// jobs_task_usage.go 定义并接线「task_usage 小时级汇总」这个调度任务：封装成 JobSpec
// 注册进 scheduler。handler 调用既有的 `rollup_task_usage_hourly()` SQL 函数做真正
// 的汇总，调度器这边只负责租约、心跳与审计行；函数内部自带 advisory lock 4246，
// 与遗留的 pg_cron tick / 手工调用并发执行也是安全的。
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/taskusagebackfill"
)

// JobNameRollupTaskUsageHourly 是审计行里使用的规范任务名。跨版本必须保持稳定——
// 改名必须带迁移。
const JobNameRollupTaskUsageHourly = "rollup_task_usage_hourly"

// TaskUsageHourlyJob 返回驱动 task_usage_hourly 汇总的 JobSpec。handler 调用既有的
// `rollup_task_usage_hourly()` SQL 函数——该函数内部已持有 advisory lock 4246，
// 所以与遗留的 pg_cron tick 或手工调用并发执行是安全的（RFC §11.3）。
//
// 该 spec 即 RFC §11.1 里的规范配置：
//
//	cadence:               5m
//	schedule_delay:        5m
//	catch_up_mode:         latest_only（历史由 handler 的水位线驱动）
//	run_timeout:           25m
//	stale_timeout:         30m
//	heartbeat_interval:    30s
//	max_attempts:          3
//	retry_backoff:         1m, 5m, 15m
//	allow_stale_reentry:   true
func TaskUsageHourlyJob(pool *pgxpool.Pool) JobSpec {
	return JobSpec{
		Name:              JobNameRollupTaskUsageHourly,
		Cadence:           5 * time.Minute,
		ScheduleDelay:     5 * time.Minute,
		CatchUpMode:       CatchUpLatestOnly,
		CatchUpWindow:     24 * time.Hour,
		RunTimeout:        25 * time.Minute,
		StaleTimeout:      30 * time.Minute,
		HeartbeatInterval: 30 * time.Second,
		AllowStaleReentry: true,
		MaxAttempts:       3,
		RetryBackoff: []time.Duration{
			1 * time.Minute,
			5 * time.Minute,
			15 * time.Minute,
		},
		Scopes:  StaticScopes(ScopeGlobal),
		Handler: makeTaskUsageHourlyHandler(pool),
	}
}

// taskUsageCatchUpTicks 限制单次 tick 最多连续调用多少次汇总函数。每次调用最多推进
// 一天（函数内部的一天窗口上限），60 次即 60 天；更大的积压留给下一次 tick，避免
// 一次 tick 长时间占着 advisory lock 4246。
const taskUsageCatchUpTicks = 60

// ensureTaskUsageRollupState 在既有汇总锁内补齐状态，并跳过没有原始变更或脏键的空区间。
// 水位按 updated_at（旧行回落 created_at）筛选变更，桶日期仍来自 created_at。
func ensureTaskUsageRollupState(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanupCtx)
	}()
	var locked bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(4246)`).Scan(&locked); err != nil {
		return false, err
	}
	if !locked {
		return false, nil
	}
	// 原始变更从当前水位起查；旧dirty键仍需消费，不能按水位过滤。三个MIN复用既有索引。
	if _, err := tx.Exec(ctx, `
		WITH progress AS (SELECT watermark_at FROM task_usage_hourly_rollup_state WHERE id = 1)
		INSERT INTO task_usage_hourly_rollup_state (id, watermark_at)
		SELECT 1, LEAST(now() - INTERVAL '5 minutes', COALESCE(date_trunc('hour', LEAST(
			(SELECT MIN(updated_at) FROM task_usage WHERE updated_at >= COALESCE((SELECT watermark_at FROM progress), '-infinity'::timestamptz)),
			(SELECT MIN(created_at) FROM task_usage WHERE updated_at IS NULL AND created_at >= COALESCE((SELECT watermark_at FROM progress), '-infinity'::timestamptz)),
			(SELECT MIN(enqueued_at) FROM task_usage_hourly_dirty)
		)), now() - INTERVAL '5 minutes'))
		ON CONFLICT (id) DO UPDATE SET watermark_at = EXCLUDED.watermark_at
		WHERE task_usage_hourly_rollup_state.watermark_at < EXCLUDED.watermark_at
	`); err != nil {
		return false, fmt.Errorf("heal empty rollup interval: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// makeTaskUsageHourlyHandler 在补齐状态行后连续调用 SQL 汇总函数，直到水位不再推进
// （追平、或别的写入者持有 advisory lock 4246）或达到本次 tick 的上限。
//
// 函数内部一天一窗口、并且自带 advisory lock 4246：本 handler 不抢锁，只在锁内做一天
// 的活；追上历史靠连续调用，而不是靠一条语句重算很久，所以超时取消时锁仍会释放。
func makeTaskUsageHourlyHandler(pool *pgxpool.Pool) Handler {
	return func(ctx context.Context, in HandlerInput) (HandlerResult, error) {
		ready, err := ensureTaskUsageRollupState(ctx, pool)
		if err != nil {
			return HandlerResult{}, err
		}
		if !ready {
			return HandlerResult{Result: map[string]any{"advisory_lock_id": taskusagebackfill.AdvisoryLockKey, "rollup_ticks": 1, "skipped_lock": true}}, nil
		}

		watermarkBefore, err := readTaskUsageWatermark(ctx, pool)
		if err != nil {
			return HandlerResult{}, fmt.Errorf("read watermark before: %w", err)
		}

		// 整轮都跑在一条固定连接上：函数用的是会话级 advisory lock 4246，一旦语句被
		// statement_timeout 或取消打断，锁会留在那个会话里（plpgsql 的 WHEN OTHERS
		// 不接 query_canceled）。固定连接后我们能在归还连接池之前显式解锁，不会让它
		// 带着锁回到池里，后续 tick 与 DeleteWorkspace 的 xact 锁都不被卡住。
		conn, err := pool.Acquire(ctx)
		if err != nil {
			return HandlerResult{}, fmt.Errorf("acquire rollup connection: %w", err)
		}
		defer func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _ = conn.Exec(cleanupCtx, `SELECT pg_advisory_unlock(4246)`)
			conn.Release()
		}()

		var rows int64
		ticks := 0
		previous := watermarkBefore
		for ticks < taskUsageCatchUpTicks {
			ticks++
			var tickRows int64
			if err := conn.QueryRow(ctx, `SELECT rollup_task_usage_hourly()`).Scan(&tickRows); err != nil {
				return HandlerResult{}, fmt.Errorf("rollup_task_usage_hourly: %w", err)
			}
			rows += tickRows

			// 水位不动就停：已追平，或这一轮没拿到锁。继续调用只会空转。
			after, err := readTaskUsageWatermark(ctx, pool)
			if err != nil {
				return HandlerResult{}, fmt.Errorf("read watermark after tick: %w", err)
			}
			if !after.After(previous) {
				break
			}
			previous = after

			if in.Heartbeat != nil {
				_ = in.Heartbeat(ctx)
			}
		}

		watermarkAfter, err := readTaskUsageWatermark(ctx, pool)
		if err != nil {
			return HandlerResult{}, fmt.Errorf("read watermark after: %w", err)
		}

		// 结尾轻量心跳一次，让跑得比 HeartbeatInterval 短得多的任务也能把 stale_after
		// 保持在新鲜状态。
		if in.Heartbeat != nil {
			_ = in.Heartbeat(ctx)
		}

		result := map[string]any{
			"advisory_lock_id": taskusagebackfill.AdvisoryLockKey,
			"rollup_ticks":     ticks,
		}
		if !watermarkBefore.IsZero() {
			result["watermark_before"] = watermarkBefore.UTC().Format(time.RFC3339)
		}
		if !watermarkAfter.IsZero() {
			result["watermark_after"] = watermarkAfter.UTC().Format(time.RFC3339)
		}
		return HandlerResult{
			RowsAffected: rows,
			Result:       result,
		}, nil
	}
}

// readTaskUsageWatermark 读当前水位线；行缺失时返回零值 time.Time（101 迁移之后
// 不应该发生，但 handler 遇到时也不该 panic）。
func readTaskUsageWatermark(ctx context.Context, pool *pgxpool.Pool) (time.Time, error) {
	var t time.Time
	err := pool.QueryRow(ctx, `
		SELECT watermark_at
		  FROM task_usage_hourly_rollup_state
		 WHERE id = 1
	`).Scan(&t)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	return t, nil
}
