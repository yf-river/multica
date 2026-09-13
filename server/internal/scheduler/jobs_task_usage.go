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

// makeTaskUsageHourlyHandler 在调用 SQL 函数前后各读一次水位线，好让审计行记录「业务
// 状态是否推进了」。函数调用本身在 SQL 内部获取 advisory lock 4246——若别的写入者正
// 持有该锁，函数立刻返回 0，本次尝试以 rows_affected=0 的 SUCCESS 收场，这正是
// 「内层竞争没赢、没有活可干」的正确结局。
func makeTaskUsageHourlyHandler(pool *pgxpool.Pool) Handler {
	return func(ctx context.Context, in HandlerInput) (HandlerResult, error) {
		watermarkBefore, err := readTaskUsageWatermark(ctx, pool)
		if err != nil {
			return HandlerResult{}, fmt.Errorf("read watermark before: %w", err)
		}

		var rows int64
		if err := pool.QueryRow(ctx, `SELECT rollup_task_usage_hourly()`).Scan(&rows); err != nil {
			return HandlerResult{}, fmt.Errorf("rollup_task_usage_hourly: %w", err)
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
