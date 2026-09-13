// Package taskusagebackfill 从历史 task_usage 行播种 task_usage_hourly。
//
// 有两个调用方：
//
//   - server/cmd/backfill_task_usage_hourly：显式的运维命令，供该文件记载的
//     SELF-HOST 升级顺序使用。
//   - server/cmd/migrate：作为 hook 在迁移 103 运行前被调用，这样从 v0.3.4
//     （或任何早于小时级管线的版本）直接升级的运维，在服务器仍停机时不会触发
//     迁移 103"失败即关闭"的水位保护。该 hook 可在同一次 `migrate up` 调用里
//     跑完一次完整幂等回填，然后继续应用 103/104。
//
// 实现复用了 rollup worker 使用的同一个 SQL 窗口原语
// （`rollup_task_usage_hourly_window`），因此重复运行是安全的——部分进度会在
// 下次调用时续跑。
//
// 所有调用方在回填遍历期间都必须持有 advisory lock 4246（AdvisoryLockKey）。
// 正是这把锁让本流程可与 SQL 的 `rollup_task_usage_hourly()` cron 条目、
// 进程内 scheduler 及其它并发回填并存——抢到锁者执行，抢不到的等到释放为止。
package taskusagebackfill

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AdvisoryLockKey 是所有驱动 task_usage_hourly 汇总写入路径共享的 int64 标识：
//
//   - 迁移 102 里的 rollup_task_usage_hourly()。
//   - cmd/backfill_task_usage_hourly。
//   - cmd/migrate 的 pre-103 hook（MUL-2957）。
//   - 进程内 scheduler 的 rollup_task_usage_hourly handler（纵深防御——
//     scheduler 的租约已防止跨实例双跑；这把 advisory lock 额外防止与遗留的
//     pg_cron / 手动入口竞争）。
//
// 它与以往每个版本的 rollup 管线使用同一 id，因此混版本集群（滚动发布）不会双写。
const AdvisoryLockKey int64 = 4246

// MaxLagThreshold 对应迁移 103"失败即关闭"防护里的 v_lag 间隔。pre-103 hook
// 只有在水位落后最新 task_usage 事件超过该值时才触发回填；低于阈值时迁移本来
// 就能通过，省去一次扫描。
const MaxLagThreshold = time.Hour

// 水位上限 `now() - 5 minutes` 直接写死在 stampWatermark / stampWatermarkOnConn 的
// SQL UPDATE 里，使计算发生在执行写入的同一会话——不掺入应用侧的 time.Now()。
//（MUL-2957 review: blocker #3。）

// Result 描述一次回填运行的结果。导出供调用方（migrate 命令与测试）打印或断言。
type Result struct {
	// Skipped 是该运行没有做任何切片工作时的简短原因字符串。空字符串表示
	// 该运行确实遍历了至少一个切片。
	Skipped string

	// SlicesProcessed 被汇总的月度切片数。
	SlicesProcessed int

	// RowsTouched 是 rollup_task_usage_hourly_window 返回计数的总和。
	RowsTouched int64

	// From / To 圈定遍历的 UTC 范围。
	From time.Time
	To   time.Time

	// WatermarkStamped 报告是否发出了水位 UPDATE。
	WatermarkStamped bool
}

// HookOptions 控制 Hook 的行为。默认值适用于生产；测试按需覆盖字段。
type HookOptions struct {
	// Logger 接收回填遍历的 slog 记录。nil = slog.Default()。
	Logger *slog.Logger

	// LagThreshold 覆盖 MaxLagThreshold。零 = MaxLagThreshold。测试会传一个
	// 小值，在确定性夹具上强制触发回填。
	LagThreshold time.Duration

	// SleepBetweenSlices 在繁忙 DB 上给遍历节流。零 = 不暂停。对应独立回填命令
	// 面向运维的那个标志。
	SleepBetweenSlices time.Duration
}

// Hook 是迁移期入口。它检查 task_usage_hourly_rollup_state.watermark_at 是否落后最新
// task_usage 事件超过滞后阈值，若是则跑一次幂等的 task_usage_hourly 月度切片回填。
// 遍历完成（或无需回填）后，把水位打为 `now() - 5 minutes`，与独立回填命令的
// stampWatermark 步骤一致。
//
// 以下情况 hook 不会失败：
//
//   - task_usage 为空（全新数据库没有历史可回填——打上水位即可让迁移 103 的防护
//     接受空状态）。
//
//   - rollup 状态表缺失（某种异常顺序下 hook 跑在迁移 101 之前——视为无事可做，
//     迁移循环随后会建表）。
//
// 当 rollup 遍历本身失败时它会返回错误：这种情况与独立回填失败相同，迁移运行必须
// 中止，让运维在迁移 103 删除旧版日级 rollup 之前排查。
//
// hook 在自己会话的连接上获取 advisory lock 4246，避免与 migrate 循环在另一条连接
// 上的会话级 migrationAdvisoryLockKey 冲突。
func Hook(ctx context.Context, pool *pgxpool.Pool, opts HookOptions) (Result, error) {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	threshold := opts.LagThreshold
	if threshold <= 0 {
		threshold = MaxLagThreshold
	}

	// 第 1 步：廉价的预检查——rollup 状态表还没建时本 hook 无事可做
	//（迁移 101/102 会建表，而迁移循环只有在它们应用之后才会走到 103）。
	stateExists, err := rollupStateExists(ctx, pool)
	if err != nil {
		return Result{}, fmt.Errorf("check rollup state existence: %w", err)
	}
	if !stateExists {
		log.Info("task_usage hourly rollup hook: rollup state tables not present, skipping",
			"reason", "migrations 101/102 not yet applied")
		return Result{Skipped: "rollup_state_missing"}, nil
	}

	// 第 2 步：在连接池上读取 task_usage 范围与当前水位。这些读取无需加锁——它们
	// 只用于判断是否该跑受锁保护的遍历，而遍历本身幂等，即便水位在我们之前被推进
	// 也无妨。
	usageRange, err := loadUsageRange(ctx, pool)
	if err != nil {
		return Result{}, fmt.Errorf("load task_usage range: %w", err)
	}
	watermark, err := loadWatermark(ctx, pool)
	if err != nil {
		return Result{}, fmt.Errorf("load rollup watermark: %w", err)
	}

	if !usageRange.HasRows {
		// 空库。打上水位，让迁移 103 的防护接受全新升级里"无历史"的路径，也让
		// rollup worker 从该位置向前起步。用数据库自身时钟，避免时钟偏斜的应用进程
		// 把水位打到 DB 的未来。
		if err := stampWatermark(ctx, pool); err != nil {
			return Result{}, err
		}
		log.Info("task_usage hourly rollup hook: task_usage empty, watermark stamped from db now()")
		return Result{Skipped: "task_usage_empty", WatermarkStamped: true}, nil
	}

	if !watermark.Valid {
		// 防御性检查——schema 保证该行存在且默认 1970-01-01，因此这里值无效
		// 意味着有人直接改过这行。
		return Result{}, errors.New("task_usage_hourly_rollup_state row is missing or watermark is NULL; manual intervention required before migration 103")
	}

	// 滞后相对 DB 的 max_event（本身已是 DB 时间）计算；与 DB 时间的水位比较可
	// 避免任何应用/DB 时钟偏差。
	maxEvent := usageRange.MaxEvent
	lag := maxEvent.Sub(watermark.Time)
	if lag <= threshold {
		log.Info("task_usage hourly rollup hook: watermark already current, skipping backfill",
			"watermark_at", watermark.Time.UTC().Format(time.RFC3339),
			"max_event", maxEvent.UTC().Format(time.RFC3339),
			"lag", lag.String(),
			"threshold", threshold.String())
		// 用 DB now() 重打一次水位，把值与 cron 上限拉齐；迁移 103 的滞后防护无论
		// 如何都会通过，但重打让 hook 之后的状态与独立回填命令保持一致。
		if err := stampWatermark(ctx, pool); err != nil {
			return Result{}, err
		}
		return Result{Skipped: "watermark_within_threshold", WatermarkStamped: true}, nil
	}

	log.Info("task_usage hourly rollup hook: backfilling under advisory lock",
		"watermark_at", watermark.Time.UTC().Format(time.RFC3339),
		"max_event", maxEvent.UTC().Format(time.RFC3339),
		"lag", lag.String(),
		"threshold", threshold.String())

	// 第 3 步：通过 advisory lock 4246 与 SQL cron 条目 / 独立回填 / scheduler
	// handler 串行化。这里使用专用且固定在会话上的连接，因为 pg advisory lock
	// 是会话级的。
	lockConn, err := pool.Acquire(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("acquire advisory-lock connection: %w", err)
	}
	defer lockConn.Release()

	if _, err := lockConn.Exec(ctx, `SELECT pg_advisory_lock($1)`, AdvisoryLockKey); err != nil {
		return Result{}, fmt.Errorf("acquire advisory lock %d: %w", AdvisoryLockKey, err)
	}
	// 解锁用全新 context，避免被取消的 ctx 跳过释放。随后释放连接本就会结束会话，
	// 但显式解锁可以立刻归还锁。
	defer func() {
		_, _ = lockConn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, AdvisoryLockKey)
	}()

	from := monthFloor(usageRange.MinEvent)
	end := monthFloor(maxEvent).AddDate(0, 1, 0)
	res := Result{From: from, To: end}

	cursor := from
	for cursor.Before(end) {
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		default:
		}

		next := cursor.AddDate(0, 1, 0)
		var rows int64
		err := lockConn.QueryRow(
			ctx,
			`SELECT rollup_task_usage_hourly_window($1::timestamptz, $2::timestamptz)`,
			cursor, next,
		).Scan(&rows)
		if err != nil {
			return res, fmt.Errorf("rollup slice %s..%s: %w",
				cursor.Format(time.RFC3339), next.Format(time.RFC3339), err)
		}
		res.SlicesProcessed++
		res.RowsTouched += rows
		log.Info("task_usage hourly rollup hook: slice complete",
			"from", cursor.Format(time.RFC3339),
			"to", next.Format(time.RFC3339),
			"rows_touched", rows)
		cursor = next
		if opts.SleepBetweenSlices > 0 && cursor.Before(end) {
			select {
			case <-time.After(opts.SleepBetweenSlices):
			case <-ctx.Done():
				return res, ctx.Err()
			}
		}
	}

	if err := stampWatermarkOnConn(ctx, lockConn.Conn()); err != nil {
		return res, err
	}
	res.WatermarkStamped = true

	log.Info("task_usage hourly rollup hook: complete",
		"slices", res.SlicesProcessed,
		"total_rows_touched", res.RowsTouched,
		"watermark_source", "db_now")
	return res, nil
}

// usageRange 记录 task_usage 表里事件时间的边界；HasRows 为 false 表示空表。
type usageRange struct {
	HasRows  bool
	MinEvent time.Time
	MaxEvent time.Time
}

// loadUsageRange 读取 task_usage 的最小创建时间与最大"变更时间"，并标记是否有行。
func loadUsageRange(ctx context.Context, pool *pgxpool.Pool) (usageRange, error) {
	var minTS, maxTS pgtype.Timestamptz
	// COALESCE(updated_at, created_at) 与迁移 103 防护用的表达式一致，保证滞后比较
	// 与防护下一步将要检查的值口径相同。
	err := pool.QueryRow(ctx, `
		SELECT MIN(created_at), MAX(COALESCE(updated_at, created_at))
		  FROM task_usage
	`).Scan(&minTS, &maxTS)
	if err != nil {
		return usageRange{}, err
	}
	if !minTS.Valid || !maxTS.Valid {
		return usageRange{HasRows: false}, nil
	}
	return usageRange{
		HasRows:  true,
		MinEvent: minTS.Time.UTC(),
		MaxEvent: maxTS.Time.UTC(),
	}, nil
}

// loadWatermark 读取 task_usage_hourly_rollup_state 里 id=1 行的水位；行不存在时
// 返回一个无效值（而非报错），由调用方统一走"手工介入"的防御逻辑。
func loadWatermark(ctx context.Context, pool *pgxpool.Pool) (pgtype.Timestamptz, error) {
	var watermark pgtype.Timestamptz
	err := pool.QueryRow(ctx, `
		SELECT watermark_at FROM task_usage_hourly_rollup_state WHERE id = 1
	`).Scan(&watermark)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pgtype.Timestamptz{}, nil
		}
		return pgtype.Timestamptz{}, err
	}
	return watermark, nil
}

// rollupStateExists 检查 rollup 状态表是否已在库中创建（迁移 101/102 才会建表）。
func rollupStateExists(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	var exists bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			 WHERE table_schema = 'public'
			   AND table_name = 'task_usage_hourly_rollup_state'
		)
	`).Scan(&exists)
	return exists, err
}

// stampWatermark 用 PostgreSQL 的时钟（而非应用进程时钟）把小时级 rollup 状态水位
// 移到 `now() - 5 min`。这与 cron 条目的上界一致，并且——关键点——保证水位不会因为
// 容器时钟漂移被打到 DB 的未来。（MUL-2957 review: 见张大彪 的 blocker #3。）
func stampWatermark(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `
		UPDATE task_usage_hourly_rollup_state
		   SET watermark_at = now() - INTERVAL '5 minutes'
		 WHERE id = 1
	`)
	if err != nil {
		return fmt.Errorf("stamp watermark: %w", err)
	}
	return nil
}

// stampWatermarkOnConn 与 stampWatermark 相同，但复用调用方已持有（并已加锁）的连接，
// 保证打水位与最后的回填操作在同一个会话里完成。
func stampWatermarkOnConn(ctx context.Context, conn *pgx.Conn) error {
	_, err := conn.Exec(ctx, `
		UPDATE task_usage_hourly_rollup_state
		   SET watermark_at = now() - INTERVAL '5 minutes'
		 WHERE id = 1
	`)
	if err != nil {
		return fmt.Errorf("stamp watermark: %w", err)
	}
	return nil
}

// monthFloor 把时间归一化到其所在月的 1 号 00:00（UTC），用于切分月度回填窗口。
func monthFloor(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}
