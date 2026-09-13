// backfill_delegated_failure_settled —— 收尾"委派失败恢复"的历史评论。
//
// 在 comment.recovery_settled_at 字段出现之前，一部分委派失败恢复评论其实已被
// 终态投递回执关掉了；本命令给这些老评论补上"已收尾"标记，让 outbox 扫描器不再
// 反复把它们捞出来。
//
// 建议在滚动部署里"所有后端都带上写该标记的代码"之后再跑。它跑完之前，outbox
// 扫描的行为与以前完全一样——未打标记的历史仍会被扫描，任何恢复都不会丢——所以
// 这是一次"性能恢复"步骤而非正确性步骤，错峰跑、分几次跑都安全。
//
// # 断点续跑
//
// 遍历以 (created_at, id) 为键，与 idx_comment_delegated_failure_unsettled 索引
// 对齐；水位只推进"被检视过的候选"，而不是"被打标记的候选"。这个差别正是有界
// 运行可续跑的原因：那些本命令刻意留着 pending 的行（恢复确实还开着）会永远停在
// 索引最前面，若只记住"打过标记的行"，重启会永远卡在同一前缀里、够不到后面可收
// 尾的历史。
//
// 传 --checkpoint-file 可在每个已提交批次后持久化水位并自动从那里续跑；或者读取
// 本命令在每条退出路径上打印的 resume_after_* 字段，用 --after-created-at /
// --after-id 传回去。两者都不给，重跑就从起点开始——正确，但会重复做功。
//
// 会话级咨询锁保证不会有两个运维同时开跑。这只是本手工命令的运维互斥——周期
// 清扫器自己的单实例闸门是另一回事，不由这把锁管。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/delegatedrecoverybackfill"
	"github.com/multica-ai/multica/server/internal/logger"
)

// advisoryLockName 是本次回填用的会话咨询锁名字（防止两名运维同时开跑）。
const advisoryLockName = "delegated_failure_recovery_settled_backfill"

// checkpoint 是 keyset 水位在磁盘上的存储形态。
type checkpoint struct {
	AfterCreatedAt time.Time `json:"after_created_at"`
	AfterID        string    `json:"after_id"`
}

// readCheckpoint 从文件读水位；文件不存在返回 (nil,nil)（表示从头开始）。
func readCheckpoint(path string) (*delegatedrecoverybackfill.Cursor, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read checkpoint %s: %w", path, err)
	}
	var cp checkpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		return nil, fmt.Errorf("parse checkpoint %s: %w", path, err)
	}
	if cp.AfterID == "" {
		return nil, nil
	}
	return &delegatedrecoverybackfill.Cursor{CreatedAt: cp.AfterCreatedAt, ID: cp.AfterID}, nil
}

// writeCheckpoint 原子地替换水位文件——中断的写入不会留下半截水位、导致续跑从
// 错误的位置开始。
func writeCheckpoint(path string, cursor *delegatedrecoverybackfill.Cursor) error {
	raw, err := json.Marshal(checkpoint{AfterCreatedAt: cursor.CreatedAt, AfterID: cursor.ID})
	if err != nil {
		return fmt.Errorf("encode checkpoint: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".checkpoint-*")
	if err != nil {
		return fmt.Errorf("create checkpoint temp file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("write checkpoint: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close checkpoint: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("install checkpoint %s: %w", path, err)
	}
	return nil
}

// resolveStart 决定从哪个水位开始：显式给的 --after-created-at/--after-id 优先于
// 文件里的 checkpoint（那是运维覆盖不再信任的水位的方式）；两个参数必须同时给。
func resolveStart(checkpointFile, afterCreatedAt, afterID string) (*delegatedrecoverybackfill.Cursor, error) {
	if (afterCreatedAt == "") != (afterID == "") {
		return nil, fmt.Errorf("--after-created-at and --after-id must be given together")
	}
	if afterID != "" {
		createdAt, err := time.Parse(time.RFC3339Nano, afterCreatedAt)
		if err != nil {
			return nil, fmt.Errorf("--after-created-at must be RFC3339: %w", err)
		}
		return &delegatedrecoverybackfill.Cursor{CreatedAt: createdAt, ID: afterID}, nil
	}
	if checkpointFile == "" {
		return nil, nil
	}
	return readCheckpoint(checkpointFile)
}

func main() {
	logger.Init()
	if err := run(); err != nil {
		slog.Error("delegated failure recovery settled backfill failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	batchSize := flag.Int("batch-size", delegatedrecoverybackfill.DefaultBatchSize, "maximum candidate recovery comments examined per statement")
	delay := flag.Duration("sleep-between-batches", 100*time.Millisecond, "delay between committed batches")
	maxBatches := flag.Int("max-batches", 0, "stop after N batches (0 = walk the whole history)")
	checkpointFile := flag.String("checkpoint-file", "", "persist the keyset watermark here after every batch and resume from it")
	afterCreatedAt := flag.String("after-created-at", "", "resume after this comment created_at (RFC3339); requires --after-id")
	afterID := flag.String("after-id", "", "resume after this comment id; requires --after-created-at")
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
	cursor, err := resolveStart(*checkpointFile, *afterCreatedAt, *afterID)
	if err != nil {
		return err
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

	// 会话咨询锁：防止两个运维同时遍历。
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

	remaining, err := delegatedrecoverybackfill.CountRemaining(ctx, pool, "")
	if err != nil {
		return err
	}
	slog.Info("delegated failure recovery settled backfill started",
		"unsettled", remaining, "batch_size", *batchSize, "delay", delay.String(),
		"resumed_from_id", cursorID(cursor))

	var scanned, settled int64
	// 每条退出路径（含 SIGTERM、--max-batches）都打印水位，让没配
	// --checkpoint-file 的运行也能从日志续跑。
	defer func() {
		slog.Info("delegated failure recovery settled backfill stopped",
			"scanned", scanned, "settled", settled,
			"resume_after_created_at", cursorCreatedAt(cursor),
			"resume_after_id", cursorID(cursor))
	}()

	for batch := 1; *maxBatches == 0 || batch <= *maxBatches; batch++ {
		result, err := delegatedrecoverybackfill.Batch(ctx, pool, delegatedrecoverybackfill.Options{
			BatchSize: *batchSize,
			After:     cursor,
		})
		if err != nil {
			return err
		}
		if result.Scanned == 0 {
			break
		}
		if result.Last == nil {
			return fmt.Errorf("backfill batch examined %d candidates without a keyset watermark", result.Scanned)
		}
		scanned += result.Scanned
		settled += result.Settled
		cursor = result.Last
		if *checkpointFile != "" {
			if err := writeCheckpoint(*checkpointFile, cursor); err != nil {
				return err
			}
		}
		slog.Info("delegated failure recovery settled batch committed",
			"batch", batch, "scanned", result.Scanned, "settled", result.Settled,
			"scanned_total", scanned, "settled_total", settled, "last_id", cursor.ID)
		if *delay > 0 {
			select {
			case <-time.After(*delay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	remaining, err = delegatedrecoverybackfill.CountRemaining(ctx, pool, "")
	if err != nil {
		return err
	}
	// 剩下来的是"真正还开着"的积压，加上被一条可逆排除规则扣住的量——这正是该
	// 索引本来该装的东西。
	slog.Info("delegated failure recovery settled backfill finished",
		"scanned", scanned, "settled", settled, "unsettled_remaining", remaining)
	return nil
}

// cursorID 空安全地取游标 ID（用于打日志）。
func cursorID(c *delegatedrecoverybackfill.Cursor) string {
	if c == nil {
		return ""
	}
	return c.ID
}

// cursorCreatedAt 空安全地把游标时间格式化成 RFC3339Nano（用于打日志）。
func cursorCreatedAt(c *delegatedrecoverybackfill.Cursor) string {
	if c == nil {
		return ""
	}
	return c.CreatedAt.Format(time.RFC3339Nano)
}
