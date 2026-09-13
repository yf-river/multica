// worker.go 是 seatcapacity 的「对账 worker」：周期性扫描 seat_capacity_outbox 表里
// 那些「产品侧已承诺、但还没在 Cloud 容量系统落定」的意图(intent)，逐条对账并推进到
// 终态。每条行在被 Cloud 请求前会先被原子认领（lease），这样多副本不会把同一批积压
// 放大成重复的 Cloud 调用。
package seatcapacity

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	// outbox 里的动作类型（意图做什么）。
	ActionReserveInvitation = "reserve_invitation" // 预留一次邀请
	ActionConsumeInvitation = "consume_invitation" // 消耗邀请（正式入座）
	ActionClaimShareJoin    = "claim_share_join"   // 认领共享加入
	ActionConfirm           = "confirm"            // 确认
	ActionRelease           = "release"            // 释放
	ActionReleaseMember     = "release_member"     // 释放成员

	defaultReconcileInterval = 30 * time.Second // 对账周期
	defaultRecoveryGrace     = 5 * time.Minute  // 认领行多久没进展视为需要恢复
	defaultClaimLease        = 5 * time.Minute  // 认领租约时长
	defaultWorkspaceLockWait = 30 * time.Second // 等 workspace 分布式锁的最长时间
	defaultBatchSize         = 100              // 每轮最多处理的意图数
	defaultMaxAttempts       = 10               // 单条意图最多尝试次数
)

// WorkerConfig 配置 worker。零值字段会用默认值补齐。
type WorkerConfig struct {
	ReconcileInterval time.Duration
	BatchSize         int32
	MaxAttempts       int32
	Logger            *slog.Logger
}

// workerQueries 是 worker 需要的全部查询（抽象出来便于测试注入替身）。
type workerQueries interface {
	ClaimNextDueSeatCapacityIntent(context.Context, pgtype.Timestamptz) (db.SeatCapacityOutbox, error)
	DeferClaimedSeatCapacityIntent(context.Context, db.DeferClaimedSeatCapacityIntentParams) (int64, error)
	DeleteClaimedSeatCapacityIntent(context.Context, db.DeleteClaimedSeatCapacityIntentParams) (int64, error)
	ExpireInvitationForCapacityRecovery(context.Context, pgtype.UUID) error
	GetClaimedSeatCapacityIntent(context.Context, db.GetClaimedSeatCapacityIntentParams) (db.SeatCapacityOutbox, error)
	GetInvitation(context.Context, pgtype.UUID) (db.WorkspaceInvitation, error)
	MarkClaimedSeatCapacityIntentDeadLettered(context.Context, db.MarkClaimedSeatCapacityIntentDeadLetteredParams) (int64, error)
	MarkClaimedSeatCapacityIntentFailed(context.Context, db.MarkClaimedSeatCapacityIntentFailedParams) (int64, error)
	TransitionClaimedSeatCapacityIntent(context.Context, db.TransitionClaimedSeatCapacityIntentParams) (int64, error)
}

// Worker 负责「结算」那些持久化的产品侧意图。每条行在发起 Cloud 请求前都会被原子
// 认领，因此多个 API 副本不会放大同一份积压。
type Worker struct {
	queries           workerQueries
	executor          Executor
	reconcileInterval time.Duration
	batchSize         int32
	maxAttempts       int32
	logger            *slog.Logger
	workspaceLocker   WorkspaceLocker
	now               func() time.Time
}

// NewWorker 用 *db.Queries、执行器与分布式 workspace 锁构造 worker。
func NewWorker(queries *db.Queries, executor Executor, locker WorkspaceLocker, cfg WorkerConfig) *Worker {
	worker := newWorker(queries, executor, cfg)
	worker.workspaceLocker = locker
	return worker
}

func newWorker(queries workerQueries, executor Executor, cfg WorkerConfig) *Worker {
	interval := cfg.ReconcileInterval
	if interval <= 0 {
		interval = defaultReconcileInterval
	}
	batch := cfg.BatchSize
	if batch <= 0 {
		batch = defaultBatchSize
	}
	maxAttempts := cfg.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultMaxAttempts
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Worker{
		queries: queries, executor: executor, reconcileInterval: interval,
		batchSize: batch, maxAttempts: maxAttempts, logger: logger,
		now: time.Now,
	}
}

// Enabled 报告 worker 是否可用（依赖的查询/执行器是否就绪）。
func (w *Worker) Enabled() bool {
	return w != nil && w.queries != nil && CanRunWorker(w.executor)
}

// Run 阻塞运行 worker：先立刻对账一次，之后每 reconcileInterval 对账一次，
// 直到 ctx 被取消。
func (w *Worker) Run(ctx context.Context) {
	if !w.Enabled() {
		return
	}
	ticker := time.NewTicker(w.reconcileInterval)
	defer ticker.Stop()
	for {
		if err := w.ReconcileOnce(ctx); err != nil && ctx.Err() == nil {
			w.logger.WarnContext(ctx, "seat capacity outbox reconciliation failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// ReconcileOnce 执行一轮对账：批量认领到期意图，逐个结算；遇到 Cloud 限流就推迟重试，
// 整个 workspace 级限流只推迟同 workspace 后续意图，全局限流则直接结束本轮。
func (w *Worker) ReconcileOnce(ctx context.Context) error {
	if !w.Enabled() {
		return nil
	}
	workspaceDeferrals := make(map[uuid.UUID]rateLimitDeferral)
	for i := int32(0); i < w.batchSize; i++ {
		intent, err := w.queries.ClaimNextDueSeatCapacityIntent(ctx, pgtype.Timestamptz{
			Time: w.now().Add(defaultClaimLease), Valid: true,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			return err
		}
		workspaceID := uuidFromPG(intent.WorkspaceID)
		// 本 workspace 刚被限流并记了推迟，就不再发 Cloud 请求，直接把该意图推迟到
		// 同一时间点。
		if deferral, ok := workspaceDeferrals[workspaceID]; ok && deferral.until.After(w.now()) {
			if err := w.deferClaimedUntil(ctx, intent, deferral.reason, deferral.until); err != nil {
				return err
			}
			continue
		}
		settleErr := w.settleWithWorkspaceLimit(ctx, intent)
		if settleErr != nil {
			if IsRateLimited(settleErr) {
				deferredUntil, err := w.deferRateLimited(ctx, intent, settleErr)
				if err != nil {
					return errors.Join(settleErr, err)
				}
				if RateLimitScopeOf(settleErr) == RateLimitScopeWorkspace {
					workspaceDeferrals[workspaceID] = rateLimitDeferral{
						until: deferredUntil, reason: settleErr.Error(),
					}
					continue
				}
				// 全局 Cloud 429，或由入口/代理产生的、不带作用域的 429，可能波及每个
				// 租户。停止本轮而不是放大饱和；明确按 workspace 限流的 429 在上面
				// 继续处理。
				break
			}
			w.recordFailure(ctx, intent, settleErr)
		}
	}
	return nil
}

// rateLimitDeferral 记录某个 workspace 因限流被推迟到的时间与原因。
type rateLimitDeferral struct {
	until  time.Time
	reason string
}

// deferRateLimited 根据限流响应里的 Retry-After 把意图推迟重试（缺省 1 秒）。
func (w *Worker) deferRateLimited(ctx context.Context, intent db.SeatCapacityOutbox, settleErr error) (time.Time, error) {
	delay := RateLimitRetryAfter(settleErr)
	if delay <= 0 {
		delay = time.Second
	}
	deferredUntil := w.now().Add(delay)
	return deferredUntil, w.deferClaimedUntil(ctx, intent, settleErr.Error(), deferredUntil)
}

// deferClaimedUntil 把已认领的意图改期为「下次重试时间 + 错误原因」。
func (w *Worker) deferClaimedUntil(ctx context.Context, intent db.SeatCapacityOutbox, reason string, deferredUntil time.Time) error {
	_, err := w.queries.DeferClaimedSeatCapacityIntent(ctx, db.DeferClaimedSeatCapacityIntentParams{
		LastError: reason, NextAttemptAt: pgtype.Timestamptz{Time: deferredUntil, Valid: true},
		OperationToken: intent.OperationToken, Action: intent.Action, LeaseToken: intent.LeaseToken,
	})
	return err
}

// settleWithWorkspaceLimit 在「每个 workspace 一把分布式锁」的前提下结算一条意图：
// 拿到锁后用锁住的连接重读该行（防止持锁期间被别的 worker 改写），再执行结算。
func (w *Worker) settleWithWorkspaceLimit(ctx context.Context, intent db.SeatCapacityOutbox) error {
	if w.workspaceLocker == nil {
		return w.settle(ctx, intent)
	}
	lockCtx, cancel := context.WithTimeout(ctx, defaultWorkspaceLockWait)
	defer cancel()
	lockedDB, unlock, err := w.workspaceLocker.Lock(lockCtx, uuidFromPG(intent.WorkspaceID))
	if err != nil {
		return err
	}
	defer unlock()
	lockedQueries := w.queries
	if lockedDB != nil {
		lockedQueries = db.New(lockedDB)
	}
	// 在持锁的连接上重读当前行：行已不归我们（被删/被改）就直接无事返回。
	current, err := lockedQueries.GetClaimedSeatCapacityIntent(ctx, db.GetClaimedSeatCapacityIntentParams{
		OperationToken: intent.OperationToken, Action: intent.Action, LeaseToken: intent.LeaseToken,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	lockedWorker := *w
	lockedWorker.queries = lockedQueries
	return lockedWorker.settle(ctx, current)
}

// settle 按意图动作把 Cloud 容量状态与本地行对齐（对照真实的 Cloud operation 状态，
// 能删除的删除、需要释放的释放）。
func (w *Worker) settle(ctx context.Context, intent db.SeatCapacityOutbox) error {
	if !intent.LeaseToken.Valid {
		return errors.New("claimed seat capacity intent omitted lease token")
	}
	workspaceID := uuidFromPG(intent.WorkspaceID)
	token := uuidFromPG(intent.OperationToken)
	switch intent.Action {
	case ActionReserveInvitation:
		return w.recoverReserve(ctx, intent, workspaceID, token)
	case ActionConsumeInvitation, ActionClaimShareJoin:
		return w.recoverConsuming(ctx, intent, workspaceID, token)
	case ActionConfirm:
		decision, err := w.executor.Confirm(ctx, workspaceID, token, uuidFromPG(intent.MemberID))
		if err != nil {
			return err
		}
		if !decision.Allowed {
			return errors.New("capacity confirm rejected in state " + decision.Reason)
		}
		return w.deleteCurrent(ctx, intent)
	case ActionRelease:
		decision, err := w.executor.Release(ctx, workspaceID, token)
		if err != nil && !IsNotFound(err) {
			return err
		}
		if err == nil && decision.Managed && !decision.Allowed && decision.Reason != "released" {
			return errors.New("capacity release rejected in state " + decision.Reason)
		}
		return w.deleteCurrent(ctx, intent)
	case ActionReleaseMember:
		decision, err := w.executor.ReleaseMember(ctx, workspaceID, uuidFromPG(intent.MemberID))
		if err != nil && !IsNotFound(err) {
			return err
		}
		if err == nil && decision.Managed && !decision.Allowed && decision.Reason != "released" {
			return errors.New("capacity member release rejected in state " + decision.Reason)
		}
		return w.deleteCurrent(ctx, intent)
	default:
		return errors.New("unknown seat capacity outbox action")
	}
}

// recoverReserve 对照 Cloud 操作状态恢复「预留邀请」类意图：操作已释放/不存在则清理
// 行；仍处于 reserved 则看本地邀请是否还 pending——是则无需动作，否则释放容量。
func (w *Worker) recoverReserve(ctx context.Context, intent db.SeatCapacityOutbox, workspaceID, token uuid.UUID) error {
	decision, err := w.executor.GetOperation(ctx, workspaceID, token)
	if IsNotFound(err) || (err == nil && !decision.Managed) {
		return w.deleteCurrent(ctx, intent)
	}
	if err != nil {
		return err
	}
	if decision.Operation == nil {
		return errors.New("managed capacity operation response omitted operation")
	}
	switch decision.Operation.State {
	case "released":
		return w.deleteCurrent(ctx, intent)
	case "reserved":
		invitationID := intent.InvitationID
		if !invitationID.Valid {
			_, transitionErr := w.transition(ctx, intent, ActionRelease, pgtype.UUID{})
			return transitionErr
		}
		invitation, getErr := w.queries.GetInvitation(ctx, invitationID)
		if getErr == nil && invitation.Status == "pending" {
			return w.deleteCurrent(ctx, intent)
		}
		if getErr != nil && !errors.Is(getErr, pgx.ErrNoRows) {
			return getErr
		}
		// 本地邀请已不存在或不再 pending：没人会兑现这个预留，转去释放容量。
		_, transitionErr := w.transition(ctx, intent, ActionRelease, pgtype.UUID{})
		return transitionErr
	default:
		return errors.New("unexpected recovered invitation reservation state " + decision.Operation.State)
	}
}

// recoverConsuming 对照 Cloud 操作状态恢复「消耗（入座）」类意图。不同状态的分支目标
// 都是让占用结束且不留孤儿行：已 used/不存在则清理；仍 consuming 则说明产品侧事务
// 没提交（若提交了该行会原子变成 confirm），把被遗弃的请求释放掉而不是永远占着容量。
func (w *Worker) recoverConsuming(ctx context.Context, intent db.SeatCapacityOutbox, workspaceID, token uuid.UUID) error {
	decision, err := w.executor.GetOperation(ctx, workspaceID, token)
	if IsNotFound(err) || (err == nil && !decision.Managed) {
		return w.deleteCurrent(ctx, intent)
	}
	if err != nil {
		return err
	}
	if decision.Operation == nil {
		return errors.New("managed capacity operation response omitted operation")
	}
	switch decision.Operation.State {
	case "reserved":
		// 消耗请求从未生效。让邀请保持可用即可。
		return w.deleteCurrent(ctx, intent)
	case "used":
		// 已有并发请求提交并确认了该成员。陈旧的消耗 worker 绝不能再尝试释放这个
		// 已使用的座位。
		return w.deleteCurrent(ctx, intent)
	case "released":
		if intent.Action == ActionConsumeInvitation && intent.InvitationID.Valid {
			if err := w.queries.ExpireInvitationForCapacityRecovery(ctx, intent.InvitationID); err != nil {
				return err
			}
		}
		return w.deleteCurrent(ctx, intent)
	case "consuming":
		// 没有产品侧事务提交：若提交了，该事务会把本行原子改成 confirm。这里把被遗弃
		// 的用户请求释放掉，而不是永远占着容量。
		changed, err := w.transition(ctx, intent, ActionRelease, pgtype.UUID{})
		if err != nil || !changed {
			return err
		}
		if intent.Action == ActionConsumeInvitation && intent.InvitationID.Valid {
			if err := w.queries.ExpireInvitationForCapacityRecovery(ctx, intent.InvitationID); err != nil {
				return err
			}
		}
		return nil
	default:
		return errors.New("unexpected recovered consuming capacity state " + decision.Operation.State)
	}
}

// transition 把一条已认领意图的动作改成下一个动作（返回是否真的改了一行）。
func (w *Worker) transition(ctx context.Context, intent db.SeatCapacityOutbox, action string, memberID pgtype.UUID) (bool, error) {
	rows, err := w.queries.TransitionClaimedSeatCapacityIntent(ctx, db.TransitionClaimedSeatCapacityIntentParams{
		NextAction: action, CurrentAction: intent.Action, MemberID: memberID, OperationToken: intent.OperationToken,
		NextAttemptAt: pgtype.Timestamptz{Time: w.now(), Valid: true}, LeaseToken: intent.LeaseToken,
	})
	return rows == 1, err
}

// deleteCurrent 把一条已结算完的意图从 outbox 删除（幂等）。
func (w *Worker) deleteCurrent(ctx context.Context, intent db.SeatCapacityOutbox) error {
	_, err := w.queries.DeleteClaimedSeatCapacityIntent(ctx, db.DeleteClaimedSeatCapacityIntentParams{
		OperationToken: intent.OperationToken,
		Action:         intent.Action,
		LeaseToken:     intent.LeaseToken,
	})
	return err
}

// recordFailure 记录一次结算失败：达到最大尝试次数就把行移入 dead-letter；否则按
// 指数退避（5s 起步、封顶 5 分钟）改期重试。
func (w *Worker) recordFailure(ctx context.Context, intent db.SeatCapacityOutbox, settleErr error) {
	if intent.AttemptCount+1 >= w.maxAttempts {
		rows, err := w.queries.MarkClaimedSeatCapacityIntentDeadLettered(ctx, db.MarkClaimedSeatCapacityIntentDeadLetteredParams{
			LastError: settleErr.Error(), OperationToken: intent.OperationToken, Action: intent.Action,
			LeaseToken: intent.LeaseToken,
		})
		if err != nil {
			w.logger.WarnContext(ctx, "seat capacity outbox dead letter could not be recorded", "error", err)
			return
		}
		if rows == 1 {
			w.logger.ErrorContext(ctx, "seat capacity outbox intent moved to dead letter",
				"workspace_id", workspaceIDString(intent.WorkspaceID), "action", intent.Action,
				"attempt", intent.AttemptCount+1, "error", settleErr)
		}
		return
	}
	backoff := 5 * time.Second
	for i := int32(0); i < intent.AttemptCount && backoff < 5*time.Minute; i++ {
		backoff *= 2
	}
	if backoff > 5*time.Minute {
		backoff = 5 * time.Minute
	}
	rows, err := w.queries.MarkClaimedSeatCapacityIntentFailed(ctx, db.MarkClaimedSeatCapacityIntentFailedParams{
		LastError: settleErr.Error(), NextAttemptAt: pgtype.Timestamptz{Time: w.now().Add(backoff), Valid: true},
		OperationToken: intent.OperationToken, Action: intent.Action, LeaseToken: intent.LeaseToken,
	})
	if err != nil {
		w.logger.WarnContext(ctx, "seat capacity outbox failure could not be recorded", "error", err)
	}
	// 只在首次与每隔 10 次时告警，避免噪音刷屏。
	if rows == 1 && (intent.AttemptCount == 0 || (intent.AttemptCount+1)%10 == 0) {
		w.logger.WarnContext(ctx, "seat capacity outbox intent remains unsettled",
			"workspace_id", workspaceIDString(intent.WorkspaceID), "action", intent.Action,
			"attempt", intent.AttemptCount+1, "error", settleErr)
	}
}

// uuidFromPG 把 pgtype.UUID 转成标准 uuid（无效时为 uuid.Nil）。
func uuidFromPG(value pgtype.UUID) uuid.UUID {
	if !value.Valid {
		return uuid.Nil
	}
	return uuid.UUID(value.Bytes)
}

// workspaceIDString 便于日志打印 workspace id。
func workspaceIDString(value pgtype.UUID) string { return uuidFromPG(value).String() }

// RecoveryDue 返回「到期可恢复」的时间点：现在 + 恢复宽限。供认领到期行时使用。
func RecoveryDue(now time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: now.Add(defaultRecoveryGrace), Valid: true}
}

// RetryDue 返回「立即可重试」的时间点（now）。
func RetryDue(now time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: now, Valid: true}
}
