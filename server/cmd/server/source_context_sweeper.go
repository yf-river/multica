// source_context_sweeper.go
//
// 职责：后台清扫"来源上下文（source context）"相关的孤儿对象与超时快照。
// 来源上下文是 AI 运行时要参考的代码/文档快照（存在对象存储里）。两类残留
// 需要回收：一类是"对象意图（object intent）"——快照捕获失败后留下的待处理
// 记录；一类是"被遗弃的捕获"——超过 30 天保留期仍没走完流程的快照。本文件
// 驱动 TaskService 上的两个清理方法，并刻意让清扫跑在独立 goroutine 上，
// 避免对象存储卡顿时拖累其他更紧急的后台任务（详见 runSourceContextSweeper
// 的注释）。
package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/multica-ai/multica/server/internal/service"
)

const (
	// sourceContextSweepInterval paces snapshot cleanup (MUL-6555). Nothing
	// here is latency-critical: an orphaned object intent only becomes eligible
	// one hour after its capture failed and carries a five-minute retry
	// backoff, and an abandoned capture waits out a 30-day retention window.
	// A minute keeps the drain rate for a fresh backlog close to what the old
	// placement in the 30s runtime tick gave, at two cheap queries per round.
	//
	// 中文说明：这个周期控制快照清理的节奏（MUL-6555）。这里没有延迟敏感的
	// 逻辑：孤儿对象意图要等捕获失败一小时才进入可清理名单、且自带五分钟
	// 重试退避；被遗弃的捕获要走完 30 天保留期。改成 1 分钟一轮，既能保持对
	// 积压的消化速度接近原来放在 30 秒 runtime tick 里的水平，每轮又只要
	// 两次廉价查询。
	sourceContextSweepInterval = time.Minute
	// sourceContextSweepBudget bounds one round below the interval so a slow
	// object store delays cleanup instead of stacking rounds. Per-object
	// deadlines live in the service; this is the whole-round backstop.
	//
	// 中文说明：把单轮预算压到小于清扫周期：对象存储再慢也只是让"本轮超时放弃"，
	// 而不会让多轮清理叠加积压。单个对象的截止时间在 service 里控制，这里是
	// 整轮的总兜底。
	sourceContextSweepBudget = 45 * time.Second
	// sourceContextCleanupBatchSize bounds delete attempts per round.
	// 中文说明：每轮最多尝试删除的对象数量，避免一轮删太多把对象存储打满。
	sourceContextCleanupBatchSize = 50
)

// sourceContextCleaner 是 TaskService 中本清扫器需要用到的接口切片。
// 定义成接口是为了在测试里可以注入一个"卡住的对象存储"来验证单轮预算
// （sourceContextSweepBudget）是否真的能兜住超时。
type sourceContextCleaner interface {
	CleanupSourceContextObjectIntents(ctx context.Context, limit int) (int, error)
	CleanupAbandonedSourceContexts(ctx context.Context, limit int32) (int, error)
}

// 编译期断言：TaskService 确实实现了本文件定义的接口。
var _ sourceContextCleaner = (*service.TaskService)(nil)

// runSourceContextSweeper 周期回收两类残留：从未提交成功的快照对象，以及
// 重试窗口已过期的捕获。
//
// 它刻意跑在独立 goroutine 而不是塞进 runtime 的 tick 里：清扫的两个阶段都
// 要访问对象存储，如果那个端点卡住或被限流，绝不能拖累"标记 runtime 离线、
// 回收孤儿任务、重放委派失败恢复"这些阶段——正是它们在守护者进程死掉约
// 180 秒内把看板状态拉回真实。
func runSourceContextSweeper(ctx context.Context, cleaner sourceContextCleaner) {
	ticker := time.NewTicker(sourceContextSweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweepSourceContextsWithBudget(ctx, cleaner, sourceContextSweepBudget)
		}
	}
}

// sweepSourceContextsWithBudget 执行一轮清扫：给整轮套上 budget 超时上限，
// 依次清对象意图与被遗弃的捕获，各自的清理结果（成功条数/错误）记日志。
// 两段都失败也不致命，等下一轮再试。
func sweepSourceContextsWithBudget(ctx context.Context, cleaner sourceContextCleaner, budget time.Duration) {
	sweepCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	if cleaned, err := cleaner.CleanupSourceContextObjectIntents(sweepCtx, sourceContextCleanupBatchSize); err != nil {
		slog.Warn("source context object intent cleanup failed", "error", err)
	} else if cleaned > 0 {
		slog.Info("source context object intent cleanup completed", "count", cleaned)
	}
	if cleaned, err := cleaner.CleanupAbandonedSourceContexts(sweepCtx, sourceContextCleanupBatchSize); err != nil {
		slog.Warn("source context cleanup sweeper failed", "error", err)
	} else if cleaned > 0 {
		slog.Info("source context cleanup sweeper removed abandoned captures", "count", cleaned)
	}
}
