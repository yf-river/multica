package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/redact"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

func (s *TaskService) ClaimTask(ctx context.Context, agentID pgtype.UUID) (*db.AgentTaskQueue, error) {
	start := time.Now()
	var (
		outcome                                                              = "unknown"
		getAgentMs, countRunningMs, claimAgentMs, updateStatusMs, dispatchMs int64
	)
	defer func() {
		s.maybeLogClaimSlow(agentID, outcome, start, getAgentMs, countRunningMs, claimAgentMs, updateStatusMs, dispatchMs)
	}()

	t0 := start
	agent, err := s.Queries.GetAgent(ctx, agentID)
	getAgentMs = time.Since(t0).Milliseconds()
	if err != nil {
		outcome = "error_get_agent"
		return nil, fmt.Errorf("agent not found: %w", err)
	}

	t0 = time.Now()
	running, err := s.Queries.CountRunningTasks(ctx, agentID)
	countRunningMs = time.Since(t0).Milliseconds()
	if err != nil {
		outcome = "error_count_running"
		return nil, fmt.Errorf("count running tasks: %w", err)
	}
	if running >= int64(agent.MaxConcurrentTasks) {
		slog.Debug("task claim: no capacity", "agent_id", util.UUIDToString(agentID), "running", running, "max", agent.MaxConcurrentTasks)
		outcome = "no_capacity"
		return nil, nil // No capacity
	}

	t0 = time.Now()
	task, err := s.Queries.ClaimAgentTask(ctx, agentID)
	claimAgentMs = time.Since(t0).Milliseconds()
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			slog.Debug("task claim: no tasks available", "agent_id", util.UUIDToString(agentID))
			outcome = "no_tasks"
			return nil, nil // No tasks available
		}
		outcome = "error_claim"
		return nil, fmt.Errorf("claim task: %w", err)
	}

	slog.Info("task claimed", "task_id", util.UUIDToString(task.ID), "agent_id", util.UUIDToString(agentID))
	s.captureTaskDispatched(ctx, task)

	// Refresh agent status from active tasks. This avoids a stale unconditional
	// working write racing after a just-cancelled claim.
	t0 = time.Now()
	s.ReconcileAgentStatus(ctx, agentID)
	updateStatusMs = time.Since(t0).Milliseconds()

	// Broadcast task:dispatch. ResolveTaskWorkspaceID inside this path can
	// re-query issue/chat_session/autopilot_run, so it can also be a real
	// contributor to claim latency.
	t0 = time.Now()
	s.broadcastTaskDispatch(ctx, task)
	dispatchMs = time.Since(t0).Milliseconds()

	outcome = "claimed"
	return &task, nil
}

// ClaimTaskForRuntime claims the next runnable task for a runtime while
// still respecting each agent's max_concurrent_tasks limit.
//
// Empty-claim fast path: when EmptyClaim is configured and a recent
// check verified the runtime had no queued tasks, returns immediately
// without touching Postgres. The cache is invalidated synchronously on
// every enqueue (notifyTaskAvailable), so a queued task becomes
// claimable on the next call rather than waiting for the TTL.
func (s *TaskService) ClaimTaskForRuntime(ctx context.Context, runtimeID pgtype.UUID) (*db.AgentTaskQueue, error) {
	start := time.Now()
	var (
		outcome          = "no_task"
		listMs, loopMs   int64
		listCount, tried int
		claimedFlag      bool
	)
	defer func() {
		totalMs := time.Since(start).Milliseconds()
		if totalMs < 300 {
			return
		}
		slog.Info("claim_for_runtime slow",
			"runtime_id", util.UUIDToString(runtimeID),
			"outcome", outcome,
			"total_ms", totalMs,
			"list_pending_ms", listMs,
			"list_pending_count", listCount,
			"agents_tried", tried,
			"claim_loop_ms", loopMs,
			"claimed", claimedFlag,
		)
	}()

	runtimeKey := util.UUIDToString(runtimeID)
	// Check this before EmptyClaim: a lost claim response moves the task out of
	// `queued`, so the empty-queued cache cannot represent recoverability.
	stale, err := s.Queries.ReclaimStaleDispatchedTaskForRuntime(ctx, db.ReclaimStaleDispatchedTaskForRuntimeParams{
		RuntimeID:         runtimeID,
		ClaimRecoverySecs: claimResponseRecoveryWindow.Seconds(),
	})
	if err == nil {
		outcome = "reclaimed_dispatched"
		claimedFlag = true
		slog.Info("stale dispatched task reclaimed",
			"task_id", util.UUIDToString(stale.ID),
			"runtime_id", runtimeKey,
			"agent_id", util.UUIDToString(stale.AgentID),
		)
		return &stale, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		outcome = "error_reclaim_dispatched"
		return nil, fmt.Errorf("reclaim stale dispatched task: %w", err)
	}

	if s.EmptyClaim.IsEmpty(ctx, runtimeKey) {
		outcome = "empty_cache_hit"
		return nil, nil
	}

	// Sample the invalidation version BEFORE the SELECT. If a
	// concurrent enqueue Bumps between this read and the post-SELECT
	// MarkEmpty, the next IsEmpty will see the empty key tagged with
	// a stale version and reject it — closing the race that would
	// otherwise stall the just-queued task until the empty key's TTL
	// expired.
	preSelectVersion := s.EmptyClaim.CurrentVersion(ctx, runtimeKey)

	t0 := time.Now()
	tasks, err := s.Queries.ListQueuedClaimCandidatesByRuntime(ctx, runtimeID)
	listMs = time.Since(t0).Milliseconds()
	listCount = len(tasks)
	if err != nil {
		outcome = "error_list"
		return nil, fmt.Errorf("list queued claim candidates: %w", err)
	}

	if len(tasks) == 0 {
		s.EmptyClaim.MarkEmpty(ctx, runtimeKey, preSelectVersion)
		outcome = "empty_db"
		return nil, nil
	}

	loopStart := time.Now()
	triedAgents := map[string]struct{}{}
	var claimed *db.AgentTaskQueue
	for _, candidate := range tasks {
		agentKey := util.UUIDToString(candidate.AgentID)
		if _, seen := triedAgents[agentKey]; seen {
			continue
		}
		triedAgents[agentKey] = struct{}{}
		tried++

		task, err := s.ClaimTask(ctx, candidate.AgentID)
		if err != nil {
			loopMs = time.Since(loopStart).Milliseconds()
			outcome = "error_claim"
			return nil, err
		}
		if task != nil && task.RuntimeID == runtimeID {
			claimed = task
			break
		}
	}
	loopMs = time.Since(loopStart).Milliseconds()
	if claimed != nil {
		claimedFlag = true
		outcome = "claimed"
	}

	return claimed, nil
}

// maybeLogClaimSlow emits one structured log per ClaimTask call when its total
// latency exceeds 300ms, so the prod tail can be diagnosed without flooding
// logs at normal poll rates. Called via defer so it captures the full path
// including post-claim updateAgentStatus / broadcastTaskDispatch (both of
// which can hit the DB) and any error exit.
func (s *TaskService) maybeLogClaimSlow(agentID pgtype.UUID, outcome string, start time.Time, getAgentMs, countRunningMs, claimAgentMs, updateStatusMs, dispatchMs int64) {
	totalMs := time.Since(start).Milliseconds()
	if totalMs < 300 {
		return
	}
	slog.Info("claim_task slow",
		"agent_id", util.UUIDToString(agentID),
		"outcome", outcome,
		"total_ms", totalMs,
		"get_agent_ms", getAgentMs,
		"count_running_ms", countRunningMs,
		"claim_agent_ms", claimAgentMs,
		"update_status_ms", updateStatusMs,
		"dispatch_ms", dispatchMs,
	)
}

// StartTask transitions a dispatched task to running.
// For assignment-triggered issue tasks, the platform also advances the issue
// from todo to in_progress. This is mechanical execution state, not workflow
// semantics, so it should not depend on the agent remembering a CLI call.
func (s *TaskService) StartTask(ctx context.Context, taskID pgtype.UUID) (*db.AgentTaskQueue, error) {
	task, err := s.Queries.StartAgentTask(ctx, taskID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if existing, getErr := s.Queries.GetAgentTask(ctx, taskID); getErr == nil {
				return nil, TaskStartConflictError{Status: existing.Status}
			}
		}
		return nil, fmt.Errorf("start task: %w", err)
	}

	slog.Info("task started", "task_id", util.UUIDToString(task.ID), "issue_id", util.UUIDToString(task.IssueID))
	s.autoStartIssueForTask(ctx, task)
	s.captureTaskStarted(ctx, task)
	s.syncSquadSOPTaskStep(ctx, task, "步骤开始", "进行中")
	// Tell every connected workspace WS client that this task transitioned
	// (dispatched | waiting_local_directory) → running. Without this, the
	// workspace-wide `agentTaskSnapshot` query only refreshes on the 30s
	// staleTime, so any UI that distinguishes "queued" from "running" (e.g.
	// the issue-card agent activity indicator) lags by up to half a minute
	// on the transition users care about most.
	s.broadcastTaskEvent(ctx, protocol.EventTaskRunning, task)
	return &task, nil
}

type squadSOPProfileStep struct {
	Key     string `json:"key"`
	Name    string `json:"name"`
	RoleKey string `json:"role_key"`
}

type squadSOPProfile struct {
	Steps []squadSOPProfileStep `json:"steps"`
}

func (s *TaskService) syncSquadSOPTaskStep(ctx context.Context, task db.AgentTaskQueue, eventType, eventStatus string) {
	s.syncSquadSOPTaskStepWithResult(ctx, task, eventType, eventStatus, nil)
}

func (s *TaskService) syncSquadSOPTaskStepWithResult(ctx context.Context, task db.AgentTaskQueue, eventType, eventStatus string, result []byte) {
	if !task.IssueID.Valid {
		return
	}
	issue, err := s.Queries.GetIssue(ctx, task.IssueID)
	if err != nil || !issue.AssigneeType.Valid || issue.AssigneeType.String != "squad" || !issue.AssigneeID.Valid {
		return
	}
	run, err := s.Queries.GetOpenSquadSOPRunByIssue(ctx, task.IssueID)
	if err != nil {
		return
	}
	agent, err := s.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID:          task.AgentID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		slog.Warn("sync squad SOP task step skipped: agent not found",
			"task_id", util.UUIDToString(task.ID),
			"issue_id", util.UUIDToString(task.IssueID),
			"agent_id", util.UUIDToString(task.AgentID),
			"error", err,
		)
		return
	}
	steps := parseSquadSOPProfileSteps(run.Profile)
	step, index, ok := matchSquadSOPStepForAgentRecord(steps, agent)
	if !ok {
		return
	}
	events, err := s.Queries.ListSquadSOPStepEventsByRun(ctx, run.ID)
	if err != nil {
		slog.Warn("sync squad SOP task step skipped: list existing events failed",
			"run_id", util.UUIDToString(run.ID),
			"task_id", util.UUIDToString(task.ID),
			"error", err,
		)
		return
	}
	for _, existing := range events {
		if existing.TaskID.Valid && existing.TaskID == task.ID && existing.EventType == eventType {
			return
		}
	}

	var duration pgtype.Int8
	if eventType != "步骤开始" && task.StartedAt.Valid && task.CompletedAt.Valid {
		duration = pgtype.Int8{Int64: task.CompletedAt.Time.Sub(task.StartedAt.Time).Milliseconds(), Valid: true}
	}
	reason := "Agent task 状态自动同步到 SOP 阶段。"
	switch eventType {
	case "步骤开始":
		reason = "Agent task 已开始，自动记录 SOP 阶段开始。"
	case "步骤完成":
		reason = "Agent task 已完成，自动记录 SOP 阶段完成。"
	case "步骤失败":
		reason = "Agent task 已失败，自动记录 SOP 阶段失败。"
	}
	if _, err := s.Queries.CreateSquadSOPStepEvent(ctx, db.CreateSquadSOPStepEventParams{
		RunID:         run.ID,
		WorkspaceID:   run.WorkspaceID,
		IssueID:       run.IssueID,
		SquadID:       run.SquadID,
		StepKey:       step.Key,
		StepName:      step.Name,
		RoleKey:       step.RoleKey,
		EventType:     eventType,
		Status:        eventStatus,
		Reason:        reason,
		DurationMs:    duration,
		CreatedByType: "system",
		TaskID:        task.ID,
	}); err != nil {
		slog.Warn("sync squad SOP task step event failed",
			"run_id", util.UUIDToString(run.ID),
			"task_id", util.UUIDToString(task.ID),
			"step_key", step.Key,
			"event_type", eventType,
			"error", err,
		)
		return
	}

	nextStatus, nextStepKey, shouldUpdate := nextSquadSOPStateForTaskEvent(issue, steps, index, step.Key, eventType)
	if !shouldUpdate {
		return
	}
	finalStepBlocked := eventType == "步骤完成" && nextStatus == "已完成" && squadSOPFinalOutputBlocked(result)
	if finalStepBlocked {
		nextStatus = "已阻塞"
	}
	updatedRun, err := s.Queries.UpdateSquadSOPRunStatus(ctx, db.UpdateSquadSOPRunStatusParams{
		ID:             run.ID,
		WorkspaceID:    run.WorkspaceID,
		Status:         nextStatus,
		CurrentStepKey: pgtype.Text{String: nextStepKey, Valid: nextStepKey != ""},
	})
	if err != nil {
		slog.Warn("sync squad SOP run status failed",
			"run_id", util.UUIDToString(run.ID),
			"task_id", util.UUIDToString(task.ID),
			"step_key", step.Key,
			"event_type", eventType,
			"error", err,
		)
		return
	}
	if eventType == "步骤完成" && updatedRun.Status == "已完成" {
		s.closeIssueAfterCompletedSOPRun(ctx, issue)
	}
	if eventType == "步骤完成" && updatedRun.Status == "已阻塞" {
		s.blockIssueAfterBlockedSOPRun(ctx, issue)
	}
	if eventType == "步骤失败" && updatedRun.Status == "已失败" {
		s.blockIssueAfterBlockedSOPRun(ctx, issue)
	}
}

func (s *TaskService) squadSOPFailureComment(ctx context.Context, task db.AgentTaskQueue, errMsg, failureReason string) (string, bool) {
	if !task.IssueID.Valid {
		return "", false
	}
	issue, err := s.Queries.GetIssue(ctx, task.IssueID)
	if err != nil || !issue.AssigneeType.Valid || issue.AssigneeType.String != "squad" {
		return "", false
	}
	run, err := s.Queries.GetOpenSquadSOPRunByIssue(ctx, task.IssueID)
	if err != nil {
		return "", false
	}
	agent, err := s.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID:          task.AgentID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return "", false
	}
	steps := parseSquadSOPProfileSteps(run.Profile)
	step, _, ok := matchSquadSOPStepForAgentRecord(steps, agent)
	if !ok {
		return "", false
	}
	reason := strings.TrimSpace(failureReason)
	if reason == "" {
		reason = taskfailure.Classify(errMsg).String()
	}
	var b strings.Builder
	b.WriteString("## 阶段执行失败\n\n")
	fmt.Fprintf(&b, "- 阶段：%s\n", step.Name)
	fmt.Fprintf(&b, "- Agent：%s\n", agent.Name)
	fmt.Fprintf(&b, "- Task：%s\n", util.UUIDToString(task.ID))
	fmt.Fprintf(&b, "- 失败类型：%s\n", reason)
	b.WriteString("- 处理结果：SOP 运行已标记为失败，当前 issue 已阻塞，等待 PM 或人工确认后重试。\n")
	if summary := taskFailureSummary(errMsg); summary != "" {
		fmt.Fprintf(&b, "- 错误摘要：%s\n", summary)
	}
	return b.String(), true
}

func (s *TaskService) squadSOPTaskHasDeliveryComment(ctx context.Context, task db.AgentTaskQueue) bool {
	if !task.IssueID.Valid {
		return false
	}
	issue, err := s.Queries.GetIssue(ctx, task.IssueID)
	if err != nil || !issue.AssigneeType.Valid || issue.AssigneeType.String != "squad" {
		return false
	}
	run, ok := s.squadSOPRunForWorkerTask(ctx, task, issue)
	if !ok {
		return false
	}
	agent, err := s.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID:          task.AgentID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return false
	}
	if _, _, ok := matchSquadSOPStepForAgentRecord(parseSquadSOPProfileSteps(run.Profile), agent); !ok {
		return false
	}
	comments, err := s.Queries.ListCommentsForIssue(ctx, db.ListCommentsForIssueParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Limit:       2000,
	})
	if err != nil {
		slog.Warn("squad SOP delivery comment lookup failed",
			"task_id", util.UUIDToString(task.ID),
			"issue_id", util.UUIDToString(task.IssueID),
			"error", err,
		)
		return false
	}
	for _, comment := range comments {
		if comment.AuthorType == "agent" &&
			comment.SourceTaskID.Valid &&
			comment.SourceTaskID == task.ID &&
			comment.Type != "system" &&
			strings.TrimSpace(comment.Content) != "" {
			return true
		}
	}
	return false
}

func taskFailureSummary(errMsg string) string {
	errMsg = strings.TrimSpace(redact.Text(errMsg))
	if errMsg == "" {
		return ""
	}
	errMsg = strings.Join(strings.Fields(errMsg), " ")
	if len([]rune(errMsg)) > 240 {
		return "原始错误较长，已保留在任务运行记录中。"
	}
	return errMsg
}

func (s *TaskService) closeIssueAfterCompletedSOPRun(ctx context.Context, issue db.Issue) {
	switch issue.Status {
	case "done", "cancelled", "blocked":
		return
	}
	pullRequests, err := s.Queries.ListPullRequestsByIssue(ctx, issue.ID)
	if err != nil {
		slog.Warn("auto-close issue after completed SOP skipped: pull request lookup failed",
			"issue_id", util.UUIDToString(issue.ID),
			"error", err,
		)
		return
	}
	if s.issueRequiresGongfengMR(ctx, issue) {
		if len(pullRequests) > 0 {
			if _, err := s.updateIssueStatusAfterCompletedSOPRun(ctx, issue, "done"); err != nil {
				slog.Warn("auto-close issue after completed SOP with linked MR failed",
					"issue_id", util.UUIDToString(issue.ID),
					"error", err,
				)
				return
			}
			slog.Info("issue auto-closed after completed SOP run with linked MR", "issue_id", util.UUIDToString(issue.ID))
			return
		}
		blockedIssue, err := s.updateIssueStatusAfterCompletedSOPRun(ctx, issue, "blocked")
		if err != nil {
			slog.Warn("block issue after completed SOP without MR failed",
				"issue_id", util.UUIDToString(issue.ID),
				"error", err,
			)
			return
		}
		s.recordMissingMRGateComment(ctx, blockedIssue)
		slog.Info("issue blocked after completed SOP run without linked MR", "issue_id", util.UUIDToString(issue.ID))
		return
	}
	if len(pullRequests) > 0 {
		return
	}
	if _, err := s.updateIssueStatusAfterCompletedSOPRun(ctx, issue, "done"); err != nil {
		slog.Warn("auto-close issue after completed SOP failed",
			"issue_id", util.UUIDToString(issue.ID),
			"error", err,
		)
		return
	}
	slog.Info("issue auto-closed after completed SOP run", "issue_id", util.UUIDToString(issue.ID))
}

func (s *TaskService) blockIssueAfterBlockedSOPRun(ctx context.Context, issue db.Issue) {
	switch issue.Status {
	case "done", "cancelled", "blocked":
		return
	}
	if _, err := s.updateIssueStatusAfterCompletedSOPRun(ctx, issue, "blocked"); err != nil {
		slog.Warn("block issue after blocked SOP run failed",
			"issue_id", util.UUIDToString(issue.ID),
			"error", err,
		)
		return
	}
	slog.Info("issue blocked after blocked SOP run", "issue_id", util.UUIDToString(issue.ID))
}

func (s *TaskService) updateIssueStatusAfterCompletedSOPRun(ctx context.Context, issue db.Issue, status string) (db.Issue, error) {
	if status == "done" {
		incomplete, err := s.countIncompleteChildIssues(ctx, issue)
		if err != nil {
			return db.Issue{}, err
		}
		if incomplete > 0 {
			return db.Issue{}, fmt.Errorf("%w: %d incomplete child issue(s)", errIssueDoneBlockedByChildren, incomplete)
		}
	}
	updated, err := s.persistIssueUpdate(ctx, issue, taskIssueUpdateChanges{Status: true}, func(queries *db.Queries) (db.Issue, error) {
		return queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
			ID:          issue.ID,
			Status:      status,
			WorkspaceID: issue.WorkspaceID,
		})
	})
	if err != nil {
		return db.Issue{}, err
	}
	if s.IssueStatusChanged != nil {
		s.IssueStatusChanged(ctx, issue, updated, "system", "")
	}
	return updated, nil
}

func (s *TaskService) countIncompleteChildIssues(ctx context.Context, issue db.Issue) (int, error) {
	children, err := s.Queries.ListChildIssues(ctx, issue.ID)
	if err != nil {
		return 0, err
	}
	incomplete := 0
	for _, child := range children {
		if child.Status != "done" {
			incomplete++
		}
	}
	return incomplete, nil
}

func (s *TaskService) autoStartIssueForTask(ctx context.Context, task db.AgentTaskQueue) {
	if !shouldAutoStartIssueForTask(task) {
		return
	}
	s.autoTransitionIssueStatus(ctx, task, "todo", "in_progress", "task_started")
}

func (s *TaskService) autoReviewIssueForTask(ctx context.Context, task db.AgentTaskQueue) {
	if !shouldConsiderAutoReviewIssueForTask(task) {
		return
	}
	issue, ok := s.issueForTaskStatusAutomation(ctx, task)
	if !ok || !shouldAutoReviewIssueForTask(task, issue) || issue.Status != "in_progress" {
		return
	}
	s.updateIssueStatusForTaskAutomation(ctx, task, issue, "in_review", "task_completed")
}

func (s *TaskService) autoBlockIssueForTaskFailure(ctx context.Context, task db.AgentTaskQueue) {
	if !shouldAutoBlockIssueForTaskFailure(task) {
		return
	}
	hasActive, err := s.Queries.HasActiveTaskForIssue(ctx, task.IssueID)
	if err != nil {
		slog.Warn("task failure issue status automation skipped: active task check failed",
			"task_id", util.UUIDToString(task.ID),
			"issue_id", util.UUIDToString(task.IssueID),
			"error", err,
		)
		return
	}
	if hasActive {
		return
	}
	s.autoTransitionIssueStatus(ctx, task, "in_progress", "blocked", "task_failed")
}

func (s *TaskService) autoTransitionIssueStatus(ctx context.Context, task db.AgentTaskQueue, fromStatus, toStatus, reason string) {
	issue, ok := s.issueForTaskStatusAutomation(ctx, task)
	if !ok || issue.Status != fromStatus {
		return
	}
	s.updateIssueStatusForTaskAutomation(ctx, task, issue, toStatus, reason)
}

func (s *TaskService) issueForTaskStatusAutomation(ctx context.Context, task db.AgentTaskQueue) (db.Issue, bool) {
	if !task.IssueID.Valid {
		return db.Issue{}, false
	}
	issue, err := s.Queries.GetIssue(ctx, task.IssueID)
	if err != nil {
		slog.Warn("task issue status automation skipped: issue lookup failed",
			"task_id", util.UUIDToString(task.ID),
			"issue_id", util.UUIDToString(task.IssueID),
			"error", err,
		)
		return db.Issue{}, false
	}
	return issue, true
}

func (s *TaskService) updateIssueStatusForTaskAutomation(ctx context.Context, task db.AgentTaskQueue, issue db.Issue, status string, reason string) {
	updated, err := s.persistIssueUpdate(ctx, issue, taskIssueUpdateChanges{Status: true}, func(queries *db.Queries) (db.Issue, error) {
		return queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
			ID:          issue.ID,
			Status:      status,
			WorkspaceID: issue.WorkspaceID,
		})
	})
	if err != nil {
		slog.Warn("task issue status automation failed",
			"task_id", util.UUIDToString(task.ID),
			"issue_id", util.UUIDToString(issue.ID),
			"from_status", issue.Status,
			"to_status", status,
			"reason", reason,
			"error", err,
		)
		return
	}
	slog.Info("task issue status automated",
		"task_id", util.UUIDToString(task.ID),
		"issue_id", util.UUIDToString(issue.ID),
		"from_status", issue.Status,
		"to_status", status,
		"reason", reason,
	)
	if s.IssueStatusChanged != nil {
		s.IssueStatusChanged(ctx, issue, updated, "system", "")
	}
}

func shouldAutoStartIssueForTask(task db.AgentTaskQueue) bool {
	return isAssignmentIssueTaskForStatusAutomation(task)
}

func shouldConsiderAutoReviewIssueForTask(task db.AgentTaskQueue) bool {
	return isAssignmentIssueTaskForStatusAutomation(task) && !task.IsLeaderTask
}

func shouldAutoReviewIssueForTask(task db.AgentTaskQueue, issue db.Issue) bool {
	if !shouldConsiderAutoReviewIssueForTask(task) {
		return false
	}
	return issue.AssigneeType.Valid &&
		issue.AssigneeType.String == "agent" &&
		issue.AssigneeID.Valid &&
		issue.AssigneeID == task.AgentID
}

func shouldAutoBlockIssueForTaskFailure(task db.AgentTaskQueue) bool {
	return isAssignmentIssueTaskForStatusAutomation(task)
}

func isAssignmentIssueTaskForStatusAutomation(task db.AgentTaskQueue) bool {
	if !task.IssueID.Valid ||
		task.TriggerCommentID.Valid ||
		task.ChatSessionID.Valid ||
		task.AutopilotRunID.Valid {
		return false
	}
	if _, ok := ParseIssueSourceSummaryContext(task); ok {
		return false
	}
	return true
}

func (s *TaskService) issueRequiresGongfengMR(ctx context.Context, issue db.Issue) bool {
	if !issue.ProjectID.Valid {
		return false
	}
	resources, err := s.Queries.ListProjectResources(ctx, issue.ProjectID)
	if err != nil {
		slog.Warn("detect issue gongfeng MR requirement failed",
			"issue_id", util.UUIDToString(issue.ID),
			"project_id", util.UUIDToString(issue.ProjectID),
			"error", err,
		)
		return false
	}
	for _, resource := range resources {
		if resource.ResourceType == "gongfeng_repo" {
			return true
		}
	}
	return false
}

func (s *TaskService) recordMissingMRGateComment(ctx context.Context, issue db.Issue) {
	content := strings.TrimSpace(`05 验证已完成，但平台还没有关联 MR。请通过平台创建并关联 MR 后再进入人工 CodeReview：

multica issue mr create <issue-id> --provider gongfeng --project-path <project-path> --source-branch <branch> --target-branch <target-branch> --title "<title>" --output json

创建成功后，平台会把 MR 写入任务的关联 MR 区域。`)
	var comment db.Comment
	var createdEvent events.Event
	err := s.runInTx(ctx, func(queries *db.Queries) error {
		var err error
		comment, err = queries.CreateComment(ctx, db.CreateCommentParams{
			IssueID:     issue.ID,
			WorkspaceID: issue.WorkspaceID,
			AuthorType:  "system",
			AuthorID:    util.MustParseUUID("00000000-0000-0000-0000-000000000000"),
			Content:     content,
			Type:        "comment",
		})
		if err != nil {
			return err
		}
		createdEvent = taskCommentCreatedEvent(issue, comment, "system", "")
		createdEvent, err = eventoutbox.Enqueue(ctx, queries, createdEvent)
		return err
	})
	if err != nil {
		slog.Warn("create missing MR gate comment transaction failed",
			"issue_id", util.UUIDToString(issue.ID),
			"error", err,
		)
		return
	}
	s.Bus.Publish(createdEvent)
}

func parseSquadSOPProfileSteps(raw []byte) []squadSOPProfileStep {
	var profile squadSOPProfile
	if len(raw) == 0 || json.Unmarshal(raw, &profile) != nil {
		return nil
	}
	return profile.Steps
}

func matchSquadSOPStepForAgentRecord(steps []squadSOPProfileStep, agent db.Agent) (squadSOPProfileStep, int, bool) {
	if roleKey := roleKeyFromAgentRuntimeConfig(agent.RuntimeConfig); roleKey != "" {
		if step, index, ok := matchSquadSOPStepForAgent(steps, roleKey); ok {
			return step, index, true
		}
	}
	return matchSquadSOPStepForAgent(steps, agent.Name)
}

func roleKeyFromAgentRuntimeConfig(raw []byte) string {
	var runtimeConfig map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &runtimeConfig) != nil {
		return ""
	}
	if scope, ok := runtimeConfig["internal_squad"].(map[string]any); ok {
		return stringFromAny(scope["role_key"])
	}
	return ""
}

func matchSquadSOPStepForAgent(steps []squadSOPProfileStep, agentNameOrRoleKey string) (squadSOPProfileStep, int, bool) {
	agentKey := normalizeSOPMatchKey(agentNameOrRoleKey)
	if agentKey == "" {
		return squadSOPProfileStep{}, -1, false
	}
	for i, step := range steps {
		if agentKey == normalizeSOPMatchKey(step.RoleKey) ||
			agentKey == normalizeSOPMatchKey(step.Key) ||
			agentKey == normalizeSOPMatchKey(step.Name) {
			return step, i, true
		}
	}
	return squadSOPProfileStep{}, -1, false
}

func normalizeSOPMatchKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "-")
	return strings.Trim(value, "-")
}

func nextSquadSOPStateForTaskEvent(issue db.Issue, steps []squadSOPProfileStep, stepIndex int, stepKey string, eventType string) (status, currentStepKey string, ok bool) {
	switch eventType {
	case "步骤开始":
		return "进行中", stepKey, true
	case "步骤失败":
		return "已失败", stepKey, true
	case "步骤完成":
		if issue.Status == "done" {
			return "已完成", stepKey, true
		}
		if len(steps) > 0 && stepIndex >= 0 && stepIndex < len(steps)-1 {
			return "进行中", steps[stepIndex+1].Key, true
		}
		if len(steps) == 0 || stepIndex == len(steps)-1 {
			return "已完成", stepKey, true
		}
	}
	return "", "", false
}

var squadSOPBlockedOutputPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?im)(?:最终判定|总体结论|验证结论|V[123][^\n|]*)[^\n|]*(?:BLOCKED|FAILED|FAIL|未通过|阻塞)`),
	regexp.MustCompile(`(?im)(?:结论|结果|判定)\s*[：:]\s*(?:❌\s*)?(?:BLOCKED|FAILED|FAIL|未通过|阻塞)`),
	regexp.MustCompile(`(?im)(?:\|\s*V[123][^|\n]*\|\s*[^|\n]*\|\s*)?(?:❌\s*)?(?:BLOCKED|FAILED|FAIL|未通过)\s*\|`),
}

func squadSOPFinalOutputBlocked(result []byte) bool {
	body := taskResultOutputText(result)
	if body == "" {
		return false
	}
	for _, pattern := range squadSOPBlockedOutputPatterns {
		if pattern.MatchString(body) {
			return true
		}
	}
	return false
}

func taskResultOutputText(result []byte) string {
	var payload protocol.TaskCompletedPayload
	if err := json.Unmarshal(result, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(util.UnescapeBackslashEscapes(payload.Output))
}

// MarkTaskWaitingLocalDirectory parks a dispatched task in the
// waiting_local_directory state while the daemon waits for another in-flight
// task to release the project_resource path lock. reason carries a short
// human-readable hint (typically the contested path) that the UI surfaces
// next to the status. Returns the updated row so the daemon can confirm the
// transition and so the broadcast carries the up-to-date snapshot.
func (s *TaskService) MarkTaskWaitingLocalDirectory(ctx context.Context, taskID pgtype.UUID, reason string) (*db.AgentTaskQueue, error) {
	reason = strings.TrimSpace(reason)
	task, err := s.Queries.MarkAgentTaskWaitingLocalDirectory(ctx, db.MarkAgentTaskWaitingLocalDirectoryParams{
		ID:         taskID,
		WaitReason: pgtype.Text{String: reason, Valid: reason != ""},
	})
	if err != nil {
		return nil, fmt.Errorf("mark task waiting_local_directory: %w", err)
	}

	slog.Info("task waiting_local_directory",
		"task_id", util.UUIDToString(task.ID),
		"issue_id", util.UUIDToString(task.IssueID),
		"reason", reason,
	)
	metadata, _ := json.Marshal(map[string]string{"等待原因": reason})
	s.recordTaskTraceEvent(ctx, task, "task.waiting_local_directory", "等待本地目录", taskTraceOptions{
		QueueWaitMs: taskQueueWaitMilliseconds(task),
		Metadata:    metadata,
	})
	s.broadcastTaskEvent(ctx, protocol.EventTaskWaitingLocalDirectory, task)
	return &task, nil
}

// CompleteTask marks a task as completed.
// For ordinary agent-assignment issue tasks, the platform also advances the
// issue from in_progress to in_review. Coordinators, comment replies, chat,
// quick-create, source-summary, and autopilot tasks are intentionally excluded.
//
// For chat tasks, CompleteAgentTask and the chat_session resume-pointer
// update run in a single transaction. This closes a race where the next
// queued chat message could be claimed in the window between the task
// flipping to 'completed' and chat_session.session_id being refreshed,
// causing the new task to resume against a stale (or NULL) session.
