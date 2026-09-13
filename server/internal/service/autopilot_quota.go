// autopilot_quota.go —— Autopilot 运行的配额（quota）准入与结算。
// 职责：① 从 Entitlements 解析云端的运行配额策略（observe/enforce），② 在同一事务里「建 run +
// 预留配额」并保证幂等（同一幂等键不二次占额），③ run 收尾时把预留结算为「消费」（completed）或
// 「释放」（failed/skipped/孤儿），④ 暴露使用量、被拒统计与恢复扫描。配额表只在 quota 启用且有事务
// 起点时才会被触碰，self-hosted 部署永远走旧的直插路径。
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/dispatch"
	"github.com/multica-ai/multica/server/internal/entitlement"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// AutopilotQuotaMetrics 刻意只接收有界的标签。工作区标识与策略值绝不能当指标标签。
type AutopilotQuotaMetrics interface {
	RecordAutopilotQuotaDecision(action, source, result string)
}

// AutopilotQuotaExceededError 只在「enforce 决策且云端给的区间已满」时返回。HTTP 调用方能序列化
// 这些事实，而不用在 OSS 里嵌入商业文案或套餐名。
type AutopilotQuotaExceededError struct {
	Used     int64
	Reserved int64
	Limit    int64
	ResetAt  time.Time
}

// Error 返回通用文案（具体用量由字段携带）。
func (e *AutopilotQuotaExceededError) Error() string {
	return "autopilot run quota exceeded"
}

// AutopilotQuotaUsage 是按工作区、策略无关的 API 模型。策略关闭/格式错误时返回 Enabled=false 且
// 各事实字段全为 nil。
type AutopilotQuotaUsage struct {
	Enabled  bool
	Action   string
	Used     *int64
	Reserved *int64
	Total    *int64
	Limit    *int64
	// Reached 只在 enforce 时非 nil；observe 永不拒绝 run，所以观察时无此概念。
	Reached       *bool
	PeriodStart   *time.Time
	PeriodEnd     *time.Time
	ResetAt       *time.Time
	BlockedCounts map[string]int64
}

// autopilotQuotaPolicy 是解析自 Entitlements 的生效配额策略快照（含政策/订阅版本，随预留落库用于审计）。
type autopilotQuotaPolicy struct {
	action              entitlement.Action
	limit               int64
	periodStart         time.Time
	periodEnd           time.Time
	resetAt             time.Time
	policyRevision      int64
	subscriptionVersion int64
}

// newAutopilotIdempotencyKey 生成随机幂等键。
func newAutopilotIdempotencyKey() string { return uuid.NewString() }

// NewRequestIdempotencyKey 只在 HTTP 调用方漏带幂等键时使用；生成值把幂等范围限定到那单个请求。
func NewRequestIdempotencyKey() string { return newAutopilotIdempotencyKey() }

// validAutopilotExecutionSource 校验触发来源是否在受支持集合内。
func validAutopilotExecutionSource(source string) bool {
	switch source {
	case "schedule", "manual", "webhook", "api":
		return true
	default:
		return false
	}
}

// quotaPolicy 解析工作区的生效配额策略。Entitlements/workspace 缺失、动作非 observe/enforce、或
// 政策字段畸形时返回 enabled=false。
func (s *AutopilotService) quotaPolicy(ctx context.Context, workspaceID pgtype.UUID) (autopilotQuotaPolicy, bool) {
	if s.Entitlements == nil || !workspaceID.Valid {
		return autopilotQuotaPolicy{}, false
	}
	decision := s.Entitlements.Gate(ctx, uuid.UUID(workspaceID.Bytes), entitlement.GateAutopilotRuns)
	gate := decision.Gate
	if gate.Action == entitlement.ActionOff {
		return autopilotQuotaPolicy{}, false
	}
	if (gate.Action != entitlement.ActionObserve && gate.Action != entitlement.ActionEnforce) ||
		gate.Limit == nil || *gate.Limit < 0 || gate.PeriodStart == nil || gate.PeriodEnd == nil ||
		gate.ResetAt == nil || !gate.PeriodStart.Before(*gate.PeriodEnd) {
		// 畸形政策 fail-open，而且关键是绝不访问配额表。区间如何构造只由 Cloud 说了算。
		return autopilotQuotaPolicy{}, false
	}
	return autopilotQuotaPolicy{
		action:              gate.Action,
		limit:               int64(*gate.Limit),
		periodStart:         gate.PeriodStart.UTC(),
		periodEnd:           gate.PeriodEnd.UTC(),
		resetAt:             gate.ResetAt.UTC(),
		policyRevision:      decision.PolicyRevision,
		subscriptionVersion: decision.SubscriptionVersion,
	}, true
}

// createAutopilotRunWithQuota 在单事务里预留配额并建 run。策略关闭时，刻意走旧版直插 INSERT——
// self-hosted 部署因此永远不碰配额表。
func (s *AutopilotService) createAutopilotRunWithQuota(
	ctx context.Context,
	workspaceID pgtype.UUID,
	source, idempotencyKey string,
	params db.CreateAutopilotRunParams,
) (db.AutopilotRun, bool, error) {
	if !validAutopilotExecutionSource(source) {
		return db.AutopilotRun{}, false, fmt.Errorf("invalid autopilot execution source %q", source)
	}
	policy, enabled := s.quotaPolicy(ctx, workspaceID)
	// run-start 事件描述准入本身，必须与 run 行在同一事务里落库；否则 INSERT 后立刻崩溃会留下
	// 一条无法重放也无法审计的执行。每种 server 装配都有事务起点；nil starter 只为小型进程内
	// 测试/嵌入夹具保留——那里历史同步总线是唯一可用的投递路径。
	if !enabled && s.TxStarter == nil {
		run, err := s.Queries.CreateAutopilotRun(ctx, params)
		if err != nil {
			return db.AutopilotRun{}, false, err
		}
		s.publishAutopilotRunStart(ctx, workspaceID, source, run)
		return run, false, nil
	}
	if s.TxStarter == nil {
		return db.AutopilotRun{}, false, errors.New("autopilot run: transaction starter is required")
	}

	if !enabled {
		tx, err := s.TxStarter.Begin(ctx)
		if err != nil {
			return db.AutopilotRun{}, false, fmt.Errorf("begin autopilot admission: %w", err)
		}
		defer tx.Rollback(ctx)
		qtx := s.Queries.WithTx(tx)
		run, err := qtx.CreateAutopilotRun(ctx, params)
		if err != nil {
			return db.AutopilotRun{}, false, err
		}
		start, err := recordDurableEventTx(ctx, qtx, autopilotRunStartEvent(workspaceID, source, run))
		if err != nil {
			return db.AutopilotRun{}, false, fmt.Errorf("record autopilot run start: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return db.AutopilotRun{}, false, fmt.Errorf("commit autopilot admission: %w", err)
		}
		publishCommittedEvent(s.Bus, start)
		return run, false, nil
	}

	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return db.AutopilotRun{}, false, fmt.Errorf("begin quota admission: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)
	periodArgs := db.EnsureAutopilotQuotaPeriodParams{
		WorkspaceID: workspaceID,
		PeriodStart: pgtype.Timestamptz{Time: policy.periodStart, Valid: true},
		PeriodEnd:   pgtype.Timestamptz{Time: policy.periodEnd, Valid: true},
	}
	period, err := qtx.EnsureAutopilotQuotaPeriod(ctx, periodArgs)
	if err != nil {
		return db.AutopilotRun{}, false, fmt.Errorf("lock quota period: %w", err)
	}

	existing, err := qtx.GetAutopilotQuotaReservationByKey(ctx, db.GetAutopilotQuotaReservationByKeyParams{
		WorkspaceID:    workspaceID,
		PeriodStart:    periodArgs.PeriodStart,
		PeriodEnd:      periodArgs.PeriodEnd,
		IdempotencyKey: idempotencyKey,
	})
	if err == nil {
		run, runErr := qtx.GetAutopilotRunByQuotaReservation(ctx, existing.ID)
		if errors.Is(runErr, pgx.ErrNoRows) && existing.State == "reserved" {
			// 预留/run 插入正常是原子提交的。这里恢复一条被手动删除或成了孤儿的 reserved 行——
			// 免得这条稳定的幂等键把整个周期的所有重试都楔死。
			if _, releaseErr := settleAutopilotQuota(ctx, qtx, existing.ID, false); releaseErr != nil {
				return db.AutopilotRun{}, false, fmt.Errorf("release orphaned idempotency reservation: %w", releaseErr)
			}
			period.ReservedCount--
			err = pgx.ErrNoRows // 继续走下方正常的新预留路径
		} else if runErr != nil {
			return db.AutopilotRun{}, false, fmt.Errorf("load idempotent quota run: %w", runErr)
		} else {
			if err := tx.Commit(ctx); err != nil {
				return db.AutopilotRun{}, false, fmt.Errorf("commit idempotent quota admission: %w", err)
			}
			s.recordAutopilotQuotaDecision(policy.action, source, "reused")
			return run, true, nil
		}
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return db.AutopilotRun{}, false, fmt.Errorf("lookup quota reservation: %w", err)
	}

	wouldBlock := period.UsedCount+period.ReservedCount >= policy.limit
	if wouldBlock && policy.action == entitlement.ActionEnforce {
		if _, err := qtx.IncrementAutopilotQuotaBlocked(ctx, db.IncrementAutopilotQuotaBlockedParams{
			Source: source, WorkspaceID: workspaceID,
			PeriodStart: periodArgs.PeriodStart, PeriodEnd: periodArgs.PeriodEnd,
		}); err != nil {
			return db.AutopilotRun{}, false, fmt.Errorf("record blocked quota admission: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return db.AutopilotRun{}, false, fmt.Errorf("commit blocked quota admission: %w", err)
		}
		s.recordAutopilotQuotaDecision(policy.action, source, "blocked")
		return db.AutopilotRun{}, false, &AutopilotQuotaExceededError{
			Used: period.UsedCount, Reserved: period.ReservedCount,
			Limit: policy.limit, ResetAt: policy.resetAt,
		}
	}
	// observe 模式下的「本会拦截」只进有界决策指标。持久的被拒计数只为真正拒绝工作的决策服务，
	// 供 usage API 读取。
	reservation, err := qtx.CreateAutopilotQuotaReservation(ctx, db.CreateAutopilotQuotaReservationParams{
		WorkspaceID: workspaceID, PeriodStart: periodArgs.PeriodStart, PeriodEnd: periodArgs.PeriodEnd,
		PolicyRevision: policy.policyRevision, SubscriptionVersion: policy.subscriptionVersion,
		Source: source, IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return db.AutopilotRun{}, false, fmt.Errorf("create quota reservation: %w", err)
	}
	if _, err := qtx.IncrementAutopilotQuotaReserved(ctx, db.IncrementAutopilotQuotaReservedParams{
		WorkspaceID: periodArgs.WorkspaceID, PeriodStart: periodArgs.PeriodStart, PeriodEnd: periodArgs.PeriodEnd,
	}); err != nil {
		return db.AutopilotRun{}, false, fmt.Errorf("increment reserved quota: %w", err)
	}
	params.QuotaReservationID = reservation.ID
	run, err := qtx.CreateAutopilotRun(ctx, params)
	if err != nil {
		return db.AutopilotRun{}, false, fmt.Errorf("create quota-linked run: %w", err)
	}
	start, err := recordDurableEventTx(ctx, qtx, autopilotRunStartEvent(workspaceID, source, run))
	if err != nil {
		return db.AutopilotRun{}, false, fmt.Errorf("record autopilot run start: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return db.AutopilotRun{}, false, fmt.Errorf("commit quota admission: %w", err)
	}
	publishCommittedEvent(s.Bus, start)
	result := "admitted"
	if wouldBlock {
		result = "would_block"
	}
	s.recordAutopilotQuotaDecision(policy.action, source, result)
	return run, false, nil
}

// autopilotRunStartEvent 构造「run 已开始」的持久化事件信封（幂等键以 run id 计，重放不会重复）。
func autopilotRunStartEvent(workspaceID pgtype.UUID, source string, run db.AutopilotRun) events.Event {
	return events.Event{
		Type:           protocol.EventAutopilotRunStart,
		IdempotencyKey: "autopilot:run-start:" + util.UUIDToString(run.ID),
		StreamKey:      "autopilot:" + util.UUIDToString(run.AutopilotID),
		WorkspaceID:    util.UUIDToString(workspaceID),
		ActorType:      "system",
		Payload: map[string]any{
			"run_id":       util.UUIDToString(run.ID),
			"autopilot_id": util.UUIDToString(run.AutopilotID),
			"source":       source,
			"status":       run.Status,
		},
	}
}

func (s *AutopilotService) publishAutopilotRunStart(ctx context.Context, workspaceID pgtype.UUID, source string, run db.AutopilotRun) {
	if s == nil || s.Bus == nil {
		return
	}
	event := autopilotRunStartEvent(workspaceID, source, run)
	// 该分支刻意只服务于非事务夹具。生产服务永远走上面的事务持久化路径。
	s.Bus.Publish(event)
}

// recordAutopilotQuotaDecision 上报一次配额决策指标（QuotaMetrics 可为 nil，静默跳过）。
func (s *AutopilotService) recordAutopilotQuotaDecision(action entitlement.Action, source, result string) {
	if s.QuotaMetrics != nil {
		s.QuotaMetrics.RecordAutopilotQuotaDecision(string(action), source, result)
	}
}

// settleAutopilotQuota 结算一条配额预留：consume=true 记消费（completed），否则释放（failed/skipped/
// 孤儿）。基于 CAS 的状态翻转幂等；返回 changed=false 表示该预留已被终态重放处理过。
func settleAutopilotQuota(ctx context.Context, q *db.Queries, reservationID pgtype.UUID, consume bool) (bool, error) {
	if !reservationID.Valid {
		return false, nil
	}
	var err error
	if consume {
		_, err = q.ConsumeAutopilotQuotaReservation(ctx, reservationID)
	} else {
		_, err = q.ReleaseAutopilotQuotaReservation(ctx, reservationID)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // 终态重放：该预留早已结算
	}
	return err == nil, err
}

// completeAutopilotRun 非事务地把 run 置为 completed 并消费配额（供无 TxStarter 的降级路径）。
func (s *AutopilotService) completeAutopilotRun(ctx context.Context, params db.UpdateAutopilotRunCompletedParams) (db.AutopilotRun, error) {
	row, err := s.Queries.UpdateAutopilotRunTerminalWithQuota(ctx, db.UpdateAutopilotRunTerminalWithQuotaParams{
		TerminalStatus: "completed",
		Result:         params.Result,
		RunID:          params.ID,
		Consume:        true,
	})
	return autopilotRunFromTerminalRow(row), err
}

// failAutopilotRun 非事务地把 run 置为 failed 并释放配额。
func (s *AutopilotService) failAutopilotRun(ctx context.Context, params db.UpdateAutopilotRunFailedParams) (db.AutopilotRun, error) {
	row, err := s.Queries.UpdateAutopilotRunTerminalWithQuota(ctx, db.UpdateAutopilotRunTerminalWithQuotaParams{
		TerminalStatus: "failed",
		FailureReason:  params.FailureReason,
		ReasonCode:     params.ReasonCode,
		RunID:          params.ID,
	})
	return autopilotRunFromTerminalRow(row), err
}

// skipAutopilotRun 非事务地把 run 置为 skipped 并释放配额。
func (s *AutopilotService) skipAutopilotRun(ctx context.Context, params db.UpdateAutopilotRunSkippedParams) (db.AutopilotRun, error) {
	row, err := s.Queries.UpdateAutopilotRunTerminalWithQuota(ctx, db.UpdateAutopilotRunTerminalWithQuotaParams{
		TerminalStatus: "skipped",
		FailureReason:  params.FailureReason,
		ReasonCode:     params.ReasonCode,
		RunID:          params.ID,
	})
	return autopilotRunFromTerminalRow(row), err
}

// autopilotRunFromTerminalRow 把终态更新的返回行还原成 db.AutopilotRun。
func autopilotRunFromTerminalRow(row db.UpdateAutopilotRunTerminalWithQuotaRow) db.AutopilotRun {
	return db.AutopilotRun{
		ID: row.ID, AutopilotID: row.AutopilotID, TriggerID: row.TriggerID,
		Source: row.Source, Status: row.Status, IssueID: row.IssueID, TaskID: row.TaskID,
		TriggeredAt: row.TriggeredAt, CompletedAt: row.CompletedAt,
		FailureReason: row.FailureReason, TriggerPayload: row.TriggerPayload,
		Result: row.Result, CreatedAt: row.CreatedAt, SquadID: row.SquadID,
		PlannedAt: row.PlannedAt, WebhookDeliveryID: row.WebhookDeliveryID,
		QuotaReservationID: row.QuotaReservationID, ReasonCode: row.ReasonCode,
	}
}

// settleAutopilotRunDurably 执行一次 run 终态切换，并把 run_done 信封与它在同一数据库提交里落库。
// 供持久化 task/issue 投影 worker 之外的派发错误/skip 路径使用。
func (s *AutopilotService) settleAutopilotRunDurably(
	ctx context.Context,
	runID pgtype.UUID,
	status string,
	result []byte,
	reason string,
	reasonCode dispatch.ReasonCode,
	bumpLastRun bool,
) (db.AutopilotRun, events.Event, error) {
	if s == nil || s.TxStarter == nil {
		return db.AutopilotRun{}, events.Event{}, errors.New("autopilot terminal transition: transaction starter is required")
	}
	if status != "completed" && status != "failed" && status != "skipped" {
		return db.AutopilotRun{}, events.Event{}, fmt.Errorf("autopilot terminal transition: invalid status %q", status)
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return db.AutopilotRun{}, events.Event{}, fmt.Errorf("begin autopilot terminal transition: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)
	row, err := qtx.UpdateAutopilotRunTerminalWithQuota(ctx, db.UpdateAutopilotRunTerminalWithQuotaParams{
		TerminalStatus: status,
		Result:         result,
		FailureReason:  pgtype.Text{String: reason, Valid: reason != ""},
		ReasonCode:     pgtype.Text{String: string(reasonCode), Valid: reasonCode != ""},
		RunID:          runID,
		Consume:        status == "completed",
	})
	if err != nil {
		return db.AutopilotRun{}, events.Event{}, fmt.Errorf("update autopilot run: %w", err)
	}
	run := autopilotRunFromTerminalRow(row)
	autopilot, err := qtx.GetAutopilot(ctx, run.AutopilotID)
	if err != nil {
		return db.AutopilotRun{}, events.Event{}, fmt.Errorf("load autopilot for terminal event: %w", err)
	}
	if bumpLastRun {
		if err := qtx.UpdateAutopilotLastRunAt(ctx, autopilot.ID); err != nil {
			return db.AutopilotRun{}, events.Event{}, fmt.Errorf("update autopilot last run: %w", err)
		}
	}
	event, err := recordDurableEventTx(ctx, qtx, autopilotRunDoneEvent(autopilot.WorkspaceID, run, status))
	if err != nil {
		return db.AutopilotRun{}, events.Event{}, fmt.Errorf("record autopilot run done: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return db.AutopilotRun{}, events.Event{}, fmt.Errorf("commit autopilot terminal transition: %w", err)
	}
	publishCommittedEvent(s.Bus, event)
	return run, event, nil
}

// recoverPartialAutopilotRun 把「建了 run 却未建下游资源」的半成品标记为失败（DB 侧 SQL 完成），
// 释放其 planned_at 部分唯一槽位。返回是否有行被恢复。
func (s *AutopilotService) recoverPartialAutopilotRun(ctx context.Context, run db.AutopilotRun) (bool, error) {
	rows, err := s.Queries.RecoverPartialAutopilotRun(ctx, run.ID)
	return rows > 0, err
}

// FailAutopilotRunsByIssue 在删除清掉 issue_id 之前，让 create_issue 的消费保持不可变、同时释放
// 仍被预留的 run_only 槽位。
func (s *AutopilotService) FailAutopilotRunsByIssue(ctx context.Context, issueID pgtype.UUID) error {
	if s.TxStarter == nil {
		_, err := s.Queries.FailAutopilotRunsByIssue(ctx, issueID)
		return err
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin fail autopilot runs by issue: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)
	rows, err := qtx.FailAutopilotRunsByIssue(ctx, issueID)
	if err != nil {
		return fmt.Errorf("fail autopilot runs by issue: %w", err)
	}
	eventsToPublish := make([]events.Event, 0, len(rows))
	for _, row := range rows {
		run := autopilotRunFromFailedByIssueRow(row)
		autopilot, loadErr := qtx.GetAutopilot(ctx, run.AutopilotID)
		if loadErr != nil {
			return fmt.Errorf("load autopilot for deleted issue run: %w", loadErr)
		}
		event, eventErr := recordDurableEventTx(ctx, qtx, autopilotRunDoneEvent(autopilot.WorkspaceID, run, "failed"))
		if eventErr != nil {
			return fmt.Errorf("record deleted issue run event: %w", eventErr)
		}
		eventsToPublish = append(eventsToPublish, event)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit failed autopilot runs by issue: %w", err)
	}
	for _, event := range eventsToPublish {
		publishCommittedEvent(s.Bus, event)
	}
	return nil
}

func autopilotRunFromFailedByIssueRow(row db.FailAutopilotRunsByIssueRow) db.AutopilotRun {
	return db.AutopilotRun{
		ID: row.ID, AutopilotID: row.AutopilotID, TriggerID: row.TriggerID,
		Source: row.Source, Status: row.Status, IssueID: row.IssueID, TaskID: row.TaskID,
		TriggeredAt: row.TriggeredAt, CompletedAt: row.CompletedAt,
		FailureReason: row.FailureReason, TriggerPayload: row.TriggerPayload,
		Result: row.Result, CreatedAt: row.CreatedAt, SquadID: row.SquadID,
		PlannedAt: row.PlannedAt, WebhookDeliveryID: row.WebhookDeliveryID,
		QuotaReservationID: row.QuotaReservationID, ReasonCode: row.ReasonCode,
	}
}

// AutopilotQuotaUsage 查询当前周期配额用量。政策关闭时返回 Enabled=false。
func (s *AutopilotService) AutopilotQuotaUsage(ctx context.Context, workspaceID pgtype.UUID) (AutopilotQuotaUsage, error) {
	policy, enabled := s.quotaPolicy(ctx, workspaceID)
	if !enabled {
		return AutopilotQuotaUsage{Enabled: false}, nil
	}
	period, err := s.Queries.GetAutopilotQuotaPeriod(ctx, db.GetAutopilotQuotaPeriodParams{
		WorkspaceID: workspaceID,
		PeriodStart: pgtype.Timestamptz{Time: policy.periodStart, Valid: true},
		PeriodEnd:   pgtype.Timestamptz{Time: policy.periodEnd, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		period.UsedCount = 0
		period.ReservedCount = 0
	} else if err != nil {
		return AutopilotQuotaUsage{}, fmt.Errorf("load autopilot quota usage: %w", err)
	}
	blockedCounts := make(map[string]int64)
	if len(period.BlockedCounts) > 0 {
		if err := json.Unmarshal(period.BlockedCounts, &blockedCounts); err != nil {
			return AutopilotQuotaUsage{}, fmt.Errorf("decode autopilot quota blocked counts: %w", err)
		}
		if blockedCounts == nil {
			blockedCounts = make(map[string]int64)
		}
	}
	total := period.UsedCount + period.ReservedCount
	var reached *bool
	if policy.action == entitlement.ActionEnforce {
		value := total >= policy.limit
		reached = &value
	}
	return AutopilotQuotaUsage{
		Enabled: true, Action: string(policy.action),
		Used: &period.UsedCount, Reserved: &period.ReservedCount, Total: &total,
		Limit: &policy.limit, Reached: reached,
		PeriodStart: &policy.periodStart, PeriodEnd: &policy.periodEnd, ResetAt: &policy.resetAt,
		BlockedCounts: blockedCounts,
	}, nil
}

// QuotaEnabled 报告本部署是否配置了配额提供方。
func (s *AutopilotService) QuotaEnabled() bool { return s.Entitlements != nil }

// ReconcileAutopilotQuotaReservations 修复「预留/run 事务之后、下游副作用或正常 finalizer 之前」崩溃
// 留下的窗口。预留翻转保持 CAS 语义，副本可并发跑本函数而不把计数翻倍。
func (s *AutopilotService) ReconcileAutopilotQuotaReservations(
	ctx context.Context,
	terminalCreatedBefore time.Time,
	partialCreatedBefore time.Time,
	limit int32,
) (int, error) {
	if !s.QuotaEnabled() || limit <= 0 {
		return 0, nil
	}
	reservations, err := s.Queries.ListRecoverableAutopilotQuotaReservations(ctx, db.ListRecoverableAutopilotQuotaReservationsParams{
		TerminalCreatedBefore: pgtype.Timestamptz{Time: terminalCreatedBefore.UTC(), Valid: true},
		PartialCreatedBefore:  pgtype.Timestamptz{Time: partialCreatedBefore.UTC(), Valid: true},
		RowLimit:              limit,
	})
	if err != nil {
		return 0, fmt.Errorf("list recoverable quota reservations: %w", err)
	}
	settled := 0
	for _, reservation := range reservations {
		run, runErr := s.Queries.GetAutopilotRunByQuotaReservation(ctx, reservation.ID)
		switch {
		case errors.Is(runErr, pgx.ErrNoRows):
			changed, err := settleAutopilotQuota(ctx, s.Queries, reservation.ID, false)
			if err != nil {
				return settled, fmt.Errorf("release orphan quota reservation: %w", err)
			}
			if !changed {
				continue
			}
		case runErr != nil:
			return settled, fmt.Errorf("load quota-linked run: %w", runErr)
		case run.Status == "completed":
			changed, err := settleAutopilotQuota(ctx, s.Queries, reservation.ID, true)
			if err != nil {
				return settled, fmt.Errorf("consume completed quota reservation: %w", err)
			}
			if !changed {
				continue
			}
		case run.Status == "failed" || run.Status == "skipped":
			changed, err := settleAutopilotQuota(ctx, s.Queries, reservation.ID, false)
			if err != nil {
				return settled, fmt.Errorf("release terminal quota reservation: %w", err)
			}
			if !changed {
				continue
			}
		case (run.Source == "manual" || run.Source == "api") &&
			!run.IssueID.Valid && !run.TaskID.Valid &&
			(run.Status == "pending" || run.Status == "issue_created" || run.Status == "running"):
			changed, err := s.recoverPartialAutopilotRun(ctx, run)
			if err != nil {
				return settled, fmt.Errorf("recover abandoned quota run: %w", err)
			}
			if !changed {
				continue
			}
		default:
			// 计划与 webhook 重试自管其半成品恢复；查询已把它们排除，此分支只是防御性兜底。
			continue
		}
		settled++
	}
	return settled, nil
}
