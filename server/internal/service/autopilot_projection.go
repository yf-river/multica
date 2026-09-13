// autopilot_projection.go —— 把 autopilot 创建 issue / 相关任务的终态投影回 autopilot_run（经调用方事务句柄）。
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ProjectAutopilotRunFromIssue 经调用方事务内查询，应用 autopilot 创建 issue 的终态。事件为 nil 表示该 issue
// 不是活跃 autopilot run。
func ProjectAutopilotRunFromIssue(ctx context.Context, queries *db.Queries, issue db.Issue) (*events.Event, error) {
	if !issue.OriginType.Valid || issue.OriginType.String != "autopilot" {
		return nil, nil
	}
	run, err := queries.GetAutopilotRunByIssue(ctx, issue.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load autopilot run for issue: %w", err)
	}
	autopilot, err := queries.GetAutopilot(ctx, run.AutopilotID)
	if err != nil {
		return nil, fmt.Errorf("load autopilot for issue run: %w", err)
	}

	effectiveStatus := issuestatus.Effective(ctx, queries, issue.WorkspaceID, issue.Status)
	switch effectiveStatus {
	case "done", "in_review":
		updated, err := updateAutopilotRunTerminalInProjection(ctx, queries, run.ID, "completed", nil, "")
		return terminalAutopilotRunEvent(autopilot, updated, "completed", err)
	case "cancelled", "blocked":
		reason := "issue " + issue.Status
		updated, err := updateAutopilotRunTerminalInProjection(ctx, queries, run.ID, "failed", nil, reason)
		return terminalAutopilotRunEvent(autopilot, updated, "failed", err)
	default:
		return nil, nil
	}
}

// ProjectAutopilotRunFromTask 应用任务终态：既处理直接挂在 run 上的 run_only 任务，也处理经 issue 关联的
// create_issue 任务。调用方持有事务与投递回执。
func ProjectAutopilotRunFromTask(ctx context.Context, queries *db.Queries, task db.AgentTaskQueue) (*events.Event, error) {
	if task.AutopilotRunID.Valid {
		return projectDirectAutopilotTask(ctx, queries, task)
	}
	if !task.IssueID.Valid || task.Status != "failed" {
		return nil, nil
	}
	return projectLinkedIssueAutopilotTask(ctx, queries, task)
}

// projectDirectAutopilotTask 投影直接挂 run 的任务终态（仅当 run 处于 running/issue_created）。
func projectDirectAutopilotTask(ctx context.Context, queries *db.Queries, task db.AgentTaskQueue) (*events.Event, error) {
	run, err := queries.GetAutopilotRun(ctx, task.AutopilotRunID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load autopilot run for task: %w", err)
	}
	if run.Status != "running" && run.Status != "issue_created" {
		return nil, nil
	}
	autopilot, err := queries.GetAutopilot(ctx, run.AutopilotID)
	if err != nil {
		return nil, fmt.Errorf("load autopilot for task run: %w", err)
	}

	switch task.Status {
	case "completed":
		updated, err := updateAutopilotRunTerminalInProjection(ctx, queries, run.ID, "completed", task.Result, "")
		return terminalAutopilotRunEvent(autopilot, updated, "completed", err)
	case "failed", "cancelled":
		updated, err := updateAutopilotRunFailed(ctx, queries, run.ID, task)
		return terminalAutopilotRunEvent(autopilot, updated, "failed", err)
	default:
		return nil, nil
	}
}

// projectLinkedIssueAutopilotTask 投影经 issue 关联的失败任务：无重试子任务、issue 无其它活跃任务时，
// 把该 autopilot run 判为失败。
func projectLinkedIssueAutopilotTask(ctx context.Context, queries *db.Queries, task db.AgentTaskQueue) (*events.Event, error) {
	run, err := queries.GetAutopilotRunByIssue(ctx, task.IssueID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load autopilot run for linked issue task: %w", err)
	}
	hasRetry, err := queries.HasRetryTaskForParent(ctx, task.ID)
	if err != nil {
		return nil, fmt.Errorf("check retry child for autopilot issue task: %w", err)
	}
	if hasRetry {
		return nil, nil
	}
	hasActive, err := queries.HasActiveTaskForIssue(ctx, task.IssueID)
	if err != nil {
		return nil, fmt.Errorf("check active tasks for autopilot issue: %w", err)
	}
	if hasActive {
		return nil, nil
	}
	autopilot, err := queries.GetAutopilot(ctx, run.AutopilotID)
	if err != nil {
		return nil, fmt.Errorf("load autopilot for linked issue run: %w", err)
	}
	updated, err := updateAutopilotRunFailed(ctx, queries, run.ID, task)
	return terminalAutopilotRunEvent(autopilot, updated, "failed", err)
}

// updateAutopilotRunFailed 用任务失败原因更新 run 为 failed。
func updateAutopilotRunFailed(ctx context.Context, queries *db.Queries, runID pgtype.UUID, task db.AgentTaskQueue) (db.AutopilotRun, error) {
	reason := taskFailureReasonForAutopilotRun(task)
	return updateAutopilotRunTerminalInProjection(ctx, queries, runID, "failed", nil, reason)
}

// updateAutopilotRunTerminalInProjection 用“带配额”的终态更新把 run 落到 completed/failed（completed 才消耗配额），
// 并在无行变化时回退到空结果。
func updateAutopilotRunTerminalInProjection(ctx context.Context, queries *db.Queries, runID pgtype.UUID, status string, result []byte, reason string) (db.AutopilotRun, error) {
	row, err := queries.UpdateAutopilotRunTerminalWithQuota(ctx, db.UpdateAutopilotRunTerminalWithQuotaParams{
		TerminalStatus: status,
		Result:         result,
		FailureReason:  pgtype.Text{String: reason, Valid: reason != ""},
		RunID:          runID,
		Consume:        status == "completed",
	})
	return autopilotRunFromTerminalRow(row), err
}

// terminalAutopilotRunEvent 组装 autopilot:run_done 事件；ErrNoRows 时返回 nil（无事件）。
func terminalAutopilotRunEvent(autopilot db.Autopilot, run db.AutopilotRun, status string, err error) (*events.Event, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("set autopilot run %s: %w", status, err)
	}
	event := events.Event{
		Type:           protocol.EventAutopilotRunDone,
		IdempotencyKey: "autopilot:run-done:" + util.UUIDToString(run.ID) + ":" + status,
		StreamKey:      "autopilot:" + util.UUIDToString(run.AutopilotID),
		WorkspaceID:    util.UUIDToString(autopilot.WorkspaceID),
		ActorType:      "system",
		Payload: map[string]any{
			"run_id":       util.UUIDToString(run.ID),
			"autopilot_id": util.UUIDToString(run.AutopilotID),
			"status":       status,
		},
	}
	return &event, nil
}

// CaptureAutopilotRunDone 只在 durable 投影事务提交并发出 run_done 之后记录 best-effort 分析数据。
func (s *AutopilotService) CaptureAutopilotRunDone(ctx context.Context, event events.Event) {
	if event.Type != protocol.EventAutopilotRunDone {
		return
	}
	var payload struct {
		RunID       string `json:"run_id"`
		AutopilotID string `json:"autopilot_id"`
		Status      string `json:"status"`
	}
	raw, err := json.Marshal(event.Payload)
	if err != nil {
		return
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return
	}
	runID, err := util.ParseUUID(payload.RunID)
	if err != nil {
		return
	}
	run, err := s.Queries.GetAutopilotRun(ctx, runID)
	if err != nil {
		slog.Debug("autopilot analytics: load run failed", "run_id", payload.RunID, "error", err)
		return
	}
	autopilot, err := s.Queries.GetAutopilot(ctx, run.AutopilotID)
	if err != nil {
		slog.Debug("autopilot analytics: load autopilot failed", "run_id", payload.RunID, "error", err)
		return
	}
	if payload.AutopilotID != util.UUIDToString(autopilot.ID) ||
		(event.WorkspaceID != "" && event.WorkspaceID != util.UUIDToString(autopilot.WorkspaceID)) ||
		payload.Status != run.Status {
		return
	}
	switch payload.Status {
	case "completed":
		s.captureAutopilotRunCompleted(autopilot, run)
	case "failed":
		reason := "unknown"
		if run.FailureReason.Valid && strings.TrimSpace(run.FailureReason.String) != "" {
			reason = run.FailureReason.String
		}
		s.captureAutopilotRunFailed(autopilot, run, run.Source, reason)
	case "skipped":
		if s.TaskSvc.Metrics != nil {
			reason := "unknown"
			if run.FailureReason.Valid && strings.TrimSpace(run.FailureReason.String) != "" {
				reason = run.FailureReason.String
			}
			s.TaskSvc.Metrics.RecordAutopilotRunSkipped(run.Source, reason)
		}
	}
}
