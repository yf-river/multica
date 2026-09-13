// backfill_issue_last_activity —— 重建 issue.last_activity_at 字段。
//
// 从既有的 updated_at 值回填 last_activity_at。要在滚动部署中"所有写 Issue 的
// 后端都已升级"之后再跑——老版本写入方不维护这个新列。本命令刻意与迁移、启动
// 流程分开：巨大的 issue 表绝不会阻塞一次部署。
//
// 每个批次独立提交。SIGINT/SIGTERM、数据库错误或 --max-batches 都能停下遍历，
// 之后再跑一次会从"last_activity_at 仍为 NULL"的行继续。每一趟用 id keyset
// 水位前进，而不是重扫已完成的区间。会话级咨询锁串行化多个运维，同时 SKIP
// LOCKED 避免在无关的热行后面干等；后续几趟会绕回头，把被锁而跳过的行补上。
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

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/issueactivitybackfill"
	"github.com/multica-ai/multica/server/internal/logger"
)

const (
	// advisoryLockName 是会话咨询锁的名字（防止多个运维同时回填）。
	advisoryLockName = "issue_last_activity_backfill"
	// defaultMaxStalledPasses 是默认的"连续空转趟数上限"。
	defaultMaxStalledPasses = 10
)

// stalledPassGuard 监视连续"没有进展"的遍历趟数：超过上限就报错中止——通常意味
// 着某些行锁被长时间持有，需要释放后重跑或调大 --max-stalled-passes。
type stalledPassGuard struct {
	max         int
	consecutive int
}

// observe 记录一趟的结果：只要这趟动了行就清零连续计数，否则累加；达到上限即
// 返回带剩余量的错误提示。
func (g *stalledPassGuard) observe(passRows, remaining int64) error {
	if passRows > 0 {
		g.consecutive = 0
		return nil
	}
	g.consecutive++
	if g.max > 0 && g.consecutive >= g.max {
		return fmt.Errorf(
			"issue last-activity backfill stalled: %d consecutive passes made no progress with %d rows remaining; release long-held row locks and rerun, or increase --max-stalled-passes",
			g.consecutive,
			remaining,
		)
	}
	return nil
}

func main() {
	logger.Init()
	if err := run(); err != nil {
		slog.Error("issue last-activity backfill failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	batchSize := flag.Int("batch-size", issueactivitybackfill.DefaultBatchSize, "maximum issue rows updated per transaction")
	delay := flag.Duration("sleep-between-batches", 100*time.Millisecond, "delay between committed batches")
	maxBatches := flag.Int("max-batches", 0, "stop after N batches (0 = finish all remaining rows)")
	maxStalledPasses := flag.Int("max-stalled-passes", defaultMaxStalledPasses, "fail after N consecutive no-progress passes (0 = disable the guard)")
	flag.Parse()
	if *batchSize < 1 {
		return fmt.Errorf("--batch-size must be at least 1")
	}
	if *delay < 0 {
		return fmt.Errorf("--sleep-between-batches must not be negative")
	}
	if *maxBatches < 0 {
		return fmt.Errorf("--max-batches must not be negative")
	}
	if *maxStalledPasses < 0 {
		return fmt.Errorf("--max-stalled-passes must not be negative")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}

	// 会话咨询锁：一次只允许一个回填进程。
	lockConn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire advisory-lock connection: %w", err)
	}
	defer lockConn.Release()
	if _, err := lockConn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1, 0))`, advisoryLockName); err != nil {
		return fmt.Errorf("acquire advisory lock: %w", err)
	}
	defer func() {
		_, _ = lockConn.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, advisoryLockName)
	}()

	remaining, err := issueactivitybackfill.CountRemaining(ctx, pool, "")
	if err != nil {
		return err
	}
	slog.Info("issue last-activity backfill started", "remaining", remaining, "batch_size", *batchSize, "delay", delay.String())

	var total int64
	var passRows int64
	var afterID *string
	pass := 1
	stallGuard := stalledPassGuard{max: *maxStalledPasses}
	for batch := 1; *maxBatches == 0 || batch <= *maxBatches; batch++ {
		result, err := issueactivitybackfill.Batch(ctx, pool, issueactivitybackfill.Options{
			BatchSize: *batchSize,
			AfterID:   afterID,
		})
		if err != nil {
			return err
		}
		if result.Rows > 0 && result.LastID == "" {
			return fmt.Errorf("issue last-activity backfill batch returned %d rows without a keyset watermark", result.Rows)
		}
		total += result.Rows
		passRows += result.Rows
		if result.Rows > 0 {
			slog.Info("issue last-activity batch committed", "batch", batch, "pass", pass, "rows", result.Rows, "total", total, "last_id", result.LastID)
			next := result.LastID
			afterID = &next
		}
		// 短/空的一批 keyset 数据表示"这趟到头了"，但不代表全部完成：SKIP LOCKED
		// 可能在水位之下留了热行。每趟结束数一次剩余，然后绕回起点继续，直到那些
		// 行被解锁。
		if result.Rows < int64(*batchSize) {
			remaining, err = issueactivitybackfill.CountRemaining(ctx, pool, "")
			if err != nil {
				return err
			}
			if remaining == 0 {
				slog.Info("issue last-activity backfill complete", "rows_backfilled", total, "remaining", 0)
				return nil
			}
			if err := stallGuard.observe(passRows, remaining); err != nil {
				return err
			}
			pass++
			afterID = nil
			slog.Info("issue last-activity pass complete; rows remain locked or pending", "pass", pass-1, "rows", passRows, "remaining", remaining, "stalled_passes", stallGuard.consecutive)
			passRows = 0
		}
		if *delay > 0 {
			select {
			case <-time.After(*delay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	remaining, err = issueactivitybackfill.CountRemaining(ctx, pool, "")
	if err != nil {
		return err
	}
	slog.Info("issue last-activity backfill stopped at max batches", "rows_backfilled", total, "remaining", remaining)
	return nil
}
