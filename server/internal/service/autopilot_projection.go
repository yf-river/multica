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
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ProjectAutopilotRunFromIssue applies the terminal state of an
// autopilot-created issue through the caller's transaction-scoped queries.
// A nil event means the issue is not an active autopilot run.
func ProjectAutopilotRunFromIssue(ctx context.Context, queries *db.Queries, issue db.Issue) (*events.Event, error) {
	if queries == nil {
		return nil, errors.New("project autopilot issue: queries are required")
	}
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

	switch issue.Status {
	case "done", "in_review":
		updated, err := queries.UpdateAutopilotRunCompleted(ctx, db.UpdateAutopilotRunCompletedParams{ID: run.ID})
		return terminalAutopilotRunEvent(autopilot, updated, "completed", err)
	case "cancelled", "blocked":
		reason := "issue " + issue.Status
		updated, err := queries.UpdateAutopilotRunFailed(ctx, db.UpdateAutopilotRunFailedParams{
			ID:            run.ID,
			FailureReason: pgtype.Text{String: reason, Valid: true},
		})
		return terminalAutopilotRunEvent(autopilot, updated, "failed", err)
	default:
		return nil, nil
	}
}

// ProjectAutopilotRunFromTask applies a terminal task state. It handles both
// run_only tasks linked directly to a run and create_issue tasks linked through
// an issue. The caller owns the transaction and delivery receipt.
func ProjectAutopilotRunFromTask(ctx context.Context, queries *db.Queries, task db.AgentTaskQueue) (*events.Event, error) {
	if queries == nil {
		return nil, errors.New("project autopilot task: queries are required")
	}
	if task.AutopilotRunID.Valid {
		return projectDirectAutopilotTask(ctx, queries, task)
	}
	if !task.IssueID.Valid || task.Status != "failed" {
		return nil, nil
	}
	return projectLinkedIssueAutopilotTask(ctx, queries, task)
}

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
		updated, err := queries.UpdateAutopilotRunCompleted(ctx, db.UpdateAutopilotRunCompletedParams{
			ID:     run.ID,
			Result: task.Result,
		})
		return terminalAutopilotRunEvent(autopilot, updated, "completed", err)
	case "failed", "cancelled":
		reason := taskFailureReasonForAutopilotRun(task)
		updated, err := queries.UpdateAutopilotRunFailed(ctx, db.UpdateAutopilotRunFailedParams{
			ID:            run.ID,
			FailureReason: pgtype.Text{String: reason, Valid: reason != ""},
		})
		return terminalAutopilotRunEvent(autopilot, updated, "failed", err)
	default:
		return nil, nil
	}
}

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
	reason := taskFailureReasonForAutopilotRun(task)
	updated, err := queries.UpdateAutopilotRunFailed(ctx, db.UpdateAutopilotRunFailedParams{
		ID:            run.ID,
		FailureReason: pgtype.Text{String: reason, Valid: reason != ""},
	})
	return terminalAutopilotRunEvent(autopilot, updated, "failed", err)
}

func terminalAutopilotRunEvent(autopilot db.Autopilot, run db.AutopilotRun, status string, err error) (*events.Event, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("set autopilot run %s: %w", status, err)
	}
	event := events.Event{
		Type:        protocol.EventAutopilotRunDone,
		WorkspaceID: util.UUIDToString(autopilot.WorkspaceID),
		ActorType:   "system",
		Payload: map[string]any{
			"run_id":       util.UUIDToString(run.ID),
			"autopilot_id": util.UUIDToString(run.AutopilotID),
			"status":       status,
		},
	}
	return &event, nil
}

func taskFailureReasonForAutopilotRun(task db.AgentTaskQueue) string {
	if task.Error.Valid && strings.TrimSpace(task.Error.String) != "" {
		return task.Error.String
	}
	if task.FailureReason.Valid && strings.TrimSpace(task.FailureReason.String) != "" {
		return task.FailureReason.String
	}
	if task.Status == "cancelled" {
		return "task cancelled"
	}
	return "task failed"
}

// CaptureAutopilotRunDone records best-effort analytics only after the
// durable projection transaction has committed and emitted run_done.
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
		reason := ""
		if run.FailureReason.Valid {
			reason = run.FailureReason.String
		}
		s.captureAutopilotRunFailed(autopilot, run, run.Source, reason)
	}
}
