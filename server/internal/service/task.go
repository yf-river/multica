package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type TaskService struct {
	Queries   *db.Queries
	TxStarter TxStarter
	Hub       *realtime.Hub
	Bus       *events.Bus
	Analytics analytics.Client
	Metrics   *obsmetrics.BusinessMetrics
	Wakeup    TaskWakeupNotifier
	// IssueStatusChanged runs best-effort side effects that live above the
	// task service layer, such as parent child-done notifications.
	IssueStatusChanged func(ctx context.Context, prev, updated db.Issue, actorType, actorID string)
	// AgentCommentCreated runs comment-trigger side effects for comments
	// synthesized by the task service. HTTP-created comments execute the same
	// logic inside the handler layer.
	AgentCommentCreated func(ctx context.Context, issue db.Issue, comment db.Comment, parentComment *db.Comment)
	// EmptyClaim caches "this runtime has no queued task" so the daemon
	// poll path can skip a Postgres scan on the steady-state empty case.
	// Optional — a nil cache disables the fast path and every claim
	// goes through the DB. Wired in router.go from the shared Redis
	// client.
	EmptyClaim *EmptyClaimCache

	analyticsContextMu    sync.Mutex
	analyticsContextCache map[string]analytics.TaskContext
	analyticsContextOrder []string
}

var ErrTaskStartConflict = errors.New("task is no longer startable")

type TaskStartConflictError struct {
	Status string
}

func (e TaskStartConflictError) Error() string {
	return fmt.Sprintf("%s: current status %s", ErrTaskStartConflict, e.Status)
}

func (e TaskStartConflictError) Is(target error) bool {
	return target == ErrTaskStartConflict
}

type TaskWakeupNotifier interface {
	NotifyTaskAvailable(runtimeID, taskID string)
}

// triggerSummaryMaxLen caps the snapshot length so the row stays cheap to
// transmit (it ends up in every task list response). 200 is enough for a
// recognisable preview of a one-paragraph comment.
const triggerSummaryMaxLen = 200

const userInputSnapshotMaxLen = 20000

// truncateForSummary returns s shortened to maxRunes, with a trailing
// `…` when truncated. Operates on runes (not bytes) so multibyte characters
// — Chinese / emoji — count as one each. Strips surrounding whitespace
// first so a leading newline doesn't waste budget.
func truncateForSummary(s string, maxRunes int) string {
	// strings.Builder + Grow avoids the O(N²) realloc cycle of `+=` in
	// a loop. Grow uses byte length, which is an upper bound for the
	// rune-equivalent output (replacing \n/\r/\t with space is byte-equal
	// for ASCII whitespace), so we never reallocate.
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\n', '\r', '\t':
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	rs := []rune(strings.TrimSpace(b.String()))
	if len(rs) <= maxRunes {
		return string(rs)
	}
	return string(rs[:maxRunes]) + "…"
}

const (
	taskAnalyticsContextCacheMax = 4096
	// claimResponseRecoveryWindow must exceed daemon client.Timeout for
	// /tasks/claim (30s) plus /tasks/{id}/start (30s) plus scheduling slack, so
	// an in-flight StartTask cannot be reclaimed and double-dispatched.
	claimResponseRecoveryWindow = 90 * time.Second
)

// buildCommentTriggerSummary fetches the comment content and truncates
// it for storage on the task row. Returns an invalid pgtype.Text when
// the comment is missing (deleted / wrong workspace / etc) so the column
// stays NULL — front-end falls back to a structural label in that case.
func (s *TaskService) buildCommentTriggerSummaryWithQueries(ctx context.Context, queries *db.Queries, commentID pgtype.UUID) pgtype.Text {
	if !commentID.Valid {
		return pgtype.Text{}
	}
	comment, err := queries.GetComment(ctx, commentID)
	if err != nil {
		return pgtype.Text{}
	}
	summary := truncateForSummary(comment.Content, triggerSummaryMaxLen)
	if summary == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: summary, Valid: true}
}

func NewTaskService(q *db.Queries, tx TxStarter, hub *realtime.Hub, bus *events.Bus, wakeups ...TaskWakeupNotifier) *TaskService {
	var wakeup TaskWakeupNotifier
	if len(wakeups) > 0 {
		wakeup = wakeups[0]
	}
	return &TaskService{Queries: q, TxStarter: tx, Hub: hub, Bus: bus, Wakeup: wakeup}
}

var trivialDoneMarkers = []string{
	"done",
	"готово",
	"готова",
	"сделано",
	"完成",
	"完了",
}

func isTrivialDoneOutput(output string) bool {
	normalized := strings.TrimSpace(strings.ToLower(output))
	normalized = strings.Trim(normalized, ".!！。… ")
	for _, marker := range trivialDoneMarkers {
		if normalized == marker {
			return true
		}
	}
	return false
}

var agentMentionURLPattern = regexp.MustCompile(`mention://agent/[0-9a-fA-F-]{36}`)

func containsAgentMention(content string) bool {
	return agentMentionURLPattern.MatchString(content)
}

func isDispatchLikeTaskMessage(content string) bool {
	normalized := strings.ToLower(strings.TrimSpace(content))
	if normalized == "" {
		return false
	}
	for _, marker := range []string{
		"调度",
		"dispatch",
		"delegate",
		"请继续",
		"请补齐",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func dispatchCommentFromTaskMessages(messages []db.TaskMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if message.Type != "text" || !message.Content.Valid {
			continue
		}
		content := strings.TrimSpace(util.UnescapeBackslashEscapes(message.Content.String))
		if content == "" || !isDispatchLikeTaskMessage(content) {
			continue
		}
		matches := agentMentionURLPattern.FindAllString(content, -1)
		if len(matches) == 1 {
			return content
		}
	}
	return ""
}

func fallbackDispatchCommentFromMessages(ctx context.Context, queries *db.Queries, taskID pgtype.UUID) (string, error) {
	messages, err := queries.ListTaskMessages(ctx, taskID)
	if err != nil {
		return "", fmt.Errorf("list task messages for dispatch fallback: %w", err)
	}
	return dispatchCommentFromTaskMessages(messages), nil
}

func (s *TaskService) CancelTasksForIssue(ctx context.Context, issueID pgtype.UUID) error {
	var cancelled []db.AgentTaskQueue
	var persistedEvents []events.Event
	err := s.runInTx(ctx, func(queries *db.Queries) error {
		var err error
		cancelled, persistedEvents, err = s.CancelTasksForIssueInTx(ctx, queries, issueID)
		return err
	})
	if err != nil {
		return err
	}
	s.PublishCancelledTasks(ctx, cancelled, persistedEvents)
	return nil
}

// CancelTasksForIssueInTx lets a parent mutation commit task cancellation and
// its durable terminal events with the parent row change. The caller owns the
// transaction and must call PublishCancelledTasks only after commit.
func (s *TaskService) CancelTasksForIssueInTx(
	ctx context.Context,
	queries *db.Queries,
	issueID pgtype.UUID,
) ([]db.AgentTaskQueue, []events.Event, error) {
	cancelled, err := queries.CancelAgentTasksByIssue(ctx, issueID)
	if err != nil {
		return nil, nil, err
	}
	persistedEvents, err := s.EnqueueCancelledTaskEvents(ctx, queries, cancelled)
	if err != nil {
		return nil, nil, err
	}
	return cancelled, persistedEvents, nil
}

// CancelTasksForAgent cancels every active task belonging to an agent
// (queued + dispatched + running), reconciles the agent's status, and
// broadcasts task:cancelled events. Used by the agent-level "Cancel all
// tasks" action — same shape as CancelTasksForIssue but scoped on agent_id.
//
// Returns the cancelled rows so callers can report counts / log them.
func (s *TaskService) CancelTasksForAgent(ctx context.Context, agentID pgtype.UUID) ([]db.AgentTaskQueue, error) {
	cancelled, persistedEvents, err := s.cancelTasksDurably(ctx, func(queries *db.Queries) ([]db.AgentTaskQueue, error) {
		return queries.CancelAgentTasksByAgent(ctx, agentID)
	})
	if err != nil {
		return nil, err
	}
	s.PublishCancelledTasks(ctx, cancelled, persistedEvents)
	return cancelled, nil
}

// CancelTasksByTriggerComment cancels active tasks whose trigger is the given
// comment. Called from DeleteComment so an agent does not run with the
// now-deleted content already embedded in its prompt. Must be invoked BEFORE
// the comment row is deleted because the FK ON DELETE SET NULL would
// otherwise nullify trigger_comment_id and we'd lose the ability to find
// the affected tasks.
func (s *TaskService) CancelTasksByTriggerComment(ctx context.Context, commentID pgtype.UUID) error {
	var cancelled []db.AgentTaskQueue
	var persistedEvents []events.Event
	err := s.runInTx(ctx, func(queries *db.Queries) error {
		var err error
		cancelled, persistedEvents, err = s.CancelTasksByTriggerCommentInTx(ctx, queries, commentID)
		return err
	})
	if err != nil {
		return err
	}
	s.PublishCancelledTasks(ctx, cancelled, persistedEvents)
	return nil
}

// CancelTasksByTriggerCommentInTx cancels tasks and persists their terminal
// events in the caller's transaction. The caller publishes only after commit.
func (s *TaskService) CancelTasksByTriggerCommentInTx(
	ctx context.Context,
	queries *db.Queries,
	commentID pgtype.UUID,
) ([]db.AgentTaskQueue, []events.Event, error) {
	cancelled, err := queries.CancelAgentTasksByTriggerComment(ctx, commentID)
	if err != nil {
		return nil, nil, err
	}
	persistedEvents, err := s.EnqueueCancelledTaskEvents(ctx, queries, cancelled)
	if err != nil {
		return nil, nil, err
	}
	return cancelled, persistedEvents, nil
}

func (s *TaskService) CancelTasksForIssueAndAgent(ctx context.Context, issueID, agentID pgtype.UUID) ([]db.AgentTaskQueue, error) {
	cancelled, persistedEvents, err := s.cancelTasksDurably(ctx, func(queries *db.Queries) ([]db.AgentTaskQueue, error) {
		return queries.CancelAgentTasksByIssueAndAgent(ctx, db.CancelAgentTasksByIssueAndAgentParams{
			IssueID: issueID,
			AgentID: agentID,
		})
	})
	if err != nil {
		return nil, err
	}
	s.PublishCancelledTasks(ctx, cancelled, persistedEvents)
	return cancelled, nil
}

type CancelledChatMessageResult struct {
	ChatSessionID  string
	MessageID      string
	Content        string
	RestoreToInput bool
	// Attachments are the rows detached from the deleted user message so they
	// survive the ON DELETE CASCADE and can re-bind when the restored draft is
	// re-sent.
	Attachments []db.Attachment
}

type CancelTaskResult struct {
	Task                 db.AgentTaskQueue
	CancelledChatMessage *CancelledChatMessageResult
}

// CancelTask cancels a single task by ID. It broadcasts a task:cancelled event
// so frontends can update immediately.
func (s *TaskService) CancelTask(ctx context.Context, taskID pgtype.UUID) (*db.AgentTaskQueue, error) {
	result, err := s.CancelTaskWithResult(ctx, taskID)
	if err != nil {
		return nil, err
	}
	return &result.Task, nil
}

// CancelTaskWithResult cancels a single task and returns any chat-specific
// cleanup result needed by user-facing callers.
func (s *TaskService) CancelTaskWithResult(ctx context.Context, taskID pgtype.UUID) (*CancelTaskResult, error) {
	var task db.AgentTaskQueue
	var cancelledChatMessage *CancelledChatMessageResult
	var persistedEvent events.Event
	err := s.runInTx(ctx, func(queries *db.Queries) error {
		var err error
		task, err = queries.CancelAgentTask(ctx, taskID)
		if err != nil {
			return err
		}
		cancelledChatMessage, err = finalizeCancelledChatMessageInTx(ctx, queries, task)
		if err != nil {
			return err
		}
		persistedEvent, err = s.enqueueTaskEvent(ctx, queries, protocol.EventTaskCancelled, task)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		existing, lookupErr := s.Queries.GetAgentTask(ctx, taskID)
		if lookupErr != nil {
			return nil, fmt.Errorf("cancel task: %w", lookupErr)
		}
		return &CancelTaskResult{Task: existing}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cancel task: %w", err)
	}

	slog.Info("task cancelled", "task_id", util.UUIDToString(task.ID), "issue_id", util.UUIDToString(task.IssueID))
	s.captureTaskCancelled(ctx, task)
	if _, err := s.ReconcileAgentStatus(ctx, task.AgentID); err != nil {
		slog.Warn("reconcile agent after task cancellation failed", "agent_id", util.UUIDToString(task.AgentID), "error", err)
	}
	s.Bus.Publish(persistedEvent)

	return &CancelTaskResult{
		Task:                 task,
		CancelledChatMessage: cancelledChatMessage,
	}, nil
}

func finalizeCancelledChatMessageInTx(ctx context.Context, queries *db.Queries, task db.AgentTaskQueue) (*CancelledChatMessageResult, error) {
	if !task.ChatSessionID.Valid {
		return nil, nil
	}
	var cancelled *CancelledChatMessageResult
	messages, err := queries.ListTaskMessages(ctx, task.ID)
	if err != nil {
		return nil, fmt.Errorf("list cancelled chat task messages: %w", err)
	}
	if len(messages) == 0 {
		// Detach attachments BEFORE deleting the user message — the
		// attachment FK is ON DELETE CASCADE, so deleting first would
		// destroy rows the restored draft needs to re-bind.
		detached, err := queries.DetachAttachmentsFromUserChatMessageByTask(ctx, task.ID)
		if err != nil {
			return nil, fmt.Errorf("detach cancelled chat message attachments: %w", err)
		}
		deleted, err := queries.DeleteUserChatMessageByTask(ctx, task.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("delete empty cancelled chat user message: %w", err)
		}
		cancelled = &CancelledChatMessageResult{
			ChatSessionID:  util.UUIDToString(deleted.ChatSessionID),
			MessageID:      util.UUIDToString(deleted.ID),
			Content:        deleted.Content,
			RestoreToInput: true,
			Attachments:    detached,
		}
		return cancelled, nil
	}
	if _, err := queries.CreateChatMessage(ctx, db.CreateChatMessageParams{
		ChatSessionID: task.ChatSessionID,
		Role:          "assistant",
		Content:       "Stopped.",
		TaskID:        task.ID,
		ElapsedMs:     ComputeChatElapsedMs(task),
	}); err != nil {
		return nil, fmt.Errorf("create cancelled chat message: %w", err)
	}
	return nil, nil
}

type agentCommentProjection struct {
	issue           db.Issue
	comment         db.Comment
	parentComment   *db.Comment
	createdEvent    events.Event
	unresolvedEvent events.Event
	threadReopened  bool
}

func createAgentCommentInTx(
	ctx context.Context,
	queries *db.Queries,
	issueID, agentID pgtype.UUID,
	content, commentType string,
	parentID, sourceTaskID pgtype.UUID,
) (*agentCommentProjection, error) {
	if content == "" {
		return nil, nil
	}
	issue, err := queries.GetIssue(ctx, issueID)
	if err != nil {
		return nil, fmt.Errorf("load issue for agent comment: %w", err)
	}
	var parentComment *db.Comment
	var rootComment *db.Comment
	if parentID.Valid {
		parent, err := queries.GetComment(ctx, parentID)
		if err != nil {
			return nil, fmt.Errorf("load parent agent comment: %w", err)
		}
		if parent.IssueID != issueID {
			return nil, errors.New("parent agent comment belongs to another issue")
		}
		parentComment = &parent
		root, err := queries.GetThreadRoot(ctx, db.GetThreadRootParams{
			CommentID:   parentID,
			WorkspaceID: issue.WorkspaceID,
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("load agent comment thread root: %w", err)
		}
		if err == nil {
			rootComment = &root
		}
	}
	comment, err := queries.CreateComment(ctx, db.CreateCommentParams{
		IssueID:      issueID,
		WorkspaceID:  issue.WorkspaceID,
		AuthorType:   "agent",
		AuthorID:     agentID,
		Content:      content,
		Type:         commentType,
		ParentID:     parentID,
		SourceTaskID: sourceTaskID,
	})
	if err != nil {
		return nil, err
	}
	createdEvent := taskCommentCreatedEvent(issue, comment, "agent", util.UUIDToString(agentID))
	unresolvedEvent, threadReopened, err := UnresolveThreadOnReply(
		ctx,
		queries,
		rootComment,
		util.UUIDToString(issue.WorkspaceID),
		"agent",
		util.UUIDToString(agentID),
	)
	if err != nil {
		return nil, err
	}
	createdEvent, err = eventoutbox.Enqueue(ctx, queries, createdEvent)
	if err != nil {
		return nil, err
	}
	return &agentCommentProjection{
		issue:           issue,
		comment:         comment,
		parentComment:   parentComment,
		createdEvent:    createdEvent,
		unresolvedEvent: unresolvedEvent,
		threadReopened:  threadReopened,
	}, nil
}

func (s *TaskService) publishAgentCommentProjection(ctx context.Context, projection *agentCommentProjection) {
	if projection == nil {
		return
	}
	if s.Bus != nil {
		s.Bus.Publish(projection.createdEvent)
		if projection.threadReopened {
			s.Bus.Publish(projection.unresolvedEvent)
		}
	}
	if projection.comment.Type == "comment" && s.AgentCommentCreated != nil {
		s.AgentCommentCreated(ctx, projection.issue, projection.comment, projection.parentComment)
	}
}

func taskCommentCreatedEvent(issue db.Issue, comment db.Comment, actorType, actorID string) events.Event {
	return events.Event{
		Type:        protocol.EventCommentCreated,
		StreamKey:   "issue:" + util.UUIDToString(issue.ID),
		WorkspaceID: util.UUIDToString(issue.WorkspaceID),
		ActorType:   actorType,
		ActorID:     actorID,
		Payload: map[string]any{
			"comment": map[string]any{
				"id":             util.UUIDToString(comment.ID),
				"issue_id":       util.UUIDToString(comment.IssueID),
				"author_type":    comment.AuthorType,
				"author_id":      util.UUIDToString(comment.AuthorID),
				"content":        comment.Content,
				"type":           comment.Type,
				"parent_id":      util.UUIDToPtr(comment.ParentID),
				"source_task_id": util.UUIDToPtr(comment.SourceTaskID),
				"created_at":     comment.CreatedAt.Time.Format("2006-01-02T15:04:05Z"),
			},
			"issue_title":  issue.Title,
			"issue_status": issue.Status,
		},
	}
}

func issueToMap(issue db.Issue, issuePrefix string) map[string]any {
	return map[string]any{
		"id":                util.UUIDToString(issue.ID),
		"workspace_id":      util.UUIDToString(issue.WorkspaceID),
		"number":            issue.Number,
		"identifier":        issuePrefix + "-" + strconv.Itoa(int(issue.Number)),
		"title":             issue.Title,
		"description":       util.TextToPtr(issue.Description),
		"status":            issue.Status,
		"priority":          issue.Priority,
		"assignee_type":     util.TextToPtr(issue.AssigneeType),
		"assignee_id":       util.UUIDToPtr(issue.AssigneeID),
		"creator_type":      issue.CreatorType,
		"creator_id":        util.UUIDToString(issue.CreatorID),
		"parent_issue_id":   util.UUIDToPtr(issue.ParentIssueID),
		"project_id":        util.UUIDToPtr(issue.ProjectID),
		"position":          issue.Position,
		"start_date":        util.DateToPtr(issue.StartDate),
		"due_date":          util.DateToPtr(issue.DueDate),
		"metadata":          issueMetadataMap(issue.Metadata),
		"created_at":        util.TimestampToString(issue.CreatedAt),
		"updated_at":        util.TimestampToString(issue.UpdatedAt),
		"work_started_at":   util.TimestampToPtr(issue.WorkStartedAt),
		"work_completed_at": util.TimestampToPtr(issue.WorkCompletedAt),
	}
}

func issueMetadataMap(raw []byte) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var metadata map[string]any
	if err := json.Unmarshal(raw, &metadata); err != nil || metadata == nil {
		return map[string]any{}
	}
	return metadata
}

// ParseQuickCreateContext returns the quick-create payload if the task's
// context JSONB contains type == "quick_create"; otherwise the bool is
// false so callers can short-circuit. Tasks linked to an issue / chat /
// autopilot are never quick-create even if they happen to carry a
// context blob, so those are filtered up front.
func ParseQuickCreateContext(task db.AgentTaskQueue) (QuickCreateContext, bool) {
	if task.IssueID.Valid || task.ChatSessionID.Valid || task.AutopilotRunID.Valid {
		return QuickCreateContext{}, false
	}
	if len(task.Context) == 0 {
		return QuickCreateContext{}, false
	}
	var qc QuickCreateContext
	if err := json.Unmarshal(task.Context, &qc); err != nil {
		return QuickCreateContext{}, false
	}
	if qc.Type != QuickCreateContextType {
		return QuickCreateContext{}, false
	}
	return qc, true
}

func ParseIssueSourceSummaryContext(task db.AgentTaskQueue) (IssueSourceSummaryContext, bool) {
	if !task.IssueID.Valid || task.ChatSessionID.Valid || task.AutopilotRunID.Valid {
		return IssueSourceSummaryContext{}, false
	}
	if len(task.Context) == 0 {
		return IssueSourceSummaryContext{}, false
	}
	var sc IssueSourceSummaryContext
	if err := json.Unmarshal(task.Context, &sc); err != nil {
		return IssueSourceSummaryContext{}, false
	}
	if sc.Type != IssueSourceSummaryContextType {
		return IssueSourceSummaryContext{}, false
	}
	return sc, true
}

// agentToMap builds a simple map for broadcasting agent status updates.
func agentToMap(a db.Agent) map[string]any {
	var rc any
	if a.RuntimeConfig != nil {
		if err := json.Unmarshal(a.RuntimeConfig, &rc); err != nil {
			slog.Warn("decode agent runtime config failed", "agent_id", util.UUIDToString(a.ID), "error", err)
		}
	}
	return map[string]any{
		"id":                   util.UUIDToString(a.ID),
		"workspace_id":         util.UUIDToString(a.WorkspaceID),
		"runtime_id":           util.UUIDToString(a.RuntimeID),
		"name":                 a.Name,
		"description":          a.Description,
		"avatar_url":           util.TextToPtr(a.AvatarUrl),
		"runtime_mode":         a.RuntimeMode,
		"runtime_config":       rc,
		"scope":                a.Scope,
		"status":               a.Status,
		"max_concurrent_tasks": a.MaxConcurrentTasks,
		"owner_id":             util.UUIDToPtr(a.OwnerID),
		"skills":               []any{},
		"created_at":           util.TimestampToString(a.CreatedAt),
		"updated_at":           util.TimestampToString(a.UpdatedAt),
		"archived_at":          util.TimestampToPtr(a.ArchivedAt),
		"archived_by":          util.UUIDToPtr(a.ArchivedBy),
	}
}
