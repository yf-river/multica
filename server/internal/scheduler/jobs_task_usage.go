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

// ensureTaskUsageRollupState 在调用汇总函数前补齐状态行，并把"纪元水位"当作从未
// 汇总来处理。
//
// 这两件事原本写在 620/634 的 SQL 函数体里，但那是函数体替换（非新增结构），发布器
// 要求单独的数据库兼容方案，旧站无法继续运行。改成应用层 DML 后语义不变、不需要迁移：
//   - 状态行缺失时按最早一条 task_usage 播种，函数不再因为 UPDATE 匹配 0 行而静默空转；
//   - 101 迁移种下的 1970-01-01 只是"从未汇总"，跳到真实最早数据而不是逐天爬 20k 次。
func ensureTaskUsageRollupState(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `
		INSERT INTO task_usage_hourly_rollup_state (id, watermark_at)
		SELECT 1, COALESCE(date_trunc('hour', MIN(created_at)), now() - INTERVAL '5 minutes')
		  FROM task_usage
		ON CONFLICT (id) DO NOTHING
	`); err != nil {
		return fmt.Errorf("seed rollup state: %w", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE task_usage_hourly_rollup_state
		   SET watermark_at = COALESCE((SELECT date_trunc('hour', MIN(created_at)) FROM task_usage),
		                               now() - INTERVAL '5 minutes')
		 WHERE id = 1
		   AND watermark_at <= TIMESTAMPTZ '1970-01-02 00:00:00+00'
	`); err != nil {
		return fmt.Errorf("heal epoch watermark: %w", err)
	}
	return nil
}

// makeTaskUsageHourlyHandler 在补齐状态行后连续调用 SQL 汇总函数，直到水位不再推进
// （追平、或别的写入者持有 advisory lock 4246）或达到本次 tick 的上限。
//
// 函数内部一天一窗口、并且自带 advisory lock 4246：本 handler 不抢锁，只在锁内做一天
// 的活；追上历史靠连续调用，而不是靠一条语句重算很久，所以超时取消时锁仍会释放。
func makeTaskUsageHourlyHandler(pool *pgxpool.Pool) Handler {
	return func(ctx context.Context, in HandlerInput) (HandlerResult, error) {
		if err := ensureTaskUsageRollupState(ctx, pool); err != nil {
			return HandlerResult{}, err
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
