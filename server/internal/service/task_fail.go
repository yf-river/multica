package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

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

func (s *TaskService) FailTask(ctx context.Context, taskID pgtype.UUID, errMsg, sessionID, workDir, failureReason string) (*db.AgentTaskQueue, error) {
	// MUL-2946: synthesise a refined reason from the error text whenever the
	// caller didn't supply one. This is the last write-path guard against
	// "agent_error" coarse rows ending up in agent_task_queue.failure_reason
	// — every other path either provides a classified reason directly
	// (sweepers writing 'queued_expired' / 'runtime_offline' / 'timeout'
	// / 'runtime_recovery' via SQL) or runs the daemon's classifyPoisonedError
	// + taskfailure.Classify chain.
	if failureReason == "" {
		failureReason = taskfailure.Classify(errMsg).String()
	}
	var task db.AgentTaskQueue
	var failedEvent events.Event
	var retried *db.AgentTaskQueue
	var retryCreated bool
	var sourceSummary *issueSourceSummaryProjection
	var deliveryCommentPosted bool
	var failureComment *agentCommentProjection
	var issueStatus *taskIssueStatusProjection
	var sopProjection *squadSOPTerminalProjection
	terminalTransitioned := false
	if err := s.runInTx(ctx, func(qtx *db.Queries) error {
		t, err := qtx.FailAgentTask(ctx, db.FailAgentTaskParams{
			ID:            taskID,
			Error:         pgtype.Text{String: errMsg, Valid: true},
			FailureReason: pgtype.Text{String: failureReason, Valid: failureReason != ""},
			SessionID:     pgtype.Text{String: sessionID, Valid: sessionID != ""},
			WorkDir:       pgtype.Text{String: workDir, Valid: workDir != ""},
		})
		if err != nil {
			return err
		}
		task = t
		terminalTransitioned = true

		// Keep resume-unsafe sessions on the task row for observability, but
		// do not promote them to the chat-level resume pointer.
		if t.ChatSessionID.Valid && !resumeUnsafeFailureReason(failureReason) {
			// Pin the chat_session's runtime_id alongside the session_id so the
			// next claim can apply the runtime-guard. Both fields move together:
			// when there's no session_id to record, leave runtime_id untouched
			// (NULL → COALESCE keeps the existing value).
			var sessionRuntimeID pgtype.UUID
			if sessionID != "" {
				sessionRuntimeID = t.RuntimeID
			}
			if err := qtx.UpdateChatSessionSession(ctx, db.UpdateChatSessionSessionParams{
				ID:        t.ChatSessionID,
				SessionID: pgtype.Text{String: sessionID, Valid: sessionID != ""},
				WorkDir:   pgtype.Text{String: workDir, Valid: workDir != ""},
				RuntimeID: sessionRuntimeID,
			}); err != nil {
				return fmt.Errorf("update chat session resume pointer: %w", err)
			}
		}
		retried, retryCreated, err = s.materializeRetryTask(ctx, qtx, task)
		if err != nil {
			return fmt.Errorf("materialize task retry: %w", err)
		}
		if err := lockIssueForTaskTerminalProjection(ctx, qtx, task); err != nil {
			return err
		}
		failedEvent, err = s.enqueueTaskEvent(ctx, qtx, protocol.EventTaskFailed, task)
		if err != nil {
			return err
		}
		if sc, ok := ParseIssueSourceSummaryContext(task); ok {
			projection, err := s.projectIssueSourceSummaryTask(ctx, qtx, task, sc, nil)
			if err != nil {
				return fmt.Errorf("project issue source summary failure: %w", err)
			}
			sourceSummary = &projection
			return nil
		}
		if retried == nil {
			deliveryCommentPosted, err = squadSOPTaskHasDeliveryComment(ctx, qtx, task)
			if err != nil {
				return fmt.Errorf("check squad SOP delivery comment: %w", err)
			}
			if errMsg != "" && task.IssueID.Valid && !deliveryCommentPosted {
				body := redact.Text(errMsg)
				if structured, ok, err := squadSOPFailureComment(ctx, qtx, task, errMsg, failureReason); err != nil {
					return fmt.Errorf("build squad SOP failure comment: %w", err)
				} else if ok {
					body = structured
				}
				failureComment, err = createAgentCommentInTx(
					ctx,
					qtx,
					task.IssueID,
					task.AgentID,
					body,
					"system",
					task.TriggerCommentID,
					task.ID,
				)
				if err != nil {
					return fmt.Errorf("create task failure comment: %w", err)
				}
			}
		}
		outcome := failedSquadSOPOutcome()
		if deliveryCommentPosted {
			if err := s.linkGongfengMRsFromTaskComments(ctx, qtx, task); err != nil {
				return fmt.Errorf("link Gongfeng merge requests from delivered failed task: %w", err)
			}
			outcome = completedSquadSOPOutcome(nil)
		}
		sopProjection, err = s.projectSquadSOPTerminal(ctx, qtx, task, outcome)
		if err != nil {
			return err
		}
		issueStatus, err = s.projectTaskFailureIssueStatus(ctx, qtx, task, deliveryCommentPosted)
		return err
	}); err != nil {
		if existing, lookupErr := s.Queries.GetAgentTask(ctx, taskID); lookupErr == nil {
			if !terminalTransitioned && errors.Is(err, pgx.ErrNoRows) && isTerminalTaskStatus(existing.Status) {
				repaired, repairErr := s.reconcileExistingSquadSOPTerminal(ctx, existing)
				if repairErr != nil {
					return nil, fmt.Errorf("fail task: repair terminal Squad SOP projection: %w", repairErr)
				}
				s.publishSquadSOPTerminalProjection(ctx, repaired)
				slog.Info("fail task: already finalized",
					"task_id", util.UUIDToString(taskID),
					"current_status", existing.Status,
					"agent_id", util.UUIDToString(existing.AgentID),
				)
				return &existing, nil
			}
			slog.Warn("fail task failed",
				"task_id", util.UUIDToString(taskID),
				"current_status", existing.Status,
				"issue_id", util.UUIDToString(existing.IssueID),
				"chat_session_id", util.UUIDToString(existing.ChatSessionID),
				"agent_id", util.UUIDToString(existing.AgentID),
				"error", err,
			)
		} else {
			slog.Warn("fail task failed: task not found",
				"task_id", util.UUIDToString(taskID),
				"lookup_error", lookupErr,
			)
		}
		return nil, fmt.Errorf("fail task: %w", err)
	}

	slog.Warn("task failed", "task_id", util.UUIDToString(task.ID), "issue_id", util.UUIDToString(task.IssueID), "error", errMsg, "failure_reason", failureReason)
	s.captureTaskFailed(ctx, task)
	if retryCreated {
		s.publishRetryTask(ctx, *retried)
	}
	if sourceSummary != nil {
		s.publishIssueSourceSummaryProjection(ctx, *sourceSummary)
		if _, err := s.ReconcileAgentStatus(ctx, task.AgentID); err != nil {
			slog.Warn("reconcile failed source-summary agent status failed", "agent_id", util.UUIDToString(task.AgentID), "error", err)
		}
		s.Bus.Publish(failedEvent)
		return &task, nil
	}

	s.publishSquadSOPTerminalProjection(ctx, sopProjection)
	if retried == nil && !deliveryCommentPosted {
		s.publishTaskIssueStatusProjection(ctx, issueStatus)
	}

	// Skip the per-failure system comment when we'll immediately retry —
	// the new task will surface its own status to the user, and we don't
	// want to spam the issue with "task timed out" messages on every
	// daemon hiccup.
	s.publishAgentCommentProjection(ctx, failureComment)

	// Reconcile agent status
	if _, err := s.ReconcileAgentStatus(ctx, task.AgentID); err != nil {
		slog.Warn("reconcile failed agent status failed", "agent_id", util.UUIDToString(task.AgentID), "error", err)
	}

	// Broadcast
	s.Bus.Publish(failedEvent)

	return &task, nil
}

// retryableReasons enumerates failure reasons that the auto-retry path is
// allowed to act on. Deterministic agent-side errors (compile failures,
// auth, quota, malformed prompts, etc.) are intentionally excluded. Transient
// provider failures are retryable because they have the same user-facing shape
// as runtime/network flakiness and are bounded by the task's max_attempts
// budget. model_not_found_or_unavailable gets one bounded retry because
// CodeBuddy can report it for transient model unavailability even when the
// configured model is valid.
var retryableReasons = map[string]bool{
	taskfailure.ReasonRuntimeOffline.String():                   true,
	taskfailure.ReasonRuntimeRecovery.String():                  true,
	taskfailure.ReasonTimeout.String():                          true,
	"codex_semantic_inactivity":                                 true,
	taskfailure.ReasonAgentProviderCapacityOrRateLimit.String(): true,
	taskfailure.ReasonAgentProviderServerError.String():         true,
	taskfailure.ReasonAgentProviderNetwork.String():             true,
	taskfailure.ReasonAgentModelNotFoundOrUnavailable.String():  true,
}

const providerNetworkExtraRetryBudget int32 = 3

func taskRetryBudget(reason string, maxAttempts int32) int32 {
	if reason == taskfailure.ReasonAgentProviderNetwork.String() {
		return maxAttempts + providerNetworkExtraRetryBudget
	}
	return maxAttempts
}

func resumeUnsafeFailureReason(reason string) bool {
	switch reason {
	// Keep in sync with GetLastTaskSession / GetLastChatTaskSession and
	// CreateRetryTask's fresh-session CASE WHEN.
	case "iteration_limit", "agent_fallback_message", "api_invalid_request", "codex_semantic_inactivity":
		return true
	default:
		return false
	}
}

// MaybeRetryFailedTask ensures a fresh queued attempt exists for a recently
// failed task when the failure is infrastructure-shaped and the attempt budget
// remains. It returns an existing child when another current path already
// materialized the retry, so callers observe one authoritative decision.
//
// Autopilot tasks are NOT auto-retried here; the autopilot scheduler owns
// its own re-run cadence and we don't want to double-fire it.
func (s *TaskService) MaybeRetryFailedTask(ctx context.Context, parent db.AgentTaskQueue) (*db.AgentTaskQueue, error) {
	var child *db.AgentTaskQueue
	var created bool
	if err := s.runInTx(ctx, func(queries *db.Queries) error {
		var err error
		child, created, err = s.materializeRetryTask(ctx, queries, parent)
		return err
	}); err != nil {
		return nil, err
	}
	if created {
		s.publishRetryTask(ctx, *child)
	}
	return child, nil
}

func (s *TaskService) materializeRetryTask(
	ctx context.Context,
	queries *db.Queries,
	parent db.AgentTaskQueue,
) (*db.AgentTaskQueue, bool, error) {
	if parent.Status != "failed" {
		return nil, false, nil
	}
	if _, isSourceSummary := ParseIssueSourceSummaryContext(parent); isSourceSummary {
		// Source summaries own a deterministic fallback write and have never
		// entered the general task auto-retry flow.
		return nil, false, nil
	}
	locked, err := queries.LockAgentTaskForRetry(ctx, parent.ID)
	if err != nil {
		return nil, false, fmt.Errorf("lock retry parent: %w", err)
	}
	if locked.Status != "failed" {
		return nil, false, nil
	}
	parent = locked
	reason := ""
	if parent.FailureReason.Valid {
		reason = parent.FailureReason.String
	}
	if !retryableReasons[reason] {
		return nil, false, nil
	}
	retryBudget := taskRetryBudget(reason, parent.MaxAttempts)
	if parent.Attempt >= retryBudget {
		slog.Info("task auto-retry skipped: budget exhausted",
			"task_id", util.UUIDToString(parent.ID),
			"attempt", parent.Attempt,
			"max_attempts", parent.MaxAttempts,
			"retry_budget", retryBudget,
			"reason", reason,
		)
		return nil, false, nil
	}
	if parent.AutopilotRunID.Valid {
		// Autopilot has its own retry semantics; do not double-trigger.
		return nil, false, nil
	}
	if !parent.IssueID.Valid && !parent.ChatSessionID.Valid {
		return nil, false, nil
	}

	existing, err := queries.GetRetryTaskForParent(ctx, parent.ID)
	if err == nil {
		return &existing, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("load existing retry task: %w", err)
	}

	child, err := queries.CreateRetryTask(ctx, parent.ID)
	if err != nil {
		return nil, false, fmt.Errorf("create retry task: %w", err)
	}
	return &child, true, nil
}

func (s *TaskService) publishRetryTask(ctx context.Context, child db.AgentTaskQueue) {
	slog.Info("task auto-retry enqueued",
		"parent_task_id", util.UUIDToString(child.ParentTaskID),
		"child_task_id", util.UUIDToString(child.ID),
		"attempt", child.Attempt,
		"max_attempts", child.MaxAttempts,
	)
	// Retry creates a fresh queued row, same status transition (∅ → queued)
	// as EnqueueTaskFor*. Broadcast queued first, then notify the daemon —
	// see EnqueueTaskForIssue for ordering rationale.
	s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, child)
	s.NotifyTaskEnqueued(ctx, child)
}

// RerunIssue creates a fresh queued task for an agent on the issue. Used by
// the manual rerun endpoint.
//
// Target agent resolution:
//   - sourceTaskID Valid: rerun the agent that ran that task (and reuse its
//     leader/worker role). This is what the execution log retry button uses
//     so a per-row retry survives a subsequent assignee change and correctly
//     re-fires the squad worker or mention agent whose row was clicked. The
//     source task's trigger_comment_id is also inherited (when the caller
//     didn't pass one) so a per-row rerun of a comment- or mention-triggered
//     task stays comment-triggered — the daemon's buildCommentPrompt path
//     keys on TriggerCommentID, and losing it would degrade the rerun into
//     a generic issue run that no longer carries the original comment.
//   - sourceTaskID empty: fall back to the issue's current assignee (agent
//     or squad leader). This preserves the CLI / API contract for callers
//     that have an issue ID but no specific task to target.
//
// The new task is flagged force_fresh_session=true so the daemon starts a
// clean agent session instead of resuming the prior (agent_id, issue_id)
// session. A user clicking rerun has just judged the prior output bad —
// resuming the same conversation would replay the same poisoned state.
// Auto-retry of an orphaned mid-flight failure (HandleFailedTasks →
// MaybeRetryFailedTask → CreateRetryTask) does NOT take this path, so
// MUL-1128's mid-flight resume contract is preserved.
//
// Only tasks belonging to the target agent on this issue are cancelled.
// Tasks owned by other agents on the same issue (e.g. a parallel
// @-mention agent) are left alone — rerun must not collateral-cancel
// them.
func (s *TaskService) RerunIssue(ctx context.Context, issueID pgtype.UUID, sourceTaskID pgtype.UUID, triggerCommentID pgtype.UUID) (*db.AgentTaskQueue, error) {
	issue, err := s.Queries.GetIssue(ctx, issueID)
	if err != nil {
		return nil, fmt.Errorf("load issue: %w", err)
	}

	// Determine the target agent for the rerun.
	var (
		agentID  pgtype.UUID
		isLeader bool
	)
	if sourceTaskID.Valid {
		sourceTask, err := s.Queries.GetAgentTask(ctx, sourceTaskID)
		if err != nil {
			return nil, fmt.Errorf("load source task: %w", err)
		}
		if !sourceTask.IssueID.Valid || util.UUIDToString(sourceTask.IssueID) != util.UUIDToString(issueID) {
			return nil, fmt.Errorf("source task does not belong to this issue")
		}
		agentID = sourceTask.AgentID
		isLeader = sourceTask.IsLeaderTask
		// Inherit trigger provenance so a per-row rerun of a comment- or
		// mention-triggered task stays a comment-triggered task. Without
		// this the daemon's buildCommentPrompt path is skipped (it keys on
		// TriggerCommentID) and the rerun degrades into a generic issue
		// run that has lost the original comment context. Only override
		// when the caller didn't pass one explicitly.
		if !triggerCommentID.Valid && sourceTask.TriggerCommentID.Valid {
			triggerCommentID = sourceTask.TriggerCommentID
		}
	} else {
		switch {
		case issue.AssigneeType.String == "agent" && issue.AssigneeID.Valid:
			agentID = issue.AssigneeID
		case issue.AssigneeType.String == "squad" && issue.AssigneeID.Valid:
			squad, err := s.Queries.GetSquad(ctx, issue.AssigneeID)
			if err != nil {
				return nil, fmt.Errorf("issue is assigned to a squad but squad not found")
			}
			agentID = squad.LeaderID
			isLeader = true
		default:
			return nil, fmt.Errorf("issue is not assigned to an agent or squad")
		}
	}

	// Cancel only the target agent's active/queued tasks on this issue.
	cancelled, err := s.CancelTasksForIssueAndAgent(ctx, issueID, agentID)
	if err != nil {
		return nil, fmt.Errorf("cancel prior tasks before rerun: %w", err)
	}

	task, err := s.enqueueRerunTask(ctx, issue, agentID, triggerCommentID, isLeader)
	if err != nil {
		return nil, err
	}
	slog.Info("issue rerun enqueued",
		"task_id", util.UUIDToString(task.ID),
		"issue_id", util.UUIDToString(issueID),
		"agent_id", util.UUIDToString(agentID),
		"source_task_id", util.UUIDToString(sourceTaskID),
		"is_leader", isLeader,
		"cancelled_prior", len(cancelled),
	)
	return &task, nil
}

// enqueueRerunTask enqueues a fresh task for the given agent on the issue.
// When the target agent is the issue's single-agent assignee we use the
// assignee-driven path (enqueueIssueTask) so the issue-assignee bookkeeping
// stays in sync; otherwise (squad member, prior assignee that has since been
// reassigned, mention agent) we use the mention path with the same
// force_fresh_session=true contract.
func (s *TaskService) enqueueRerunTask(ctx context.Context, issue db.Issue, agentID pgtype.UUID, triggerCommentID pgtype.UUID, isLeader bool) (db.AgentTaskQueue, error) {
	if issue.AssigneeType.String == "agent" && issue.AssigneeID.Valid &&
		util.UUIDToString(issue.AssigneeID) == util.UUIDToString(agentID) {
		return s.enqueueIssueTask(ctx, issue, triggerCommentID, true)
	}
	return s.enqueueMentionTask(ctx, issue, agentID, triggerCommentID, isLeader, true)
}

// HandleFailedTasks publishes post-commit traces/retries and reconciles agents
// for a batch whose terminal state, retry decision and Issue projection were
// already committed by failTasksDurably.
func (s *TaskService) HandleFailedTasks(ctx context.Context, tasks []db.AgentTaskQueue) int {
	if len(tasks) == 0 {
		return 0
	}

	affectedAgents := make(map[string]pgtype.UUID)
	retried := 0

	for _, t := range tasks {
		// Auto-retry first so the issue stays in_progress rather than
		// flapping todo → in_progress within a tick.
		if child, _ := s.MaybeRetryFailedTask(ctx, t); child != nil {
			retried++
		}

		s.captureTaskFailed(ctx, t)
		affectedAgents[util.UUIDToString(t.AgentID)] = t.AgentID
	}

	for _, agentID := range affectedAgents {
		if _, err := s.ReconcileAgentStatus(ctx, agentID); err != nil {
			slog.Warn("reconcile bulk-failed agent status failed", "agent_id", util.UUIDToString(agentID), "error", err)
		}
	}
	return retried
}

// runInTx executes fn inside a single DB transaction. If TxStarter is nil
// (e.g. some tests construct TaskService directly), fn runs against the
// regular Queries handle without transactional guarantees.
func (s *TaskService) runInTx(ctx context.Context, fn func(*db.Queries) error) error {
	if s.TxStarter == nil {
		return fn(s.Queries)
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(s.Queries.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ReportProgress broadcasts a progress update via the event bus.
func (s *TaskService) ReportProgress(ctx context.Context, taskID string, workspaceID string, summary string, step, total int) {
	s.Bus.Publish(events.Event{
		Type:        protocol.EventTaskProgress,
		WorkspaceID: workspaceID,
		ActorType:   "system",
		ActorID:     "",
		TaskID:      taskID,
		Payload: protocol.TaskProgressPayload{
			TaskID:  taskID,
			Summary: summary,
			Step:    step,
			Total:   total,
		},
	})
}

// ReconcileAgentStatus refreshes agent status from the current active task set
// and returns the persisted row. Callers that need the refreshed representation
// must handle the error instead of issuing a second, best-effort read.
func (s *TaskService) ReconcileAgentStatus(ctx context.Context, agentID pgtype.UUID) (db.Agent, error) {
	agent, err := s.Queries.RefreshAgentStatusFromTasks(ctx, agentID)
	if err != nil {
		slog.Warn("reconcile agent status failed", "agent_id", util.UUIDToString(agentID), "error", err)
		return db.Agent{}, fmt.Errorf("refresh agent status: %w", err)
	}
	slog.Debug("agent status reconciled", "agent_id", util.UUIDToString(agentID), "status", agent.Status)
	s.publishAgentStatus(agent)
	return agent, nil
}

func (s *TaskService) publishAgentStatus(agent db.Agent) {
	s.Bus.Publish(events.Event{
		Type:        protocol.EventAgentStatus,
		WorkspaceID: util.UUIDToString(agent.WorkspaceID),
		ActorType:   "system",
		ActorID:     "",
		Payload:     map[string]any{"agent": agentToMap(agent)},
	})
}

// LoadAgentSkills loads an agent's skills with their files for task execution.
func (s *TaskService) LoadAgentSkills(ctx context.Context, agentID pgtype.UUID) []AgentSkillData {
	skills, err := s.Queries.ListAgentSkills(ctx, agentID)
	if err != nil || len(skills) == 0 {
		return nil
	}

	result := make([]AgentSkillData, 0, len(skills))
	for _, sk := range skills {
		data := AgentSkillData{
			ID:          util.UUIDToString(sk.ID),
			Name:        sk.Name,
			Description: sk.Description,
			Content:     sk.Content,
		}
		files, _ := s.Queries.ListSkillFiles(ctx, sk.ID)
		for _, f := range files {
			data.Files = append(data.Files, AgentSkillFileData{Path: f.Path, Content: f.Content})
		}
		result = append(result, data)
	}
	return result
}

// AgentSkillData represents a skill for task execution responses.
type AgentSkillData struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Description string               `json:"description,omitempty"`
	Content     string               `json:"content"`
	Files       []AgentSkillFileData `json:"files,omitempty"`
}

// AgentSkillFileData represents a supporting file within a skill.
type AgentSkillFileData struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// ComputeChatElapsedMs returns the wall-clock duration from task creation
// (user hit send) to terminal state (completed/failed). Stored on the
// assistant chat_message so the UI can render "Replied in 38s" /
// "Failed after 12s". Uses created_at — not started_at — because users
// experience total wait time, including queue + dispatch, not just the
// daemon's actual run time.
func ComputeChatElapsedMs(task db.AgentTaskQueue) pgtype.Int8 {
	if !task.CompletedAt.Valid || !task.CreatedAt.Valid {
		return pgtype.Int8{}
	}
	ms := task.CompletedAt.Time.Sub(task.CreatedAt.Time).Milliseconds()
	if ms < 0 {
		ms = 0
	}
	return pgtype.Int8{Int64: ms, Valid: true}
}

func priorityToInt(p string) int32 {
	switch p {
	case "urgent":
		return 4
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

// NotifyTaskEnqueued is the cross-package shim for callers outside
// TaskService (e.g. AutopilotService.dispatchRunOnly) that insert a
// row into agent_task_queue directly. Invalidates the empty-claim
// cache and kicks the daemon WS so the new task is claimed without
// waiting for the next poll.
func (s *TaskService) NotifyTaskEnqueued(ctx context.Context, task db.AgentTaskQueue) {
	s.captureTaskUserInput(ctx, task)
	s.captureTaskQueued(ctx, task)
	s.notifyTaskAvailable(task)
}

// notifyTaskAvailable runs after a task has been inserted: bumps the
// runtime's invalidation version so any in-flight claim that is about
// to write an "empty" verdict will have it rejected on read, then
// kicks the daemon WS so the daemon claims without waiting for its
// next poll. Order matters — Bump must happen before the wakeup,
// otherwise the wakeup-driven claim could read the still-current
// empty verdict and return null.
func (s *TaskService) notifyTaskAvailable(task db.AgentTaskQueue) {
	if !task.RuntimeID.Valid {
		return
	}
	runtimeKey := util.UUIDToString(task.RuntimeID)
	// Use a background context: the cache bump / wakeup must outlive
	// the request that created the task, otherwise an early client
	// disconnect could leave the empty verdict in place and stall the
	// just-queued task until the TTL expires. The cache itself bounds
	// every Redis call with a short timeout so a wedged Redis cannot
	// block enqueue.
	s.EmptyClaim.Bump(context.Background(), runtimeKey)
	if s.Wakeup == nil {
		return
	}
	s.Wakeup.NotifyTaskAvailable(runtimeKey, util.UUIDToString(task.ID))
}

func (s *TaskService) broadcastTaskDispatch(ctx context.Context, task db.AgentTaskQueue) {
	var payload map[string]any
	if task.Context != nil {
		if err := json.Unmarshal(task.Context, &payload); err != nil {
			slog.Warn("decode task dispatch context failed", "task_id", util.UUIDToString(task.ID), "error", err)
		}
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload["task_id"] = util.UUIDToString(task.ID)
	payload["runtime_id"] = util.UUIDToString(task.RuntimeID)
	payload["issue_id"] = util.UUIDToString(task.IssueID)
	payload["agent_id"] = util.UUIDToString(task.AgentID)
	// chat_session_id is the routing key the chat window uses to writethrough
	// `chatKeys.pendingTask` to status="running" the moment the daemon claims
	// the task. Without it the pill stays stuck at "Queued" until completion.
	if task.ChatSessionID.Valid {
		payload["chat_session_id"] = util.UUIDToString(task.ChatSessionID)
	}

	workspaceID := s.ResolveTaskWorkspaceID(ctx, task)
	if workspaceID == "" {
		return
	}
	s.Bus.Publish(events.Event{
		Type:        protocol.EventTaskDispatch,
		WorkspaceID: workspaceID,
		ActorType:   "system",
		ActorID:     "",
		Payload:     payload,
	})
}

func (s *TaskService) broadcastTaskEvent(ctx context.Context, eventType string, task db.AgentTaskQueue) {
	event, err := s.buildTaskEvent(ctx, s.Queries, eventType, task)
	if err != nil {
		slog.Warn("build task event failed", "task_id", util.UUIDToString(task.ID), "event_type", eventType, "error", err)
		return
	}
	s.Bus.Publish(event)
}

// ResolveTaskWorkspaceID determines the workspace ID from the task's owned
// resource, with the task agent as the current-model fallback. It returns ""
// only when neither the resource nor the required agent row can be resolved.
func (s *TaskService) ResolveTaskWorkspaceID(ctx context.Context, task db.AgentTaskQueue) string {
	workspaceID, err := s.resolveTaskWorkspaceID(ctx, s.Queries, task)
	if err != nil {
		slog.Warn("resolve task workspace failed", "task_id", util.UUIDToString(task.ID), "error", err)
		return ""
	}
	return workspaceID
}

type taskIssueUpdateChanges struct {
	Status      bool
	Description bool
}

func (s *TaskService) persistIssueUpdateInTx(
	ctx context.Context,
	queries *db.Queries,
	previous db.Issue,
	changes taskIssueUpdateChanges,
	update func(*db.Queries) (db.Issue, error),
) (db.Issue, events.Event, error) {
	updated, err := update(queries)
	if err != nil {
		return db.Issue{}, events.Event{}, err
	}
	prefix, err := s.getIssuePrefixWithQueries(ctx, queries, updated.WorkspaceID)
	if err != nil {
		return db.Issue{}, events.Event{}, fmt.Errorf("load issue prefix: %w", err)
	}
	event := events.Event{
		Type:        protocol.EventIssueUpdated,
		StreamKey:   "issue:" + util.UUIDToString(updated.ID),
		WorkspaceID: util.UUIDToString(updated.WorkspaceID),
		ActorType:   "system",
		Payload: map[string]any{
			"issue":               issueToMap(updated, prefix),
			"status_changed":      changes.Status,
			"description_changed": changes.Description,
			"prev_status":         previous.Status,
			"prev_description":    util.TextToPtr(previous.Description),
			"creator_type":        previous.CreatorType,
			"creator_id":          util.UUIDToString(previous.CreatorID),
		},
	}
	event, err = eventoutbox.Enqueue(ctx, queries, event)
	if err != nil {
		return db.Issue{}, events.Event{}, err
	}
	return updated, event, nil
}

func (s *TaskService) getIssuePrefixWithQueries(ctx context.Context, queries *db.Queries, workspaceID pgtype.UUID) (string, error) {
	ws, err := queries.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return "", err
	}
	return ws.IssuePrefix, nil
}
