// autopilot_lifecycle.go —— Autopilot 运行的run 生命周期与终态结算。
// 职责：① 从 restricted action 解析云端的运行配额策略（observe/enforce），② 在同一事务里「建 run +
// 预留配额」并保证幂等（同一幂等键不二次占额），③ run 收尾时把预留结算为「消费」（completed）或
// 「释放」（failed/skipped/孤儿），④ 暴露使用量、被拒统计与恢复扫描。配额表只在 platform limit 启用且有事务
// 起点时才会被触碰，self-hosted 部署永远走旧的直插路径。
package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/dispatch"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func NewRequestIdempotencyKey() string { return uuid.NewString() }

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

// completeAutopilotRun marks a run completed.
func (s *AutopilotService) completeAutopilotRun(ctx context.Context, params db.UpdateAutopilotRunCompletedParams) (db.AutopilotRun, error) {
	return s.Queries.UpdateAutopilotRunCompleted(ctx, params)
}
func (s *AutopilotService) failAutopilotRun(ctx context.Context, params db.UpdateAutopilotRunFailedParams) (db.AutopilotRun, error) {
	return s.Queries.UpdateAutopilotRunFailed(ctx, params)
}
func (s *AutopilotService) skipAutopilotRun(ctx context.Context, params db.UpdateAutopilotRunSkippedParams) (db.AutopilotRun, error) {
	return s.Queries.UpdateAutopilotRunSkipped(ctx, params)
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
	var run db.AutopilotRun
	switch status {
	case "completed":
		run, err = qtx.UpdateAutopilotRunCompleted(ctx, db.UpdateAutopilotRunCompletedParams{ID: runID, Result: result})
	case "failed":
		run, err = qtx.UpdateAutopilotRunFailed(ctx, db.UpdateAutopilotRunFailedParams{ID: runID, FailureReason: pgtype.Text{String: reason, Valid: reason != ""}, ReasonCode: pgtype.Text{String: string(reasonCode), Valid: reasonCode != ""}})
	case "skipped":
		run, err = qtx.UpdateAutopilotRunSkipped(ctx, db.UpdateAutopilotRunSkippedParams{ID: runID, FailureReason: pgtype.Text{String: reason, Valid: reason != ""}, ReasonCode: pgtype.Text{String: string(reasonCode), Valid: reasonCode != ""}})
	}
	if err != nil {
		return db.AutopilotRun{}, events.Event{}, fmt.Errorf("update autopilot run: %w", err)
	}
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
		run := row
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
