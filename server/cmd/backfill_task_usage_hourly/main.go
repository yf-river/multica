// backfill_task_usage_hourly —— 用历史 task_usage 行给统一小时聚合表
// （task_usage_hourly）播种。在小时流水线迁移上线之后、注册
// rollup_task_usage_hourly() 的 pg_cron job 之前，跑一次本命令。
//
// # 自托管升级顺序 —— 迁移 100–104 是一组，但绝不能一次 `make migrate-up` 全上
//
//  1. 先应用 101+102（建 task_usage_hourly + 装触发器）；
//  2. 再跑本回填，把历史桶播种好；
//  3. 然后才应用 103+104（删遗留的日汇总 + runtime.timezone），并注册 pg_cron。
//
// 若在回填之前一口气 `migrate-up` 到 103/104：遗留的日流水线没了，而
// task_usage_hourly 里只有 102 之后触发器写出来的桶——仪表盘会一直显示空历史，
// 直到回填 + cron 追上（考虑到每 tick 1 天的上限，在有数年数据的库上要几十到
// 几百个 tick）。
//
// 与 backfill_task_usage_dashboard_daily 的思路一致：按月切片走 task_usage 的
// 时间范围，调用 cron 路径用的同一个幂等窗口原语；然后盖上 rollup-state 水位，
// 让回填后的第一个 cron tick 不会把历史再处理一遍。
//
// 重复运行是安全的——rollup_task_usage_hourly_window 幂等（对每个脏键从原始数据
// 重算并 REPLACE 那个桶），因此没跑完的回填可以直接续跑，无需先 TRUNCATE 掉
// task_usage_hourly。
//
// 读压力：每片会扫 task_usage / agent_task_queue / agent / issue。在有数年历史的
// 库上是持续的高负载。在忙碌的生产库上用 --sleep-between-slices 限速，跑全量历史
// 前要和 DBA 协调维护窗口。
//
// 运维提示：本命令不调用 prune_task_usage_hourly_dirty。回填期间脏队列是空的
// （触发器只对未来的写入生效），所以在 rollup worker 跑一段时间之前没什么可清。
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/logger"
)

func main() {
	logger.Init()
	if err := run(); err != nil {
		slog.Error("backfill failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		dryRun       = flag.Bool("dry-run", false, "log slices that would be processed without touching task_usage_hourly")
		monthsBack   = flag.Int("months-back", 0, "limit backfill to the last N months (0 = all available history)")
		forcePartial = flag.Bool("force-partial", false, "acknowledge that --months-back permanently abandons buckets older than the cutoff (the watermark still advances past them)")
		sleep        = flag.Duration("sleep-between-slices", 0, "pause this long between monthly slices to throttle source-table read pressure on a busy DB (e.g. 2s)")
	)
	flag.Parse()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}

	// SIGINT/SIGTERM 取消 ctx，让正在跑的切片干净停掉——每个切片都在自己的事务里
	//（窗口函数），Postgres 会把被打断的那个回滚，幂等设计让之后的运行能接着停下的
	// 位置续跑。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}

	// 用咨询锁 4246 与 cron 回填及其它回填运行串行化——cron 条目也是拿同一把锁
	// 做 pg_try_advisory_lock。本回填持有锁期间，cron tick 会空转而不是在
	// task_usage_hourly 的行锁上赛跑；第二个并发回填会在这里阻塞直到本回填结束。
	// 锁在整个运行期间由一条专用会话连接持有。
	lockConn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire advisory-lock connection: %w", err)
	}
	defer lockConn.Release()
	if _, err := lockConn.Exec(ctx, `SELECT pg_advisory_lock(4246)`); err != nil {
		return fmt.Errorf("acquire advisory lock 4246: %w", err)
	}
	defer func() {
		// 用全新的 context 解锁，避免被取消的 ctx（SIGINT）跳过释放。释放连接本
		// 来也会结束会话，但显式解锁能立刻释放锁。
		_, _ = lockConn.Exec(context.Background(), `SELECT pg_advisory_unlock(4246)`)
	}()

	var minTS, maxTS pgtype.Timestamptz
	if err := pool.QueryRow(ctx, `SELECT MIN(created_at), MAX(created_at) FROM task_usage`).Scan(&minTS, &maxTS); err != nil {
		return fmt.Errorf("scan task_usage time range: %w", err)
	}
	if !minTS.Valid {
		slog.Info("task_usage is empty; nothing to backfill")
		if *dryRun {
			return nil
		}
		return stampWatermark(ctx, pool)
	}

	from := monthFloor(minTS.Time.UTC())
	end := monthFloor(maxTS.Time.UTC()).AddDate(0, 1, 0)

	if *monthsBack > 0 {
		cutoff := monthFloor(time.Now().UTC()).AddDate(0, -(*monthsBack), 0)
		// 局部回填仍会把水位盖在 now()-5min，早于 cutoff 的桶等于被永久放弃：
		// cron worker 永远不会回头看那么远。这份数据丢失必须是运维的显式决定——
		// 因此要求带 --force-partial 才放行。
		if cutoff.After(from) {
			if !*forcePartial {
				return fmt.Errorf("--months-back=%d would skip buckets before %s (oldest available %s) and the watermark would still advance past them; re-run with --force-partial to accept this, or omit --months-back for a full backfill",
					*monthsBack, cutoff.Format(time.RFC3339), minTS.Time.UTC().Format(time.RFC3339))
			}
			from = cutoff
			slog.Warn("partial backfill: --months-back limits coverage; older buckets will be left empty and the watermark will still advance past them",
				"months_back", *monthsBack, "effective_from", from.Format(time.RFC3339),
				"oldest_available", minTS.Time.UTC().Format(time.RFC3339))
		}
	}

	slog.Info("backfill range", "from", from.Format(time.RFC3339), "to", end.Format(time.RFC3339), "dry_run", *dryRun, "sleep_between_slices", sleep.String())

	cursor := from
	var totalRows int64
	for cursor.Before(end) {
		next := cursor.AddDate(0, 1, 0)
		if *dryRun {
			slog.Info("would roll up slice", "from", cursor.Format(time.RFC3339), "to", next.Format(time.RFC3339))
			cursor = next
			continue
		}
		var rows int64
		err := pool.QueryRow(
			ctx,
			`SELECT rollup_task_usage_hourly_window($1::timestamptz, $2::timestamptz)`,
			cursor, next,
		).Scan(&rows)
		if err != nil {
			return fmt.Errorf("rollup slice %s..%s: %w", cursor.Format(time.RFC3339), next.Format(time.RFC3339), err)
		}
		totalRows += rows
		slog.Info("rolled up slice", "from", cursor.Format(time.RFC3339), "to", next.Format(time.RFC3339), "rows_touched", rows)
		cursor = next
		if *sleep > 0 && cursor.Before(end) {
			select {
			case <-time.After(*sleep):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	if *dryRun {
		slog.Info("dry-run complete; watermark left untouched")
		return nil
	}
	// 用全新 context 盖章：若 SIGINT 在切片完成后到达，不能跳过水位 UPDATE——
	// 丢了它会让下一次运行从 epoch 重新开始。与 deferred 的咨询锁解锁同理。
	if err := stampWatermark(context.Background(), pool); err != nil {
		return err
	}
	slog.Info("backfill complete", "total_rows_touched", totalRows)
	return nil
}

// stampWatermark 把小时聚合状态的水位推到 `now() - 5 min`，与 cron 条目的上界
// 一致。下一个定时 tick 因此只会捡回填范围之后的新事件，不会重做回填已做完的活。
func stampWatermark(ctx context.Context, pool *pgxpool.Pool) error {
	tag, err := pool.Exec(ctx, `
		UPDATE task_usage_hourly_rollup_state
		   SET watermark_at = now() - INTERVAL '5 minutes'
		 WHERE id = 1
	`)
	if err != nil {
		return fmt.Errorf("stamp watermark: %w", err)
	}
	if tag.RowsAffected() == 0 {
		slog.Warn("no rollup state row to stamp; was the task_usage_hourly schema migration applied?")
		return nil
	}
	fmt.Println("watermark stamped to now() - 5 minutes")
	return nil
}

// monthFloor 返回某时间所在月的第一天 0 点（UTC），用于按月切片。
func monthFloor(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}
