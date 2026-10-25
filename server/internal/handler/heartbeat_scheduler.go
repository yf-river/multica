// 职责定位：heartbeat（心跳）处理器收到的「本 runtime 还活着，更新它的 last_seen_at」
// 请求，最终要怎么落到数据库，由本文件的 HeartbeatScheduler 决定。它有两种实现：
//
//   - PassthroughHeartbeatScheduler：同步直写，保留历史行为——先 TouchAgentRuntimeLastSeen，
//     若更新行数为 0（被 sweeper 抢先标成 offline），再走 MarkAgentRuntimeOnline 兜底翻回
//     online（清扫竞态恢复）。Handler 默认装配它，让单元测试能立刻观察到落库，也保住既有
//     的竞态恢复测试。
//
//   - BatchedHeartbeatScheduler：把 runtime ID 先收进内存队列，每 tick 用一条批量 UPDATE
//     一次性刷新（flush）。生产环境装配它：N 个每 15s 心跳一次的 runtime，每 tick 只需
//     约 1 次数据库事务而不是 N 次。同步路径（状态翻转、从未见过的行）仍走
//     MarkAgentRuntimeOnline 立即落库；只有热点路径「行已是 online、只想撞一下
//     last_seen_at」才走批量。goroutine 装配与关闭时的排空见 cmd/server/main.go。
//
// 两阶段关键时序链（为何用 30s tick 见 DefaultHeartbeatBatchInterval）：批量 flush 把
// last_seen_at 推迟最多一个 tick，必须保证「推迟后的 last_seen_at」仍在
// staleThresholdSeconds 判定为存活的时间窗内，否则在线 runtime 会被误判离线。
package handler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// HeartbeatScheduler 抽象「一次心跳要不要立刻写库、怎么写」。
type HeartbeatScheduler interface {
	// Schedule 在心跳热点路径上被调用——调用方已先做过每行级别的 flush 窗口检查、
	// 判定确实值得写一次数据库。实现方必须保持清扫（sweeper）竞态语义：如果 SELECT
	// 时 rt.Status 是 "online" 但行此刻已被改成 offline，调度器最终必须把它翻回 online
	// （同步实现立即翻；批量实现会推迟到该 runtime 的下一次心跳——那时会看到
	// status="offline" 而走 recordHeartbeat 里的同步分支，同样能自愈）。
	Schedule(ctx context.Context, rt db.AgentRuntime) error
}

// PassthroughHeartbeatScheduler 是同步的、保持历史行为的调度器。它作为
// handler.New 的默认装配，让测试能立刻观察到数据库写入；也作为
// BatchedHeartbeatScheduler 内部对「必须在返回前落库」的场景（offline→online 翻转、
// 从未见过的 runtime）的同步兜底。
type PassthroughHeartbeatScheduler struct {
	RecoveryNotifier RuntimeRecoveryNotifier
	queries          *db.Queries
}

// NewPassthroughHeartbeatScheduler 构造同步直写版心跳调度器。
func NewPassthroughHeartbeatScheduler(queries *db.Queries) *PassthroughHeartbeatScheduler {
	return &PassthroughHeartbeatScheduler{queries: queries}
}

// Schedule 同步把这次心跳写入数据库。行还是 online 时优先走轻量的 last_seen_at 撞针
// （TouchAgentRuntimeLastSeen）；若它没命中任何行，说明清扫器在我们 SELECT 之后、本
// UPDATE 之前把行翻成了 offline，落下去 MarkAgentRuntimeOnline 把行翻回来。
func (p *PassthroughHeartbeatScheduler) Schedule(ctx context.Context, rt db.AgentRuntime) error {
	return p.scheduleWithRecovery(ctx, rt, p.RecoveryNotifier)
}

func (p *PassthroughHeartbeatScheduler) scheduleWithRecovery(ctx context.Context, rt db.AgentRuntime, notifier RuntimeRecoveryNotifier) error {
	if rt.Status == "online" && rt.LastSeenAt.Valid {
		rows, err := p.queries.TouchAgentRuntimeLastSeen(ctx, rt.ID)
		if err != nil {
			return err
		}
		if rows > 0 {
			return nil
		}
		// 清扫器在我们 SELECT 与这次 UPDATE 之间把 runtime 抢标成了 offline，
		// 落到 MarkAgentRuntimeOnline 把它翻回 online。
	}
	flipped, err := p.queries.MarkAgentRuntimeOnlineIfOffline(ctx, rt.ID)
	if err != nil {
		return err
	}
	if flipped > 0 {
		if notifier != nil {
			notifier.NotifyRuntimeRecovered(ctx, uuidToString(rt.WorkspaceID))
		}
		return nil
	}
	_, err = p.queries.MarkAgentRuntimeOnline(ctx, rt.ID)
	return err
}

// BatchedHeartbeatScheduler 把一个 tick 窗口内对同一 runtime 的多次 Schedule 调用
// 合并成一次批量 UPDATE。
//
// 并发模型：
//   - Schedule 先抢一把短锁，把 runtime ID 写进 map（天然去重），然后放锁返回；
//   - Run 是唯一的消费 goroutine：每 tickInterval 把 map 排空成一次批量 UPDATE；
//   - Stop 通知 Run 退出；Run 在退出前会做最后一次排空，保证优雅关停时不丢积压 ID。
//
// 增长边界：pending 以 runtime ID 为键，因此大小有上界——每 tick 每个活跃心跳的
// runtime 至多占一项。数据库持续报错时只记日志、绝不把失败的 ID 重新入队：每个
// runtime 的下一次心跳自然会重新调度；若在硬故障期间反复入队，map 会无限膨胀。
type BatchedHeartbeatScheduler struct {
	RecoveryNotifier RuntimeRecoveryNotifier
	queries          *db.Queries
	fallback         *PassthroughHeartbeatScheduler
	tickInterval     time.Duration

	mu      sync.Mutex
	pending map[pgtype.UUID]struct{}

	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
}

// DefaultHeartbeatBatchInterval 是批量心跳调度器的生产 tick 节奏。取 30s 是为了让
// 承重的时序链 flushInterval + heartbeatInterval + tickInterval < staleThresholdSeconds
// 有余量地成立（60 + 15 + 30 = 105 < 150）。要拉长它，必须同步调大
// staleThresholdSeconds，否则在线的 runtime 会被误判成已死。
const DefaultHeartbeatBatchInterval = 30 * time.Second

// NewBatchedHeartbeatScheduler 构造批量心跳调度器；tickInterval 非正时退回默认值。
func NewBatchedHeartbeatScheduler(queries *db.Queries, tickInterval time.Duration) *BatchedHeartbeatScheduler {
	if tickInterval <= 0 {
		tickInterval = DefaultHeartbeatBatchInterval
	}
	return &BatchedHeartbeatScheduler{
		queries:      queries,
		fallback:     NewPassthroughHeartbeatScheduler(queries),
		tickInterval: tickInterval,
		pending:      make(map[pgtype.UUID]struct{}),
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
	}
}

// Schedule 收下一次心跳。状态翻转（offline→online）与从未见过（LastSeenAt 无值）的行
// 必须在返回前落库，让调用方/后续读能观察到新状态；只有热点路径「行已 online、仅撞
// last_seen_at」才进批量队列。
func (b *BatchedHeartbeatScheduler) Schedule(ctx context.Context, rt db.AgentRuntime) error {
	// 见上方注释：非 online 或没有历史 last_seen 的行直接走同步兜底，不批量。
	if rt.Status != "online" || !rt.LastSeenAt.Valid {
		return b.fallback.scheduleWithRecovery(ctx, rt, b.RecoveryNotifier)
	}
	b.mu.Lock()
	b.pending[rt.ID] = struct{}{}
	b.mu.Unlock()
	return nil
}

// Run 驱动周期性的批量 flush。直到 Stop 被调用且最后一次排空完成后才返回。
// 设计为在 main.go 里以独立 goroutine 启动一次。
func (b *BatchedHeartbeatScheduler) Run(ctx context.Context) {
	defer close(b.doneCh)
	t := time.NewTicker(b.tickInterval)
	defer t.Stop()
	for {
		select {
		case <-b.stopCh:
			// 把积压的队列排空再退出。用一个全新、短时限的 context，避免父 context
			// 已被取消导致最后一次 flush 被一起丢掉。
			drainCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			b.flushOnce(drainCtx)
			cancel()
			return
		case <-ctx.Done():
			drainCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			b.flushOnce(drainCtx)
			cancel()
			return
		case <-t.C:
			b.flushOnce(ctx)
		}
	}
}

// Stop 通知 Run goroutine 排空并退出，并阻塞到最后一次 flush 完成，让调用方能确定性地
// 安排关停顺序。
//
// 纵深防御：Run 退出后 Stop 还会再补一次 flush。这覆盖了「Run 已经从 ctx.Done() 分支
// 返回（例如父 context 在 Stop 之前就被取消）、之后又有一批迟到的 Schedule 写进
// pending map」这种少见情形。
func (b *BatchedHeartbeatScheduler) Stop() {
	b.stopOnce.Do(func() {
		close(b.stopCh)
	})
	<-b.doneCh
	finalCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	b.flushOnce(finalCtx)
	cancel()
}

// FlushNow 暴露给测试：想在不等 tickInterval 的情况下断言 flush 之后的数据库状态。
// 生产代码应该依赖 Run。
func (b *BatchedHeartbeatScheduler) FlushNow(ctx context.Context) {
	b.flushOnce(ctx)
}

// PendingCount 返回当前排队中的不同 runtime ID 个数。暴露给测试与潜在指标。
func (b *BatchedHeartbeatScheduler) PendingCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pending)
}

// flushOnce 把此刻积压的 ID 一次性批量刷新到数据库。先取出并清空 pending，再发批量
// UPDATE——「先拿后清」让排空期间新到的 Schedule 进的是新一批，不会丢也不会重复。
func (b *BatchedHeartbeatScheduler) flushOnce(ctx context.Context) {
	b.mu.Lock()
	if len(b.pending) == 0 {
		b.mu.Unlock()
		return
	}
	ids := make([]pgtype.UUID, 0, len(b.pending))
	for id := range b.pending {
		ids = append(ids, id)
	}
	b.pending = make(map[pgtype.UUID]struct{})
	b.mu.Unlock()

	rows, err := b.queries.TouchAgentRuntimesLastSeenBatch(ctx, ids)
	if err != nil {
		// 持续性故障不重新入队——理由见类型注释（会无限膨胀、下次心跳自然补偿）。
		slog.Warn("heartbeat batch flush failed",
			"scheduled", len(ids), "error", err)
		return
	}
	if int(rows) < len(ids) {
		// 部分 runtime 在「入队」到「flush」之间被竞态翻成了非 online。它们下一次心跳
		// 会看到 status != "online" 而走 recordHeartbeat 的同步 MarkAgentRuntimeOnline
		// 分支，因此这个偏差会在一个心跳周期（约 15s）内自愈。
		slog.Info("heartbeat batch flush: some runtimes raced to offline",
			"scheduled", len(ids), "affected", rows)
	}
}
