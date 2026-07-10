package service

import (
	"context"
	"crypto/sha256"
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
	"github.com/multica-ai/multica/server/internal/events"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/redact"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
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

var errIssueDoneBlockedByChildren = errors.New("issue has child issues that are not done")

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
func (s *TaskService) buildCommentTriggerSummary(ctx context.Context, commentID pgtype.UUID) pgtype.Text {
	if !commentID.Valid {
		return pgtype.Text{}
	}
	comment, err := s.Queries.GetComment(ctx, commentID)
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

func (s *TaskService) fallbackDispatchCommentFromMessages(ctx context.Context, taskID pgtype.UUID) string {
	messages, err := s.Queries.ListTaskMessages(ctx, taskID)
	if err != nil {
		slog.Warn("list task messages for dispatch fallback failed",
			"task_id", util.UUIDToString(taskID),
			"error", err,
		)
		return ""
	}
	return dispatchCommentFromTaskMessages(messages)
}

func (s *TaskService) captureTaskQueued(ctx context.Context, task db.AgentTaskQueue) {
	s.recordTaskTraceEvent(ctx, task, "task.queued", "任务已入队", taskTraceOptions{})
	if s.Metrics != nil {
		source, runtimeMode, _ := s.taskMetricsContext(ctx, task)
		s.Metrics.RecordTaskEnqueued(source, runtimeMode)
	}
}

func (s *TaskService) captureTaskUserInput(ctx context.Context, task db.AgentTaskQueue) {
	events, err := s.Queries.ListTaskTraceEventsByTask(ctx, task.ID)
	if err != nil {
		slog.Warn("list task trace events before user input trace failed",
			"task_id", util.UUIDToString(task.ID),
			"error", err,
		)
	} else {
		for _, event := range events {
			if event.EventType == "user_input.received" {
				return
			}
		}
	}

	metadata := s.buildTaskUserInputMetadata(ctx, task)
	if len(metadata) == 0 {
		return
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		slog.Warn("marshal task user input trace metadata failed",
			"task_id", util.UUIDToString(task.ID),
			"error", err,
		)
		return
	}
	s.recordTaskTraceEvent(ctx, task, "user_input.received", "用户输入已接收", taskTraceOptions{Metadata: raw})
}

func (s *TaskService) buildTaskUserInputMetadata(ctx context.Context, task db.AgentTaskQueue) map[string]any {
	switch {
	case task.TriggerCommentID.Valid:
		return s.buildCommentUserInputMetadata(ctx, task)
	case task.ChatSessionID.Valid:
		return s.buildChatUserInputMetadata(ctx, task)
	case task.AutopilotRunID.Valid:
		return s.buildAutopilotUserInputMetadata(ctx, task, task.AutopilotRunID)
	default:
		if qc, ok := s.parseQuickCreateContext(task); ok {
			return s.buildQuickCreateUserInputMetadata(task, qc)
		}
		if task.IssueID.Valid {
			return s.buildIssueUserInputMetadata(ctx, task)
		}
		return s.buildDirectUserInputMetadata(task)
	}
}

func (s *TaskService) buildIssueUserInputMetadata(ctx context.Context, task db.AgentTaskQueue) map[string]any {
	issue, err := s.Queries.GetIssue(ctx, task.IssueID)
	if err != nil {
		slog.Warn("build issue user input trace metadata failed",
			"task_id", util.UUIDToString(task.ID),
			"issue_id", util.UUIDToString(task.IssueID),
			"error", err,
		)
		return s.buildDirectUserInputMetadata(task)
	}

	inputKind := "issue"
	extra := map[string]any{}
	if issue.OriginType.Valid && issue.OriginType.String == "autopilot" {
		inputKind = "autopilot"
		extra["origin_type"] = issue.OriginType.String
		extra["origin_id"] = util.UUIDToString(issue.OriginID)
		if run, err := s.Queries.GetAutopilotRunByIssue(ctx, issue.ID); err == nil {
			extra["autopilot_run_id"] = util.UUIDToString(run.ID)
			extra["trigger_payload_summary"], extra["trigger_payload_truncated"] = summarizeRawJSON(run.TriggerPayload, triggerSummaryMaxLen)
		}
	}

	content := textWithTitle(issue.Title, issue.Description.String)
	metadata := baseUserInputMetadata(inputKind, issue.CreatorType, issue.CreatorID, "issue", issue.ID, issue.Title, content)
	metadata["source_url"] = "/issues/" + util.UUIDToString(issue.ID)
	metadata["issue_id"] = util.UUIDToString(issue.ID)
	for key, value := range extra {
		if value != "" {
			metadata[key] = value
		}
	}
	if attachments, err := s.Queries.ListAttachmentsByIssue(ctx, db.ListAttachmentsByIssueParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
	}); err == nil {
		metadata["attachments"] = attachmentMetadataList(attachments)
	} else {
		slog.Warn("list issue attachments for user input trace failed",
			"task_id", util.UUIDToString(task.ID),
			"issue_id", util.UUIDToString(issue.ID),
			"error", err,
		)
	}
	return metadata
}

func (s *TaskService) buildCommentUserInputMetadata(ctx context.Context, task db.AgentTaskQueue) map[string]any {
	comment, err := s.Queries.GetComment(ctx, task.TriggerCommentID)
	if err != nil {
		slog.Warn("build comment user input trace metadata failed",
			"task_id", util.UUIDToString(task.ID),
			"comment_id", util.UUIDToString(task.TriggerCommentID),
			"error", err,
		)
		return s.buildIssueUserInputMetadata(ctx, task)
	}

	title := "Issue 评论"
	if issue, err := s.Queries.GetIssue(ctx, comment.IssueID); err == nil && issue.Title != "" {
		title = issue.Title
	}
	metadata := baseUserInputMetadata("comment", comment.AuthorType, comment.AuthorID, "comment", comment.ID, title, comment.Content)
	metadata["issue_id"] = util.UUIDToString(comment.IssueID)
	metadata["comment_id"] = util.UUIDToString(comment.ID)
	metadata["source_url"] = "/issues/" + util.UUIDToString(comment.IssueID) + "#comment-" + util.UUIDToString(comment.ID)
	if attachments, err := s.Queries.ListAttachmentsByComment(ctx, db.ListAttachmentsByCommentParams{
		CommentID:   comment.ID,
		WorkspaceID: comment.WorkspaceID,
	}); err == nil {
		metadata["attachments"] = attachmentMetadataList(attachments)
	} else {
		slog.Warn("list comment attachments for user input trace failed",
			"task_id", util.UUIDToString(task.ID),
			"comment_id", util.UUIDToString(comment.ID),
			"error", err,
		)
	}
	return metadata
}

func (s *TaskService) buildChatUserInputMetadata(ctx context.Context, task db.AgentTaskQueue) map[string]any {
	session, err := s.Queries.GetChatSession(ctx, task.ChatSessionID)
	if err != nil {
		slog.Warn("build chat user input trace metadata failed",
			"task_id", util.UUIDToString(task.ID),
			"chat_session_id", util.UUIDToString(task.ChatSessionID),
			"error", err,
		)
		return s.buildDirectUserInputMetadata(task)
	}

	message, err := s.Queries.GetMostRecentUserChatMessage(ctx, task.ChatSessionID)
	content := session.Title
	sourceID := session.ID
	messageID := ""
	if err == nil {
		content = message.Content
		sourceID = message.ID
		messageID = util.UUIDToString(message.ID)
	} else {
		slog.Warn("load latest chat message for user input trace failed",
			"task_id", util.UUIDToString(task.ID),
			"chat_session_id", util.UUIDToString(task.ChatSessionID),
			"error", err,
		)
	}

	actorID := session.CreatorID
	if task.InitiatorUserID.Valid {
		actorID = task.InitiatorUserID
	}
	title := firstNonEmpty(session.Title, "Chat 消息")
	metadata := baseUserInputMetadata("chat", "member", actorID, "chat_message", sourceID, title, content)
	metadata["chat_session_id"] = util.UUIDToString(session.ID)
	metadata["source_url"] = "/chats/" + util.UUIDToString(session.ID)
	if messageID != "" {
		metadata["chat_message_id"] = messageID
		metadata["source_url"] = metadata["source_url"].(string) + "#message-" + messageID
		if attachments, err := s.Queries.ListAttachmentsByChatMessage(ctx, db.ListAttachmentsByChatMessageParams{
			ChatMessageID: message.ID,
			WorkspaceID:   session.WorkspaceID,
		}); err == nil {
			metadata["attachments"] = attachmentMetadataList(attachments)
		} else {
			slog.Warn("list chat attachments for user input trace failed",
				"task_id", util.UUIDToString(task.ID),
				"chat_message_id", messageID,
				"error", err,
			)
		}
	}
	return metadata
}

func (s *TaskService) buildQuickCreateUserInputMetadata(task db.AgentTaskQueue, qc QuickCreateContext) map[string]any {
	requesterID := pgtype.UUID{}
	if parsed, err := util.ParseUUID(qc.RequesterID); err == nil {
		requesterID = parsed
	}
	metadata := baseUserInputMetadata("quick_create", "member", requesterID, "quick_create", task.ID, "快速创建任务", qc.Prompt)
	metadata["task_id"] = util.UUIDToString(task.ID)
	metadata["workspace_id"] = qc.WorkspaceID
	putStringIfPresent(metadata, "project_id", qc.ProjectID)
	putStringIfPresent(metadata, "squad_id", qc.SquadID)
	putStringIfPresent(metadata, "parent_issue_id", qc.ParentIssueID)
	putStringIfPresent(metadata, "status", qc.Status)
	putStringIfPresent(metadata, "priority", qc.Priority)
	putStringIfPresent(metadata, "assignee_type", qc.AssigneeType)
	putStringIfPresent(metadata, "assignee_id", qc.AssigneeID)
	putStringIfPresent(metadata, "start_date", qc.StartDate)
	putStringIfPresent(metadata, "due_date", qc.DueDate)
	if len(qc.AttachmentIDs) > 0 {
		attachments := make([]map[string]any, 0, len(qc.AttachmentIDs))
		for _, id := range qc.AttachmentIDs {
			if id != "" {
				attachments = append(attachments, map[string]any{"id": id})
			}
		}
		metadata["attachments"] = attachments
	}
	return metadata
}

func (s *TaskService) buildAutopilotUserInputMetadata(ctx context.Context, task db.AgentTaskQueue, runID pgtype.UUID) map[string]any {
	run, err := s.Queries.GetAutopilotRun(ctx, runID)
	if err != nil {
		slog.Warn("build autopilot user input trace metadata failed",
			"task_id", util.UUIDToString(task.ID),
			"autopilot_run_id", util.UUIDToString(runID),
			"error", err,
		)
		return s.buildDirectUserInputMetadata(task)
	}
	ap, err := s.Queries.GetAutopilot(ctx, run.AutopilotID)
	if err != nil {
		slog.Warn("build autopilot user input trace metadata failed",
			"task_id", util.UUIDToString(task.ID),
			"autopilot_id", util.UUIDToString(run.AutopilotID),
			"error", err,
		)
		return s.buildDirectUserInputMetadata(task)
	}

	contentParts := []string{ap.Title}
	if ap.Description.Valid {
		contentParts = append(contentParts, ap.Description.String)
	}
	payloadSummary, payloadTruncated := summarizeRawJSON(run.TriggerPayload, triggerSummaryMaxLen)
	if payloadSummary != "" {
		contentParts = append(contentParts, payloadSummary)
	}
	content := strings.Join(contentParts, "\n\n")
	metadata := baseUserInputMetadata("autopilot", ap.CreatedByType, ap.CreatedByID, "autopilot_run", run.ID, ap.Title, content)
	metadata["autopilot_id"] = util.UUIDToString(ap.ID)
	metadata["autopilot_run_id"] = util.UUIDToString(run.ID)
	metadata["autopilot_source"] = run.Source
	metadata["trigger_payload_summary"] = payloadSummary
	metadata["trigger_payload_truncated"] = payloadTruncated
	if run.IssueID.Valid {
		metadata["issue_id"] = util.UUIDToString(run.IssueID)
		metadata["source_url"] = "/issues/" + util.UUIDToString(run.IssueID)
	}
	return metadata
}

func (s *TaskService) buildDirectUserInputMetadata(task db.AgentTaskQueue) map[string]any {
	content := "系统任务入队"
	if task.TriggerSummary.Valid && strings.TrimSpace(task.TriggerSummary.String) != "" {
		content = task.TriggerSummary.String
	}
	metadata := baseUserInputMetadata("direct", "system", pgtype.UUID{}, "task", task.ID, "直接任务", content)
	metadata["task_id"] = util.UUIDToString(task.ID)
	if task.ParentTaskID.Valid {
		metadata["parent_task_id"] = util.UUIDToString(task.ParentTaskID)
	}
	return metadata
}

func baseUserInputMetadata(inputKind, actorType string, actorID pgtype.UUID, sourceType string, sourceID pgtype.UUID, title, content string) map[string]any {
	snapshot, truncated := truncateSnapshot(content, userInputSnapshotMaxLen)
	metadata := map[string]any{
		"input_kind":        inputKind,
		"actor_type":        actorType,
		"actor_id":          util.UUIDToString(actorID),
		"source_type":       sourceType,
		"source_id":         util.UUIDToString(sourceID),
		"title":             strings.TrimSpace(title),
		"summary":           truncateForSummary(firstNonEmpty(content, title), triggerSummaryMaxLen),
		"content_snapshot":  snapshot,
		"content_truncated": truncated,
		"content_sha256":    userInputContentSHA256(content),
		"attachments":       []map[string]any{},
	}
	return metadata
}

func textWithTitle(title, body string) string {
	title = strings.TrimSpace(title)
	body = strings.TrimSpace(body)
	switch {
	case title == "":
		return body
	case body == "":
		return title
	default:
		return title + "\n\n" + body
	}
}

func truncateSnapshot(s string, maxRunes int) (string, bool) {
	rs := []rune(strings.TrimSpace(s))
	if len(rs) <= maxRunes {
		return string(rs), false
	}
	return string(rs[:maxRunes]), true
}

func userInputContentSHA256(s string) string {
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", sum[:])
}

func summarizeRawJSON(raw []byte, maxRunes int) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var decoded any
	content := strings.TrimSpace(string(raw))
	if json.Unmarshal(raw, &decoded) == nil {
		if compact, err := json.Marshal(decoded); err == nil {
			content = string(compact)
		}
	}
	return truncateSnapshot(content, maxRunes)
}

func attachmentMetadataList(attachments []db.Attachment) []map[string]any {
	out := make([]map[string]any, 0, len(attachments))
	for _, attachment := range attachments {
		out = append(out, map[string]any{
			"id":           util.UUIDToString(attachment.ID),
			"filename":     attachment.Filename,
			"content_type": attachment.ContentType,
			"size_bytes":   attachment.SizeBytes,
		})
	}
	return out
}

func putStringIfPresent(metadata map[string]any, key, value string) {
	if strings.TrimSpace(value) != "" {
		metadata[key] = value
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (s *TaskService) captureTaskDispatched(ctx context.Context, task db.AgentTaskQueue) {
	s.recordTaskTraceEvent(ctx, task, "task.dispatched", "任务已领取", taskTraceOptions{
		DurationMs:  taskQueueWaitMilliseconds(task),
		QueueWaitMs: taskQueueWaitMilliseconds(task),
	})
	if s.Metrics != nil {
		source, runtimeMode, _ := s.taskMetricsContext(ctx, task)
		s.Metrics.RecordTaskDispatched(util.UUIDToString(task.ID), source, runtimeMode, taskQueueWaitSeconds(task))
	}
}

func (s *TaskService) AnalyticsContextForTask(ctx context.Context, task db.AgentTaskQueue) analytics.TaskContext {
	return s.taskAnalyticsContext(ctx, task)
}

func (s *TaskService) captureTaskStarted(ctx context.Context, task db.AgentTaskQueue) {
	s.recordTaskTraceEvent(ctx, task, "task.started", "任务已开始", taskTraceOptions{
		QueueWaitMs: taskQueueWaitMilliseconds(task),
	})
	if s.Metrics != nil {
		source, runtimeMode, provider := s.taskMetricsContext(ctx, task)
		s.Metrics.RecordTaskStarted(source, runtimeMode, provider)
	}
}

func (s *TaskService) captureTaskCompleted(ctx context.Context, task db.AgentTaskQueue) {
	runMs := taskRunMilliseconds(task)
	s.recordTaskTraceEvent(ctx, task, "task.completed", "任务已完成", taskTraceOptions{
		DurationMs:  runMs,
		QueueWaitMs: taskQueueWaitMilliseconds(task),
		RunMs:       runMs,
		TotalMs:     taskTotalMilliseconds(task),
	})
	if s.Metrics != nil {
		source, runtimeMode, _ := s.taskMetricsContext(ctx, task)
		s.Metrics.RecordTaskTerminal(util.UUIDToString(task.ID), source, runtimeMode, task.Status, taskRunSeconds(task), taskTotalSeconds(task), task.Attempt)
	}
	s.syncPromptEvaluationRunForTask(ctx, task, "task_completed")
}

func (s *TaskService) captureTaskFailed(ctx context.Context, task db.AgentTaskQueue) {
	failureReason := taskFailureReason(task)
	runMs := taskRunMilliseconds(task)
	s.recordTaskTraceEvent(ctx, task, "task.failed", "任务已失败", taskTraceOptions{
		DurationMs:    runMs,
		QueueWaitMs:   taskQueueWaitMilliseconds(task),
		RunMs:         runMs,
		TotalMs:       taskTotalMilliseconds(task),
		FailureReason: failureReason,
		ErrorType:     taskErrorType(failureReason),
	})
	if s.Metrics != nil {
		source, runtimeMode, _ := s.taskMetricsContext(ctx, task)
		s.Metrics.RecordTaskTerminal(util.UUIDToString(task.ID), source, runtimeMode, task.Status, taskRunSeconds(task), taskTotalSeconds(task), task.Attempt)
		s.Metrics.RecordTaskFailed(source, runtimeMode, failureReason)
	}
}

func (s *TaskService) captureTaskCancelled(ctx context.Context, task db.AgentTaskQueue) {
	s.recordTaskCancelledTrace(ctx, s.Queries, task)
	s.finalizeTaskCancelledSideEffects(ctx, task)
}

// recordTaskCancelledTrace writes the task.cancelled observability row.
// Callers that hard-delete a chat session must pass the same transaction
// queries used for cancel+delete so chat_session_id still resolves while
// the session row is locked. The session id is also mirrored into metadata
// because task_trace_event.chat_session_id is ON DELETE SET NULL.
func (s *TaskService) recordTaskCancelledTrace(ctx context.Context, q *db.Queries, task db.AgentTaskQueue) {
	if q == nil {
		q = s.Queries
	}
	var metadata []byte
	if task.ChatSessionID.Valid {
		metadata = mergeTaskTraceMetadata(nil, map[string]any{
			"chat_session_id": util.UUIDToString(task.ChatSessionID),
		})
	}
	opts := taskTraceOptions{
		DurationMs:    taskTotalMilliseconds(task),
		QueueWaitMs:   taskQueueWaitMilliseconds(task),
		RunMs:         taskRunMilliseconds(task),
		TotalMs:       taskTotalMilliseconds(task),
		FailureReason: "cancelled",
		ErrorType:     "cancelled",
		Metadata:      metadata,
	}
	params, ok := s.buildTaskTraceEventParams(ctx, task, "task.cancelled", "任务已取消", opts)
	if !ok {
		return
	}
	if _, err := q.CreateTaskTraceEvent(ctx, params); err != nil {
		slog.Warn("record task trace event failed",
			"task_id", util.UUIDToString(task.ID),
			"event_type", "task.cancelled",
			"error", err,
		)
	}
}

// finalizeTaskCancelledSideEffects applies post-cancel metrics/token/eval
// bookkeeping that does not need to share the cancel transaction.
func (s *TaskService) finalizeTaskCancelledSideEffects(ctx context.Context, task db.AgentTaskQueue) {
	if s.Metrics != nil {
		source, runtimeMode, _ := s.taskMetricsContext(ctx, task)
		s.Metrics.RecordTaskTerminal(util.UUIDToString(task.ID), source, runtimeMode, task.Status, taskRunSeconds(task), taskTotalSeconds(task), task.Attempt)
	}
	// Revoke any mat_ task tokens minted for this task. Cancellation is
	// a terminal transition, so the running agent process no longer
	// needs to call back; eagerly deleting the token closes the
	// window where a compromised process could keep authenticating
	// against the API until the 24h expiry. Failure is non-fatal — the
	// expiry / FK cascade are the durable guards. MUL-2600.
	if err := s.Queries.DeleteTaskTokensByTask(ctx, task.ID); err != nil {
		slog.Warn("cancel task: failed to revoke task tokens",
			"task_id", util.UUIDToString(task.ID), "error", err)
	}
	s.syncPromptEvaluationRunForTask(ctx, task, "task_cancelled")
}

// CaptureCancelledTaskTracesInTx records cancel traces inside the caller's
// transaction. Used by DeleteChatSession so task_trace_event.chat_session_id
// still points at a live session row when the insert runs.
func (s *TaskService) CaptureCancelledTaskTracesInTx(ctx context.Context, q *db.Queries, cancelled []db.AgentTaskQueue) {
	for _, task := range cancelled {
		s.recordTaskCancelledTrace(ctx, q, task)
	}
}

// NotifyCancelledTasks reconciles agent status and broadcasts task:cancelled
// without writing another cancel trace. Call after commit when traces were
// already recorded in the cancel transaction (e.g. chat session delete).
func (s *TaskService) NotifyCancelledTasks(ctx context.Context, cancelled []db.AgentTaskQueue) {
	for _, t := range cancelled {
		s.finalizeTaskCancelledSideEffects(ctx, t)
		s.ReconcileAgentStatus(ctx, t.AgentID)
		s.broadcastTaskEvent(ctx, protocol.EventTaskCancelled, t)
	}
}

func (s *TaskService) CaptureTaskUsage(ctx context.Context, task db.AgentTaskQueue, provider, model string, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens int64) {
	s.recordTaskTraceEvent(ctx, task, "llm.usage_reported", "模型用量已上报", taskTraceOptions{
		Provider:         provider,
		Model:            model,
		InputTokens:      inputTokens,
		OutputTokens:     outputTokens,
		CacheReadTokens:  cacheReadTokens,
		CacheWriteTokens: cacheWriteTokens,
	})
	if s.Metrics == nil {
		return
	}
	source, runtimeMode, _ := s.taskMetricsContext(ctx, task)
	s.Metrics.RecordLLMUsage(source, runtimeMode, provider, model, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens)
}

func (s *TaskService) CaptureTaskUsageUnavailable(ctx context.Context, task db.AgentTaskQueue, reason string) {
	events, err := s.Queries.ListTaskTraceEventsByTask(ctx, task.ID)
	if err != nil {
		slog.Warn("list task trace events before usage unavailable trace failed",
			"task_id", util.UUIDToString(task.ID),
			"error", err,
		)
	} else {
		for _, event := range events {
			if event.EventType == "llm.usage_reported" || event.EventType == "llm.usage_unavailable" {
				return
			}
		}
	}

	metadata, _ := json.Marshal(map[string]string{
		"原因": reason,
		"说明": "执行器完成任务但模型没有返回 token 用量，本次任务不会计入 token 或成本聚合。",
	})
	s.recordTaskTraceEvent(ctx, task, "llm.usage_unavailable", "模型用量未返回", taskTraceOptions{
		Metadata: metadata,
	})
}

func (s *TaskService) CaptureQueuedExpiredTasks(ctx context.Context, tasks []db.AgentTaskQueue) {
	if s.Metrics == nil {
		return
	}
	for _, task := range tasks {
		source, runtimeMode, _ := s.taskMetricsContext(ctx, task)
		s.Metrics.RecordTaskQueuedExpired(source, runtimeMode)
	}
}

func (s *TaskService) CaptureLeaseExpiredTasks(ctx context.Context, tasks []db.AgentTaskQueue) {
	if s.Metrics == nil {
		return
	}
	for _, task := range tasks {
		source, _, _ := s.taskMetricsContext(ctx, task)
		s.Metrics.RecordTaskLeaseExpired(source)
	}
}

func (s *TaskService) cachedTaskAnalyticsContext(task db.AgentTaskQueue) (analytics.TaskContext, bool) {
	key := taskAnalyticsContextKey(task)
	if key == "" {
		return analytics.TaskContext{}, false
	}
	s.analyticsContextMu.Lock()
	defer s.analyticsContextMu.Unlock()
	if s.analyticsContextCache == nil {
		return analytics.TaskContext{}, false
	}
	tc, ok := s.analyticsContextCache[key]
	return tc, ok
}

func (s *TaskService) storeTaskAnalyticsContext(task db.AgentTaskQueue, tc analytics.TaskContext) {
	if tc.WorkspaceID == "" {
		return
	}
	key := taskAnalyticsContextKey(task)
	if key == "" {
		return
	}
	s.analyticsContextMu.Lock()
	defer s.analyticsContextMu.Unlock()
	if s.analyticsContextCache == nil {
		s.analyticsContextCache = make(map[string]analytics.TaskContext)
	}
	if _, ok := s.analyticsContextCache[key]; !ok {
		s.analyticsContextOrder = append(s.analyticsContextOrder, key)
		if len(s.analyticsContextOrder) > taskAnalyticsContextCacheMax {
			oldest := s.analyticsContextOrder[0]
			s.analyticsContextOrder = s.analyticsContextOrder[1:]
			delete(s.analyticsContextCache, oldest)
		}
	}
	s.analyticsContextCache[key] = tc
}

func taskAnalyticsContextKey(task db.AgentTaskQueue) string {
	taskID := util.UUIDToString(task.ID)
	if taskID == "" {
		return ""
	}
	return strings.Join([]string{
		taskID,
		util.UUIDToString(task.RuntimeID),
		util.UUIDToString(task.IssueID),
		util.UUIDToString(task.ChatSessionID),
		util.UUIDToString(task.AutopilotRunID),
	}, "|")
}

func (s *TaskService) taskMetricsContext(ctx context.Context, task db.AgentTaskQueue) (source, runtimeMode, provider string) {
	tc := s.taskAnalyticsContext(ctx, task)
	source = "other"
	switch {
	case task.ChatSessionID.Valid:
		source = "chat"
	case task.IssueID.Valid:
		if tc.Source == analytics.SourceAutopilot {
			source = "autopilot_issue"
		} else {
			source = "issue"
		}
	case task.AutopilotRunID.Valid:
		source = "autopilot"
	default:
		if _, ok := s.parseQuickCreateContext(task); ok {
			source = "quick_create"
		} else if tc.Source != "" {
			source = tc.Source
		}
	}
	return source, tc.RuntimeMode, tc.Provider
}

func (s *TaskService) taskAnalyticsContext(ctx context.Context, task db.AgentTaskQueue) analytics.TaskContext {
	if tc, ok := s.cachedTaskAnalyticsContext(task); ok {
		return tc
	}
	tc := analytics.TaskContext{
		AgentID: util.UUIDToString(task.AgentID),
		TaskID:  util.UUIDToString(task.ID),
		Source:  analytics.SourceManual,
	}
	if task.IssueID.Valid {
		tc.IssueID = util.UUIDToString(task.IssueID)
	}
	if task.ChatSessionID.Valid {
		tc.ChatSessionID = util.UUIDToString(task.ChatSessionID)
		tc.Source = analytics.SourceChat
	}
	if task.AutopilotRunID.Valid {
		tc.AutopilotRunID = util.UUIDToString(task.AutopilotRunID)
		tc.Source = analytics.SourceAutopilot
	}

	if task.RuntimeID.Valid {
		if rt, err := s.Queries.GetAgentRuntime(ctx, task.RuntimeID); err == nil {
			tc.WorkspaceID = util.UUIDToString(rt.WorkspaceID)
			tc.RuntimeMode = rt.RuntimeMode
			tc.Provider = rt.Provider
		}
	}
	if tc.WorkspaceID == "" || tc.RuntimeMode == "" {
		if agent, err := s.Queries.GetAgent(ctx, task.AgentID); err == nil {
			if tc.WorkspaceID == "" {
				tc.WorkspaceID = util.UUIDToString(agent.WorkspaceID)
			}
			if tc.RuntimeMode == "" {
				tc.RuntimeMode = agent.RuntimeMode
			}
		}
	}

	if task.IssueID.Valid {
		if issue, err := s.Queries.GetIssue(ctx, task.IssueID); err == nil {
			tc.WorkspaceID = util.UUIDToString(issue.WorkspaceID)
			if issue.CreatorType == "member" {
				tc.UserID = util.UUIDToString(issue.CreatorID)
			}
			if issue.OriginType.Valid {
				switch issue.OriginType.String {
				case "autopilot":
					tc.Source = analytics.SourceAutopilot
					if ap, err := s.Queries.GetAutopilot(ctx, issue.OriginID); err == nil {
						if ap.CreatedByType == "member" {
							tc.UserID = util.UUIDToString(ap.CreatedByID)
						}
					}
				case "quick_create":
					tc.Source = analytics.SourceManual
				}
			}
		}
	}
	if task.ChatSessionID.Valid {
		if cs, err := s.Queries.GetChatSession(ctx, task.ChatSessionID); err == nil {
			tc.WorkspaceID = util.UUIDToString(cs.WorkspaceID)
			tc.UserID = util.UUIDToString(cs.CreatorID)
		}
	}
	if task.AutopilotRunID.Valid {
		if run, err := s.Queries.GetAutopilotRun(ctx, task.AutopilotRunID); err == nil {
			if ap, err := s.Queries.GetAutopilot(ctx, run.AutopilotID); err == nil {
				tc.WorkspaceID = util.UUIDToString(ap.WorkspaceID)
				if ap.CreatedByType == "member" {
					tc.UserID = util.UUIDToString(ap.CreatedByID)
				}
			}
		}
	}
	if qc, ok := s.parseQuickCreateContext(task); ok {
		tc.WorkspaceID = qc.WorkspaceID
		tc.UserID = qc.RequesterID
		tc.Source = analytics.SourceManual
	}
	s.storeTaskAnalyticsContext(task, tc)
	return tc
}

type taskTraceOptions struct {
	DurationMs       pgtype.Int8
	QueueWaitMs      pgtype.Int8
	RunMs            pgtype.Int8
	TotalMs          pgtype.Int8
	Provider         string
	Model            string
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	FailureReason    string
	ErrorType        string
	Metadata         []byte
}

func (s *TaskService) recordTaskTraceEvent(ctx context.Context, task db.AgentTaskQueue, eventType, eventName string, opts taskTraceOptions) {
	params, ok := s.buildTaskTraceEventParams(ctx, task, eventType, eventName, opts)
	if !ok {
		return
	}
	if _, err := s.Queries.CreateTaskTraceEvent(ctx, params); err != nil {
		slog.Warn("record task trace event failed",
			"task_id", util.UUIDToString(task.ID),
			"event_type", eventType,
			"error", err,
		)
	}
}

func (s *TaskService) buildTaskTraceEventParams(ctx context.Context, task db.AgentTaskQueue, eventType, eventName string, opts taskTraceOptions) (db.CreateTaskTraceEventParams, bool) {
	source, _, providerFromRuntime := s.taskMetricsContext(ctx, task)
	workspaceID := pgtype.UUID{}
	issueID := task.IssueID
	runtimeID := task.RuntimeID
	squadID := pgtype.UUID{}
	projectID := pgtype.UUID{}

	if task.RuntimeID.Valid {
		if rt, err := s.Queries.GetAgentRuntime(ctx, task.RuntimeID); err == nil {
			workspaceID = rt.WorkspaceID
			if opts.Provider == "" {
				opts.Provider = rt.Provider
			}
		}
	}
	if task.IssueID.Valid {
		if issue, err := s.Queries.GetIssue(ctx, task.IssueID); err == nil {
			workspaceID = issue.WorkspaceID
			projectID = issue.ProjectID
			if issue.AssigneeType.Valid && issue.AssigneeType.String == "squad" {
				squadID = issue.AssigneeID
			}
		}
	}
	if task.AutopilotRunID.Valid {
		if run, err := s.Queries.GetAutopilotRun(ctx, task.AutopilotRunID); err == nil {
			if !issueID.Valid {
				issueID = run.IssueID
			}
			if run.SquadID.Valid {
				squadID = run.SquadID
			}
			if ap, err := s.Queries.GetAutopilot(ctx, run.AutopilotID); err == nil {
				workspaceID = ap.WorkspaceID
				if !projectID.Valid {
					projectID = ap.ProjectID
				}
			}
		}
	}
	if qc, ok := s.parseQuickCreateContext(task); ok {
		if !workspaceID.Valid && qc.WorkspaceID != "" {
			if parsed, err := util.ParseUUID(qc.WorkspaceID); err == nil {
				workspaceID = parsed
			}
		}
		if qc.SquadID != "" {
			if parsed, err := util.ParseUUID(qc.SquadID); err == nil {
				squadID = parsed
			}
		}
		if !projectID.Valid && qc.ProjectID != "" {
			if parsed, err := util.ParseUUID(qc.ProjectID); err == nil {
				projectID = parsed
			}
		}
	}
	if !workspaceID.Valid {
		if agent, err := s.Queries.GetAgent(ctx, task.AgentID); err == nil {
			workspaceID = agent.WorkspaceID
			if !runtimeID.Valid {
				runtimeID = agent.RuntimeID
			}
		}
	}
	if !workspaceID.Valid {
		tc := s.taskAnalyticsContext(ctx, task)
		if parsed, err := util.ParseUUID(tc.WorkspaceID); err == nil {
			workspaceID = parsed
		}
	}
	if !workspaceID.Valid {
		return db.CreateTaskTraceEventParams{}, false
	}
	if opts.Provider == "" {
		opts.Provider = providerFromRuntime
	}

	// task_trace_event.chat_session_id is a hard FK. DeleteChatSession writes
	// cancel traces before delete inside the same tx; other post-commit
	// cancel paths may race with session hard-delete. Prefer keeping the
	// cancel evidence and drop only the FK column when the session is gone,
	// retaining the id in metadata for later forensics.
	chatSessionID := task.ChatSessionID
	metadata := opts.Metadata
	if chatSessionID.Valid {
		if _, err := s.Queries.GetChatSession(ctx, chatSessionID); err != nil {
			sessionID := util.UUIDToString(chatSessionID)
			metadata = mergeTaskTraceMetadata(metadata, map[string]any{
				"deleted_chat_session_id": sessionID,
				"chat_session_missing":    true,
			})
			chatSessionID = pgtype.UUID{}
		}
	}

	return db.CreateTaskTraceEventParams{
		WorkspaceID:      workspaceID,
		TaskID:           task.ID,
		IssueID:          issueID,
		AgentID:          task.AgentID,
		RuntimeID:        runtimeID,
		SquadID:          squadID,
		ProjectID:        projectID,
		Source:           source,
		EventType:        eventType,
		EventName:        eventName,
		Status:           task.Status,
		Attempt:          task.Attempt,
		DurationMs:       opts.DurationMs,
		QueueWaitMs:      opts.QueueWaitMs,
		RunMs:            opts.RunMs,
		TotalMs:          opts.TotalMs,
		Provider:         opts.Provider,
		Model:            opts.Model,
		InputTokens:      opts.InputTokens,
		OutputTokens:     opts.OutputTokens,
		CacheReadTokens:  opts.CacheReadTokens,
		CacheWriteTokens: opts.CacheWriteTokens,
		FailureReason:    opts.FailureReason,
		ErrorType:        opts.ErrorType,
		TriggerCommentID: task.TriggerCommentID,
		AutopilotRunID:   task.AutopilotRunID,
		ChatSessionID:    chatSessionID,
		Metadata:         metadata,
	}, true
}

func mergeTaskTraceMetadata(raw []byte, extra map[string]any) []byte {
	base := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &base); err != nil {
			base = map[string]any{"raw_metadata": string(raw)}
		}
	}
	for k, v := range extra {
		base[k] = v
	}
	encoded, err := json.Marshal(base)
	if err != nil {
		return raw
	}
	return encoded
}

func taskQueueWaitSeconds(task db.AgentTaskQueue) float64 {
	return durationSeconds(task.CreatedAt, task.DispatchedAt)
}

func taskRunSeconds(task db.AgentTaskQueue) float64 {
	return durationSeconds(task.StartedAt, task.CompletedAt)
}

func taskTotalSeconds(task db.AgentTaskQueue) float64 {
	return durationSeconds(task.CreatedAt, task.CompletedAt)
}

func durationSeconds(start, end pgtype.Timestamptz) float64 {
	if !start.Valid || !end.Valid {
		return -1
	}
	seconds := end.Time.Sub(start.Time).Seconds()
	if seconds < 0 {
		return 0
	}
	return seconds
}

func taskQueueWaitMilliseconds(task db.AgentTaskQueue) pgtype.Int8 {
	return durationMilliseconds(task.CreatedAt, task.DispatchedAt)
}

func taskRunMilliseconds(task db.AgentTaskQueue) pgtype.Int8 {
	return durationMilliseconds(task.StartedAt, task.CompletedAt)
}

func taskTotalMilliseconds(task db.AgentTaskQueue) pgtype.Int8 {
	return durationMilliseconds(task.CreatedAt, task.CompletedAt)
}

func durationMilliseconds(start, end pgtype.Timestamptz) pgtype.Int8 {
	if !start.Valid || !end.Valid {
		return pgtype.Int8{}
	}
	ms := end.Time.Sub(start.Time).Milliseconds()
	if ms < 0 {
		ms = 0
	}
	return pgtype.Int8{Int64: ms, Valid: true}
}

func taskFailureReason(task db.AgentTaskQueue) string {
	if task.FailureReason.Valid && task.FailureReason.String != "" {
		return task.FailureReason.String
	}
	return "agent_error"
}

func taskErrorType(reason string) string {
	switch reason {
	case "runtime_offline", "runtime_recovery":
		return "runtime"
	case "timeout", "codex_semantic_inactivity":
		return "timeout"
	case "iteration_limit", "agent_fallback_message":
		return "agent_output"
	case "cancelled", "user_cancelled":
		return "cancelled"
	default:
		return "agent_error"
	}
}

// EnqueueTaskForIssue creates a queued task for an agent-assigned issue.
// No context snapshot is stored — the agent fetches all data it needs at
// runtime via the multica CLI.
func (s *TaskService) EnqueueTaskForIssue(ctx context.Context, issue db.Issue, triggerCommentID ...pgtype.UUID) (db.AgentTaskQueue, error) {
	var commentID pgtype.UUID
	if len(triggerCommentID) > 0 {
		commentID = triggerCommentID[0]
	}
	return s.enqueueIssueTask(ctx, issue, commentID, false)
}

// enqueueIssueTask is the shared implementation behind EnqueueTaskForIssue
// and the manual rerun path. forceFreshSession=true marks the task so the
// daemon claim handler skips the (agent_id, issue_id) resume lookup — the
// user already judged the prior output bad, a fresh agent session is the
// expected behavior.
func (s *TaskService) enqueueIssueTask(ctx context.Context, issue db.Issue, triggerCommentID pgtype.UUID, forceFreshSession bool) (db.AgentTaskQueue, error) {
	if !issue.AssigneeID.Valid {
		slog.Error("task enqueue failed", "issue_id", util.UUIDToString(issue.ID), "error", "issue has no assignee")
		return db.AgentTaskQueue{}, fmt.Errorf("issue has no assignee")
	}

	agent, err := s.Queries.GetAgent(ctx, issue.AssigneeID)
	if err != nil {
		slog.Error("task enqueue failed", "issue_id", util.UUIDToString(issue.ID), "error", err)
		return db.AgentTaskQueue{}, fmt.Errorf("load agent: %w", err)
	}
	if agent.ArchivedAt.Valid {
		slog.Debug("task enqueue skipped: agent is archived", "issue_id", util.UUIDToString(issue.ID), "agent_id", util.UUIDToString(agent.ID))
		return db.AgentTaskQueue{}, fmt.Errorf("agent is archived")
	}
	if !agent.RuntimeID.Valid {
		slog.Error("task enqueue failed", "issue_id", util.UUIDToString(issue.ID), "error", "agent has no runtime")
		return db.AgentTaskQueue{}, fmt.Errorf("agent has no runtime")
	}

	task, err := s.Queries.CreateAgentTask(ctx, db.CreateAgentTaskParams{
		AgentID:           issue.AssigneeID,
		RuntimeID:         agent.RuntimeID,
		IssueID:           issue.ID,
		Priority:          priorityToInt(issue.Priority),
		TriggerCommentID:  triggerCommentID,
		TriggerSummary:    s.buildCommentTriggerSummary(ctx, triggerCommentID),
		ForceFreshSession: pgtype.Bool{Bool: forceFreshSession, Valid: forceFreshSession},
	})
	if err != nil {
		slog.Error("task enqueue failed", "issue_id", util.UUIDToString(issue.ID), "error", err)
		return db.AgentTaskQueue{}, fmt.Errorf("create task: %w", err)
	}

	slog.Info("task enqueued",
		"task_id", util.UUIDToString(task.ID),
		"issue_id", util.UUIDToString(issue.ID),
		"agent_id", util.UUIDToString(issue.AssigneeID),
		"force_fresh_session", forceFreshSession,
	)
	// Order matters: broadcast first, notify daemon second. notifyTaskAvailable
	// kicks an in-process channel that the daemon picks up over HTTP and
	// claims; the claim path then emits its own task:dispatch. Doing the
	// queued broadcast afterwards risks the dispatch event reaching clients
	// before the queued one (rare but unsafe-by-construction). Publishing
	// in the desired observe-order makes correctness independent of timing.
	s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, task)
	s.NotifyTaskEnqueued(ctx, task)
	return task, nil
}

// EnqueueTaskForMention creates a queued task for a mentioned agent on an issue.
// Unlike EnqueueTaskForIssue, this takes an explicit agent ID rather than
// deriving it from the issue assignee.
func (s *TaskService) EnqueueTaskForMention(ctx context.Context, issue db.Issue, agentID pgtype.UUID, triggerCommentID pgtype.UUID) (db.AgentTaskQueue, error) {
	return s.enqueueMentionTask(ctx, issue, agentID, triggerCommentID, false, false)
}

// EnqueueTaskForSquadLeader is the leader-role variant of EnqueueTaskForMention.
// The resulting task carries is_leader_task=true so that downstream
// self-trigger guards can distinguish a comment posted while the agent was
// acting as the squad's leader (skip) from one posted while it was acting
// as a worker (do not skip). This matters for agents that are simultaneously
// the leader and a worker of the same squad — see migration 090.
func (s *TaskService) EnqueueTaskForSquadLeader(ctx context.Context, issue db.Issue, leaderID pgtype.UUID, triggerCommentID pgtype.UUID) (db.AgentTaskQueue, error) {
	return s.enqueueMentionTask(ctx, issue, leaderID, triggerCommentID, true, true)
}

// EnqueueProjectOwnerApprovalTask asks a project lead agent to review a
// backlog child issue before the issue's assigned SOP squad is allowed to run.
// It deliberately does not use is_leader_task: the agent is acting as project
// owner/reviewer, not as the issue assignee or squad leader.
func (s *TaskService) EnqueueProjectOwnerApprovalTask(ctx context.Context, issue db.Issue, project db.Project) (db.AgentTaskQueue, error) {
	if !project.LeadType.Valid || project.LeadType.String != "agent" || !project.LeadID.Valid {
		return db.AgentTaskQueue{}, fmt.Errorf("project lead is not an agent")
	}
	agent, err := s.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID:          project.LeadID,
		WorkspaceID: project.WorkspaceID,
	})
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("load project lead agent: %w", err)
	}
	if agent.ArchivedAt.Valid {
		return db.AgentTaskQueue{}, fmt.Errorf("project lead agent is archived")
	}
	if !agent.RuntimeID.Valid {
		return db.AgentTaskQueue{}, fmt.Errorf("project lead agent has no runtime")
	}
	task, err := s.Queries.CreateAgentTask(ctx, db.CreateAgentTaskParams{
		AgentID:           agent.ID,
		RuntimeID:         agent.RuntimeID,
		IssueID:           issue.ID,
		Priority:          priorityToInt(issue.Priority),
		TriggerSummary:    pgtype.Text{String: fmt.Sprintf("Project owner approval review for %s", project.Title), Valid: true},
		ForceFreshSession: pgtype.Bool{Bool: true, Valid: true},
	})
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("create project owner approval task: %w", err)
	}
	slog.Info("project owner approval task enqueued",
		"task_id", util.UUIDToString(task.ID),
		"issue_id", util.UUIDToString(issue.ID),
		"agent_id", util.UUIDToString(agent.ID),
		"project_id", util.UUIDToString(project.ID),
	)
	s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, task)
	s.NotifyTaskEnqueued(ctx, task)
	return task, nil
}

func (s *TaskService) enqueueMentionTask(ctx context.Context, issue db.Issue, agentID pgtype.UUID, triggerCommentID pgtype.UUID, isLeader bool, forceFreshSession bool) (db.AgentTaskQueue, error) {
	agent, err := s.Queries.GetAgent(ctx, agentID)
	if err != nil {
		slog.Error("mention task enqueue failed: agent not found", "issue_id", util.UUIDToString(issue.ID), "agent_id", util.UUIDToString(agentID), "error", err)
		return db.AgentTaskQueue{}, fmt.Errorf("load agent: %w", err)
	}
	if agent.ArchivedAt.Valid {
		slog.Debug("mention task enqueue skipped: agent is archived", "issue_id", util.UUIDToString(issue.ID), "agent_id", util.UUIDToString(agentID))
		return db.AgentTaskQueue{}, fmt.Errorf("agent is archived")
	}
	if !agent.RuntimeID.Valid {
		slog.Error("mention task enqueue failed: agent has no runtime", "issue_id", util.UUIDToString(issue.ID), "agent_id", util.UUIDToString(agentID))
		return db.AgentTaskQueue{}, fmt.Errorf("agent has no runtime")
	}

	task, err := s.Queries.CreateAgentTask(ctx, db.CreateAgentTaskParams{
		AgentID:           agentID,
		RuntimeID:         agent.RuntimeID,
		IssueID:           issue.ID,
		Priority:          priorityToInt(issue.Priority),
		TriggerCommentID:  triggerCommentID,
		TriggerSummary:    s.buildCommentTriggerSummary(ctx, triggerCommentID),
		IsLeaderTask:      pgtype.Bool{Bool: isLeader, Valid: isLeader},
		ForceFreshSession: pgtype.Bool{Bool: forceFreshSession, Valid: forceFreshSession},
	})
	if err != nil {
		slog.Error("mention task enqueue failed", "issue_id", util.UUIDToString(issue.ID), "agent_id", util.UUIDToString(agentID), "error", err)
		return db.AgentTaskQueue{}, fmt.Errorf("create task: %w", err)
	}

	slog.Info("mention task enqueued", "task_id", util.UUIDToString(task.ID), "issue_id", util.UUIDToString(issue.ID), "agent_id", util.UUIDToString(agentID), "is_leader_task", isLeader)
	if isLeader {
		s.ensureSquadSOPRunForLeaderTask(ctx, issue, task)
	}
	// See EnqueueTaskForIssue for ordering rationale.
	s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, task)
	s.NotifyTaskEnqueued(ctx, task)
	return task, nil
}

func (s *TaskService) ensureSquadSOPRunForLeaderTask(ctx context.Context, issue db.Issue, task db.AgentTaskQueue) {
	if !issue.AssigneeType.Valid || issue.AssigneeType.String != "squad" || !issue.AssigneeID.Valid {
		return
	}
	squad, err := s.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
		ID:          issue.AssigneeID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		slog.Warn("create squad SOP run skipped: squad not found",
			"issue_id", util.UUIDToString(issue.ID),
			"squad_id", util.UUIDToString(issue.AssigneeID),
			"error", err)
		return
	}

	profile := normalizeSquadSOPProfile(squad.SopProfile)
	profileKey, currentStepKey, currentStepName, roleKey := squadSOPProfileSummary(profile)
	run, err := s.Queries.CreateSquadSOPRun(ctx, db.CreateSquadSOPRunParams{
		WorkspaceID:    issue.WorkspaceID,
		IssueID:        issue.ID,
		SquadID:        squad.ID,
		LeaderTaskID:   task.ID,
		ProfileKey:     profileKey,
		Profile:        profile,
		Status:         "进行中",
		CurrentStepKey: currentStepKey,
	})
	if err != nil {
		slog.Warn("create squad SOP run failed",
			"issue_id", util.UUIDToString(issue.ID),
			"squad_id", util.UUIDToString(squad.ID),
			"task_id", util.UUIDToString(task.ID),
			"error", err)
		return
	}
	if currentStepKey == "" {
		return
	}
	if _, err := s.Queries.CreateSquadSOPStepEvent(ctx, db.CreateSquadSOPStepEventParams{
		RunID:         run.ID,
		WorkspaceID:   issue.WorkspaceID,
		IssueID:       issue.ID,
		SquadID:       squad.ID,
		StepKey:       currentStepKey,
		StepName:      currentStepName,
		RoleKey:       roleKey,
		EventType:     "步骤开始",
		Status:        "进行中",
		Reason:        "队长任务已入队，自动进入小队 SOP 执行。",
		CreatedByType: "system",
		TaskID:        task.ID,
	}); err != nil {
		slog.Warn("create squad SOP initial step event failed",
			"run_id", util.UUIDToString(run.ID),
			"task_id", util.UUIDToString(task.ID),
			"error", err)
	}
}

func firstSOPStringField(obj map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := obj[key].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func normalizeSquadSOPProfile(raw []byte) []byte {
	if len(raw) == 0 || string(raw) == "null" {
		return []byte(`{}`)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return []byte(`{}`)
	}
	normalized, err := json.Marshal(obj)
	if err != nil {
		return []byte(`{}`)
	}
	return normalized
}

func squadSOPProfileSummary(profile []byte) (profileKey, currentStepKey, currentStepName, roleKey string) {
	profileKey = "custom"
	var obj map[string]any
	if err := json.Unmarshal(profile, &obj); err != nil || obj == nil {
		return profileKey, "", "", ""
	}
	if v, ok := obj["profile_key"].(string); ok && strings.TrimSpace(v) != "" {
		profileKey = strings.TrimSpace(v)
	}
	steps, _ := obj["steps"].([]any)
	if len(steps) == 0 {
		return profileKey, "", "", ""
	}
	step, _ := steps[0].(map[string]any)
	if step == nil {
		return profileKey, "", "", ""
	}
	for _, key := range []string{"key", "step_key", "id"} {
		if v, ok := step[key].(string); ok && strings.TrimSpace(v) != "" {
			currentStepKey = strings.TrimSpace(v)
			break
		}
	}
	for _, key := range []string{"name", "title", "label"} {
		if v, ok := step[key].(string); ok && strings.TrimSpace(v) != "" {
			currentStepName = strings.TrimSpace(v)
			break
		}
	}
	for _, key := range []string{"role_key", "role"} {
		if v, ok := step[key].(string); ok && strings.TrimSpace(v) != "" {
			roleKey = strings.TrimSpace(v)
			break
		}
	}
	return profileKey, currentStepKey, currentStepName, roleKey
}

// QuickCreateContext is the JSON payload stored on a quick-create task's
// context column. The daemon detects this variant via Type == "quick_create"
// and switches to the quick-create prompt template; the completion path
// uses RequesterID + WorkspaceID to write the inbox notification.
//
// ProjectID is the optional project the user picked in the modal. When
// non-empty the daemon claim handler resolves the project's title +
// resources, and the prompt template instructs the agent to pass
// `--project <uuid>` so the new issue lands in that project.
//
// SquadID is non-empty when the user picked a squad (rather than an agent)
// in the modal. The task is still enqueued against the squad's leader
// agent (Queries.CreateQuickCreateTask is agent-scoped); SquadID is the
// hint the daemon claim handler uses to layer the squad-leader briefing
// onto the agent's Instructions, matching the behavior of issue-bound
// tasks assigned to the squad.
type QuickCreateContext struct {
	Type          string   `json:"type"`
	Prompt        string   `json:"prompt"`
	RequesterID   string   `json:"requester_id"`
	WorkspaceID   string   `json:"workspace_id"`
	ProjectID     string   `json:"project_id,omitempty"`
	SquadID       string   `json:"squad_id,omitempty"`
	Status        string   `json:"status,omitempty"`
	Priority      string   `json:"priority,omitempty"`
	AssigneeType  string   `json:"assignee_type,omitempty"`
	AssigneeID    string   `json:"assignee_id,omitempty"`
	StartDate     string   `json:"start_date,omitempty"`
	DueDate       string   `json:"due_date,omitempty"`
	AttachmentIDs []string `json:"attachment_ids,omitempty"`
	// ParentIssueID is the optional UUID of the parent issue the new issue
	// should be filed under. Set when the user opens the modal from "Add
	// sub issue" on an existing issue; the daemon claim handler resolves the
	// parent's identifier and the prompt template instructs the agent to
	// pass `--parent <uuid>` so the sub-issue relationship is preserved
	// across the manual→agent mode flip.
	ParentIssueID string `json:"parent_issue_id,omitempty"`
}

// QuickCreateContextType marks a task as a quick-create job.
const QuickCreateContextType = "quick_create"

const IssueSourceSummaryContextType = "issue_source_summary"

type IssueSourceSummaryContext struct {
	Type         string `json:"type"`
	Provider     string `json:"provider,omitempty"`
	SourceURL    string `json:"source_url,omitempty"`
	ResourceType string `json:"resource_type,omitempty"`
	ResourceID   string `json:"resource_id,omitempty"`
}

type EnqueueQuickCreateTaskParams struct {
	WorkspaceID   pgtype.UUID
	RequesterID   pgtype.UUID
	AgentID       pgtype.UUID
	SquadID       pgtype.UUID
	Prompt        string
	ProjectID     pgtype.UUID
	ParentIssueID pgtype.UUID
	AttachmentIDs []pgtype.UUID
	Status        string
	Priority      string
	AssigneeType  string
	AssigneeID    pgtype.UUID
	StartDate     string
	DueDate       string
}

// EnqueueQuickCreateTask creates a queued task that has no issue / chat /
// autopilot link — the user's natural-language prompt is stored in the
// task's context JSONB and the agent is expected to translate it into a
// `multica issue create` call. Pre-validates that the agent is reachable
// (not archived, has a runtime) so the API can reject up-front rather than
// queue a task no one will ever claim.
//
// projectID is optional (zero-valued pgtype.UUID when the user didn't pick
// one). The handler is responsible for validating it belongs to the same
// workspace before passing it in.
//
// squadID is non-empty (Valid) when the user picked a squad as the actor.
// The handler has already resolved it to the squad's leader agent for
// agentID; the squadID hint is stamped into the task context so the daemon
// claim handler can inject the squad-leader briefing on dispatch.
//
// parentIssueID is optional (zero-valued pgtype.UUID when the user didn't
// open the modal from "Add sub issue"). The handler is responsible for
// validating it belongs to the same workspace before passing it in.
func (s *TaskService) EnqueueQuickCreateTask(ctx context.Context, p EnqueueQuickCreateTaskParams) (db.AgentTaskQueue, error) {
	agent, err := s.Queries.GetAgent(ctx, p.AgentID)
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("load agent: %w", err)
	}
	if agent.ArchivedAt.Valid {
		return db.AgentTaskQueue{}, fmt.Errorf("agent is archived")
	}
	if !agent.RuntimeID.Valid {
		return db.AgentTaskQueue{}, fmt.Errorf("agent has no runtime")
	}

	payload := QuickCreateContext{
		Type:        QuickCreateContextType,
		Prompt:      p.Prompt,
		RequesterID: util.UUIDToString(p.RequesterID),
		WorkspaceID: util.UUIDToString(p.WorkspaceID),
	}
	if p.ProjectID.Valid {
		payload.ProjectID = util.UUIDToString(p.ProjectID)
	}
	if p.SquadID.Valid {
		payload.SquadID = util.UUIDToString(p.SquadID)
	}
	if p.ParentIssueID.Valid {
		payload.ParentIssueID = util.UUIDToString(p.ParentIssueID)
	}
	if p.Status != "" {
		payload.Status = p.Status
	}
	if p.Priority != "" {
		payload.Priority = p.Priority
	}
	if p.AssigneeType != "" && p.AssigneeID.Valid {
		payload.AssigneeType = p.AssigneeType
		payload.AssigneeID = util.UUIDToString(p.AssigneeID)
	}
	if p.StartDate != "" {
		payload.StartDate = p.StartDate
	}
	if p.DueDate != "" {
		payload.DueDate = p.DueDate
	}
	if len(p.AttachmentIDs) > 0 {
		payload.AttachmentIDs = make([]string, 0, len(p.AttachmentIDs))
		for _, id := range p.AttachmentIDs {
			if id.Valid {
				payload.AttachmentIDs = append(payload.AttachmentIDs, util.UUIDToString(id))
			}
		}
	}
	contextJSON, err := json.Marshal(payload)
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("marshal quick-create context: %w", err)
	}

	task, err := s.Queries.CreateQuickCreateTask(ctx, db.CreateQuickCreateTaskParams{
		AgentID:   p.AgentID,
		RuntimeID: agent.RuntimeID,
		Priority:  priorityToInt("high"),
		Context:   contextJSON,
	})
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("create quick-create task: %w", err)
	}

	slog.Info("quick-create task enqueued",
		"task_id", util.UUIDToString(task.ID),
		"agent_id", util.UUIDToString(p.AgentID),
		"squad_id", payload.SquadID,
		"requester_id", util.UUIDToString(p.RequesterID),
		"workspace_id", util.UUIDToString(p.WorkspaceID),
		"project_id", payload.ProjectID,
		"parent_issue_id", payload.ParentIssueID,
	)
	// Match every other Enqueue* path: kick the daemon WS so the task
	// gets claimed promptly instead of waiting for the next 30 s poll
	// cycle. Without this the user perceives "quick create never
	// triggered" because the modal closes immediately and the task
	// sits in 'queued' until the next sleepWithContextOrWakeup tick.
	s.NotifyTaskEnqueued(ctx, task)
	return task, nil
}

func (s *TaskService) EnqueueIssueSourceSummaryTask(ctx context.Context, issue db.Issue, agentID pgtype.UUID) (db.AgentTaskQueue, error) {
	if !agentID.Valid {
		return db.AgentTaskQueue{}, fmt.Errorf("source summary agent is required")
	}
	agent, err := s.Queries.GetAgent(ctx, agentID)
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("load source summary agent: %w", err)
	}
	if agent.ArchivedAt.Valid {
		return db.AgentTaskQueue{}, fmt.Errorf("source summary agent is archived")
	}
	if !agent.RuntimeID.Valid {
		return db.AgentTaskQueue{}, fmt.Errorf("source summary agent has no runtime")
	}
	ctxPayload := IssueSourceSummaryContext{
		Type:         IssueSourceSummaryContextType,
		Provider:     issueMetadataString(issue.Metadata, "source_provider"),
		SourceURL:    issueMetadataString(issue.Metadata, "source_url"),
		ResourceType: issueMetadataString(issue.Metadata, "tapd_resource_type"),
		ResourceID:   issueMetadataString(issue.Metadata, "tapd_resource_id"),
	}
	contextJSON, err := json.Marshal(ctxPayload)
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("marshal source summary context: %w", err)
	}
	task, err := s.Queries.CreateAgentTask(ctx, db.CreateAgentTaskParams{
		AgentID:           agent.ID,
		RuntimeID:         agent.RuntimeID,
		IssueID:           issue.ID,
		Priority:          priorityToInt("high"),
		TriggerSummary:    pgtype.Text{String: "为 TAPD 来源生成需求摘要", Valid: true},
		ForceFreshSession: pgtype.Bool{Bool: true, Valid: true},
		Context:           contextJSON,
	})
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("create source summary task: %w", err)
	}
	slog.Info("issue source summary task enqueued",
		"task_id", util.UUIDToString(task.ID),
		"issue_id", util.UUIDToString(issue.ID),
		"agent_id", util.UUIDToString(agent.ID),
	)
	s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, task)
	s.NotifyTaskEnqueued(ctx, task)
	return task, nil
}

// ErrChatTaskAgentArchived signals that EnqueueChatTask refused to
// queue work because the destination agent has been archived. This
// is a productizable state — surface it to the user as "this agent
// has been archived" rather than retrying.
var ErrChatTaskAgentArchived = errors.New("chat task: agent archived")

// ErrChatTaskAgentNoRuntime signals that EnqueueChatTask refused to
// queue work because the agent has never been associated with a
// runtime (agent.runtime_id IS NULL). This is the "agent has no
// daemon configured" case — productizable as "agent offline".
//
// IMPORTANT: this is NOT the same as "the daemon is currently
// disconnected". When agent.runtime_id IS set, EnqueueChatTask
// enqueues the task and the daemon claims it on next online; that
// path returns a task row, not this error.
var ErrChatTaskAgentNoRuntime = errors.New("chat task: agent has no runtime")

// EnqueueChatTask creates a queued task for a chat session.
// Unlike issue tasks, chat tasks have no issue_id.
//
// Errors split into two layers:
//
//   - Productizable rejections (agent archived, no runtime) return
//     the sentinel errors above. Callers (e.g. the Lark dispatcher)
//     can errors.Is them to decide a user-visible outcome.
//
//   - Infrastructure failures (DB load / insert errors) are wrapped
//     as ordinary errors. The caller should treat them as retryable
//     or page-worthy, NOT as user-facing state.
//
// initiatorUserID is the user who actually sent the triggering message — the
// real requester behind this run. Callers pass it explicitly because
// chat_session.creator_id is not a reliable source: Lark group sessions set the
// creator to the installer, not the sender (see the lark dispatcher). Web chat
// passes the request user; the lark dispatcher passes the inbound sender of the
// latest message in the silence window. Stored on the task so the daemon brief
// can attribute the run to the right person. See MUL-2645.
func (s *TaskService) EnqueueChatTask(ctx context.Context, chatSession db.ChatSession, initiatorUserID pgtype.UUID) (db.AgentTaskQueue, error) {
	agent, err := s.Queries.GetAgent(ctx, chatSession.AgentID)
	if err != nil {
		slog.Error("chat task enqueue failed", "chat_session_id", util.UUIDToString(chatSession.ID), "error", err)
		return db.AgentTaskQueue{}, fmt.Errorf("load agent: %w", err)
	}
	if agent.ArchivedAt.Valid {
		return db.AgentTaskQueue{}, ErrChatTaskAgentArchived
	}
	if !agent.RuntimeID.Valid {
		return db.AgentTaskQueue{}, ErrChatTaskAgentNoRuntime
	}

	task, err := s.Queries.CreateChatTask(ctx, db.CreateChatTaskParams{
		AgentID:         chatSession.AgentID,
		RuntimeID:       agent.RuntimeID,
		Priority:        2, // medium priority for chat
		ChatSessionID:   chatSession.ID,
		InitiatorUserID: initiatorUserID,
	})
	if err != nil {
		slog.Error("chat task enqueue failed", "chat_session_id", util.UUIDToString(chatSession.ID), "error", err)
		return db.AgentTaskQueue{}, fmt.Errorf("create chat task: %w", err)
	}

	slog.Info("chat task enqueued", "task_id", util.UUIDToString(task.ID), "chat_session_id", util.UUIDToString(chatSession.ID), "agent_id", util.UUIDToString(chatSession.AgentID))
	// See EnqueueTaskForIssue for ordering rationale.
	s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, task)
	s.NotifyTaskEnqueued(ctx, task)
	return task, nil
}

// CancelTasksForIssue cancels every active task on the issue, reconciles each
// affected agent's status, and broadcasts task:cancelled events so frontends
// clear their live cards.
//
// Before #1587 this path was "cancel rows and return" — issue-status flips
// (e.g. user marks the issue `done` or `cancelled` while a task is still
// running) left the agent stuck at status="working" indefinitely, requiring a
// manual `multica agent update <id> --status idle` to unwedge. Matches the
// pattern already used by CancelTask and RerunIssue.
func (s *TaskService) CancelTasksForIssue(ctx context.Context, issueID pgtype.UUID) error {
	cancelled, err := s.Queries.CancelAgentTasksByIssue(ctx, issueID)
	if err != nil {
		return err
	}
	for _, t := range cancelled {
		s.captureTaskCancelled(ctx, t)
		s.ReconcileAgentStatus(ctx, t.AgentID)
		s.broadcastTaskEvent(ctx, protocol.EventTaskCancelled, t)
	}
	return nil
}

// CancelTasksForAgent cancels every active task belonging to an agent
// (queued + dispatched + running), reconciles the agent's status, and
// broadcasts task:cancelled events. Used by the agent-level "Cancel all
// tasks" action — same shape as CancelTasksForIssue but scoped on agent_id.
//
// Returns the cancelled rows so callers can report counts / log them.
func (s *TaskService) CancelTasksForAgent(ctx context.Context, agentID pgtype.UUID) ([]db.AgentTaskQueue, error) {
	cancelled, err := s.Queries.CancelAgentTasksByAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	for _, t := range cancelled {
		s.captureTaskCancelled(ctx, t)
		s.broadcastTaskEvent(ctx, protocol.EventTaskCancelled, t)
	}
	// Reconcile once after the loop — agent transitions from
	// working→available based on remaining task counts, no need to call
	// per row (the rows we just cancelled all belong to the same agent).
	s.ReconcileAgentStatus(ctx, agentID)
	return cancelled, nil
}

// CancelTasksByTriggerComment cancels active tasks whose trigger is the given
// comment. Called from DeleteComment so an agent does not run with the
// now-deleted content already embedded in its prompt. Must be invoked BEFORE
// the comment row is deleted because the FK ON DELETE SET NULL would
// otherwise nullify trigger_comment_id and we'd lose the ability to find
// the affected tasks.
func (s *TaskService) CancelTasksByTriggerComment(ctx context.Context, commentID pgtype.UUID) error {
	cancelled, err := s.Queries.CancelAgentTasksByTriggerComment(ctx, commentID)
	if err != nil {
		return err
	}
	for _, t := range cancelled {
		s.captureTaskCancelled(ctx, t)
		s.ReconcileAgentStatus(ctx, t.AgentID)
		s.broadcastTaskEvent(ctx, protocol.EventTaskCancelled, t)
	}
	return nil
}

// BroadcastCancelledTasks reconciles each affected agent's status and emits
// task:cancelled for every row. Callers must invoke this AFTER committing the
// cancellation so subscribers don't observe a "cancelled" event for a row
// that the tx might still roll back.
func (s *TaskService) BroadcastCancelledTasks(ctx context.Context, cancelled []db.AgentTaskQueue) {
	for _, t := range cancelled {
		s.captureTaskCancelled(ctx, t)
		s.ReconcileAgentStatus(ctx, t.AgentID)
		s.broadcastTaskEvent(ctx, protocol.EventTaskCancelled, t)
	}
}

func (s *TaskService) CaptureCancelledTasks(ctx context.Context, cancelled []db.AgentTaskQueue) {
	for _, t := range cancelled {
		s.captureTaskCancelled(ctx, t)
	}
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
	task, err := s.Queries.CancelAgentTask(ctx, taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		existing, err := s.Queries.GetAgentTask(ctx, taskID)
		if err != nil {
			return nil, fmt.Errorf("cancel task: %w", err)
		}
		return &CancelTaskResult{Task: existing}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cancel task: %w", err)
	}

	slog.Info("task cancelled", "task_id", util.UUIDToString(task.ID), "issue_id", util.UUIDToString(task.IssueID))
	s.captureTaskCancelled(ctx, task)
	cancelledChatMessage := s.finalizeCancelledChatMessage(ctx, task)

	// Reconcile agent status
	s.ReconcileAgentStatus(ctx, task.AgentID)

	// Broadcast cancellation as a task:failed event so frontends clear the live card
	s.broadcastTaskEvent(ctx, protocol.EventTaskCancelled, task)

	return &CancelTaskResult{
		Task:                 task,
		CancelledChatMessage: cancelledChatMessage,
	}, nil
}

func (s *TaskService) finalizeCancelledChatMessage(ctx context.Context, task db.AgentTaskQueue) *CancelledChatMessageResult {
	if !task.ChatSessionID.Valid {
		return nil
	}
	var cancelled *CancelledChatMessageResult
	if err := s.runInTx(ctx, func(qtx *db.Queries) error {
		messages, err := qtx.ListTaskMessages(ctx, task.ID)
		if err != nil {
			return fmt.Errorf("list cancelled chat task messages: %w", err)
		}
		if len(messages) == 0 {
			// Detach attachments BEFORE deleting the user message — the
			// attachment FK is ON DELETE CASCADE, so deleting first would
			// destroy rows the restored draft needs to re-bind.
			detached, err := qtx.DetachAttachmentsFromUserChatMessageByTask(ctx, task.ID)
			if err != nil {
				return fmt.Errorf("detach cancelled chat message attachments: %w", err)
			}
			deleted, err := qtx.DeleteUserChatMessageByTask(ctx, task.ID)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("delete empty cancelled chat user message: %w", err)
			}
			cancelled = &CancelledChatMessageResult{
				ChatSessionID:  util.UUIDToString(deleted.ChatSessionID),
				MessageID:      util.UUIDToString(deleted.ID),
				Content:        deleted.Content,
				RestoreToInput: true,
				Attachments:    detached,
			}
			return nil
		}
		if _, err := qtx.CreateChatMessage(ctx, db.CreateChatMessageParams{
			ChatSessionID: task.ChatSessionID,
			Role:          "assistant",
			Content:       "Stopped.",
			TaskID:        task.ID,
			ElapsedMs:     computeChatElapsedMs(task),
		}); err != nil {
			return fmt.Errorf("create cancelled chat message: %w", err)
		}
		return nil
	}); err != nil {
		slog.Error("failed to finalize cancelled chat message",
			"task_id", util.UUIDToString(task.ID),
			"chat_session_id", util.UUIDToString(task.ChatSessionID),
			"error", err,
		)
		return nil
	}
	return cancelled
}

// ClaimTask atomically claims the next queued task for an agent,
// respecting max_concurrent_tasks.
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
		if _, err := s.updateIssueStatusAfterCompletedSOPRun(ctx, issue, "blocked"); err != nil {
			slog.Warn("block issue after completed SOP without MR failed",
				"issue_id", util.UUIDToString(issue.ID),
				"error", err,
			)
			return
		}
		s.recordMissingMRGateComment(ctx, issue)
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
	updated, err := s.Queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
		ID:          issue.ID,
		Status:      status,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return db.Issue{}, err
	}
	s.broadcastIssueUpdated(updated)
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
	updated, err := s.Queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
		ID:          issue.ID,
		Status:      status,
		WorkspaceID: issue.WorkspaceID,
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
	s.broadcastIssueUpdated(updated)
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
	comment, err := s.Queries.CreateComment(ctx, db.CreateCommentParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		AuthorType:  "system",
		AuthorID:    util.MustParseUUID("00000000-0000-0000-0000-000000000000"),
		Content:     content,
		Type:        "comment",
	})
	if err != nil {
		slog.Warn("create missing MR gate comment failed",
			"issue_id", util.UUIDToString(issue.ID),
			"error", err,
		)
		return
	}
	s.Bus.Publish(events.Event{
		Type:        protocol.EventCommentCreated,
		WorkspaceID: util.UUIDToString(issue.WorkspaceID),
		ActorType:   "system",
		ActorID:     "",
		Payload: map[string]any{
			"comment": map[string]any{
				"id":          util.UUIDToString(comment.ID),
				"issue_id":    util.UUIDToString(comment.IssueID),
				"author_type": comment.AuthorType,
				"author_id":   util.UUIDToString(comment.AuthorID),
				"content":     comment.Content,
				"type":        comment.Type,
				"created_at":  comment.CreatedAt.Time.Format("2006-01-02T15:04:05Z"),
			},
			"issue_title":  issue.Title,
			"issue_status": "blocked",
		},
	})
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
func (s *TaskService) CompleteTask(ctx context.Context, taskID pgtype.UUID, result []byte, sessionID, workDir string) (*db.AgentTaskQueue, error) {
	var task db.AgentTaskQueue
	if err := s.runInTx(ctx, func(qtx *db.Queries) error {
		t, err := qtx.CompleteAgentTask(ctx, db.CompleteAgentTaskParams{
			ID:        taskID,
			Result:    result,
			SessionID: pgtype.Text{String: sessionID, Valid: sessionID != ""},
			WorkDir:   pgtype.Text{String: workDir, Valid: workDir != ""},
		})
		if err != nil {
			return err
		}
		task = t

		if t.ChatSessionID.Valid {
			// Pin the chat_session's runtime_id alongside the session_id so the
			// next claim can apply the runtime-guard. Both fields move together:
			// when there's no session_id to record, leave runtime_id untouched
			// (NULL → COALESCE keeps the existing value).
			var sessionRuntimeID pgtype.UUID
			if sessionID != "" {
				sessionRuntimeID = t.RuntimeID
			}
			// COALESCE in SQL guarantees empty inputs don't wipe the
			// existing resume pointer; we still surface DB errors.
			if err := qtx.UpdateChatSessionSession(ctx, db.UpdateChatSessionSessionParams{
				ID:        t.ChatSessionID,
				SessionID: pgtype.Text{String: sessionID, Valid: sessionID != ""},
				WorkDir:   pgtype.Text{String: workDir, Valid: workDir != ""},
				RuntimeID: sessionRuntimeID,
			}); err != nil {
				return fmt.Errorf("update chat session resume pointer: %w", err)
			}
		}
		return nil
	}); err != nil {
		// When parallel agents race, a task may already be completed,
		// cancelled, or failed by the time this call runs. The UPDATE
		// … WHERE status = 'running' returns no rows in that case.
		// Treat it as an idempotent success — same pattern as CancelTask.
		if existing, lookupErr := s.Queries.GetAgentTask(ctx, taskID); lookupErr == nil {
			if errors.Is(err, pgx.ErrNoRows) {
				slog.Info("complete task: already finalized",
					"task_id", util.UUIDToString(taskID),
					"current_status", existing.Status,
					"agent_id", util.UUIDToString(existing.AgentID),
				)
				return &existing, nil
			}
			slog.Warn("complete task failed",
				"task_id", util.UUIDToString(taskID),
				"current_status", existing.Status,
				"issue_id", util.UUIDToString(existing.IssueID),
				"chat_session_id", util.UUIDToString(existing.ChatSessionID),
				"agent_id", util.UUIDToString(existing.AgentID),
				"error", err,
			)
		} else {
			slog.Warn("complete task failed: task not found",
				"task_id", util.UUIDToString(taskID),
				"lookup_error", lookupErr,
			)
		}
		return nil, fmt.Errorf("complete task: %w", err)
	}

	slog.Info("task completed", "task_id", util.UUIDToString(task.ID), "issue_id", util.UUIDToString(task.IssueID))
	s.captureTaskCompleted(ctx, task)
	if sc, ok := ParseIssueSourceSummaryContext(task); ok {
		s.completeIssueSourceSummaryTask(ctx, task, sc, result)
		s.ReconcileAgentStatus(ctx, task.AgentID)
		s.broadcastTaskEvent(ctx, protocol.EventTaskCompleted, task)
		return &task, nil
	}
	s.linkGongfengMRsFromTaskComments(ctx, task)
	s.syncSquadSOPTaskStepWithResult(ctx, task, "步骤完成", "已完成", result)
	s.enqueueSquadLeaderAfterWorkerStageCompletion(ctx, task)
	s.autoReviewIssueForTask(ctx, task)

	// Invariant: every completed issue task must have at least one agent
	// comment on the issue, so the user always sees something when a run
	// ends. If the agent posted a comment during execution (result, progress
	// ping, or CLI reply), HasAgentCommentedSince returns true and we skip.
	// Otherwise, synthesize one from the final output. For comment-triggered
	// tasks, TriggerCommentID threads the fallback under the original comment;
	// for assignment-triggered tasks it is NULL and the fallback is top-level.
	// Chat tasks have no IssueID and are handled separately below.
	if task.IssueID.Valid {
		suppressNoActionComment, err := HasSquadLeaderNoActionEvaluationForTask(ctx, s.Queries, task)
		if err != nil {
			slog.Warn("checking squad leader no_action evaluation failed",
				"task_id", util.UUIDToString(task.ID),
				"issue_id", util.UUIDToString(task.IssueID),
				"agent_id", util.UUIDToString(task.AgentID),
				"error", err,
			)
		}
		agentCommented, _ := s.Queries.HasAgentCommentedSince(ctx, db.HasAgentCommentedSinceParams{
			IssueID:  task.IssueID,
			AuthorID: task.AgentID,
			Since:    task.StartedAt,
		})
		if !suppressNoActionComment && !agentCommented {
			var payload protocol.TaskCompletedPayload
			if err := json.Unmarshal(result, &payload); err == nil {
				if payload.Output != "" {
					// Match the CLI's --content / --description behavior: agents that
					// emit literal `\n` 4-char sequences (Python/JSON-style) get them
					// decoded into real newlines before the comment hits the DB. See
					// util.UnescapeBackslashEscapes for the exact contract.
					body := util.UnescapeBackslashEscapes(payload.Output)
					if !containsAgentMention(body) {
						if dispatchBody := s.fallbackDispatchCommentFromMessages(ctx, task.ID); dispatchBody != "" {
							body = dispatchBody
						}
					}
					if task.TriggerCommentID.Valid && isTrivialDoneOutput(body) {
						slog.Warn("suppressing trivial comment-trigger fallback output",
							"task_id", util.UUIDToString(task.ID),
							"issue_id", util.UUIDToString(task.IssueID),
							"agent_id", util.UUIDToString(task.AgentID),
						)
					} else {
						s.createAgentComment(ctx, task.IssueID, task.AgentID, redact.Text(body), "comment", task.TriggerCommentID, task.ID)
					}
				}
			}
		}
	}

	// Quick-create tasks: locate the issue the agent just created and push
	// an inbox confirmation to the requester. The agent has no issue / chat
	// link, so the regular completion paths above don't apply. We find the
	// new issue by querying for the most recent issue this agent created in
	// the requester's workspace since the task started — more robust than
	// parsing the agent's stdout for an identifier.
	if qc, ok := s.parseQuickCreateContext(task); ok {
		s.notifyQuickCreateCompleted(ctx, task, qc)
	}

	// For chat tasks, save assistant reply and broadcast chat:done. The
	// resume pointer was already persisted inside the transaction above.
	if task.ChatSessionID.Valid {
		var assistantMsg *db.ChatMessage
		var payload protocol.TaskCompletedPayload
		if err := json.Unmarshal(result, &payload); err == nil && payload.Output != "" {
			// Same unescape as the issue-comment path above: literal `\n` from
			// agent stdout becomes a real newline so the chat panel renders
			// paragraph breaks instead of one wall of prose.
			body := util.UnescapeBackslashEscapes(payload.Output)
			row, err := s.Queries.CreateChatMessage(ctx, db.CreateChatMessageParams{
				ChatSessionID: task.ChatSessionID,
				Role:          "assistant",
				Content:       redact.Text(body),
				TaskID:        task.ID,
				ElapsedMs:     computeChatElapsedMs(task),
			})
			if err != nil {
				slog.Error("failed to save assistant chat message", "task_id", util.UUIDToString(task.ID), "error", err)
			} else {
				assistantMsg = &row
				// Event-driven unread: stamp unread_since on the first unread
				// assistant message. No-op if the session already has unread.
				// If the user is actively viewing the session, the frontend's
				// auto-mark-read effect will clear this within a tick.
				if err := s.Queries.SetUnreadSinceIfNull(ctx, task.ChatSessionID); err != nil {
					slog.Warn("failed to set unread_since", "chat_session_id", util.UUIDToString(task.ChatSessionID), "error", err)
				}
			}
		}
		s.broadcastChatDone(ctx, task, assistantMsg)
	}

	// Reconcile agent status
	s.ReconcileAgentStatus(ctx, task.AgentID)

	// Broadcast
	s.broadcastTaskEvent(ctx, protocol.EventTaskCompleted, task)

	return &task, nil
}

func (s *TaskService) completeIssueSourceSummaryTask(ctx context.Context, task db.AgentTaskQueue, sc IssueSourceSummaryContext, result []byte) {
	description, ok := issueSourceSummaryDescriptionFromResult(result)
	status := "completed"
	errorMessage := ""
	if !ok {
		status = "failed"
		errorMessage = "摘要任务输出无效，已使用来源内容生成兜底摘要。"
		description = s.fallbackIssueSourceSummaryDescription(ctx, task.IssueID, errorMessage)
	}
	s.applyIssueSourceSummary(ctx, task, sc, description, status, errorMessage)
}

func (s *TaskService) failIssueSourceSummaryTask(ctx context.Context, task db.AgentTaskQueue, sc IssueSourceSummaryContext, errMsg string) {
	errorMessage := strings.TrimSpace(errMsg)
	if errorMessage == "" {
		errorMessage = "摘要任务执行失败，已使用来源内容生成兜底摘要。"
	}
	description := s.fallbackIssueSourceSummaryDescription(ctx, task.IssueID, errorMessage)
	s.applyIssueSourceSummary(ctx, task, sc, description, "failed", errorMessage)
}

func (s *TaskService) applyIssueSourceSummary(ctx context.Context, task db.AgentTaskQueue, sc IssueSourceSummaryContext, description, status, errorMessage string) {
	if strings.TrimSpace(description) == "" {
		return
	}
	issue, err := s.Queries.GetIssue(ctx, task.IssueID)
	if err != nil {
		slog.Warn("source summary completion: issue not found",
			"task_id", util.UUIDToString(task.ID),
			"issue_id", util.UUIDToString(task.IssueID),
			"error", err,
		)
		return
	}
	updated, err := s.updateIssueDescriptionPreservingFields(ctx, issue, redact.Text(description))
	if err != nil {
		slog.Warn("source summary completion: update issue description failed",
			"task_id", util.UUIDToString(task.ID),
			"issue_id", util.UUIDToString(issue.ID),
			"error", err,
		)
		return
	}
	updated = s.setIssueMetadataString(ctx, updated, "source_summary_status", status)
	updated = s.setIssueMetadataString(ctx, updated, "source_summary_task_id", util.UUIDToString(task.ID))
	if strings.TrimSpace(errorMessage) != "" {
		updated = s.setIssueMetadataString(ctx, updated, "source_summary_error", errorMessage)
	}
	if sc.Provider != "" {
		updated = s.setIssueMetadataString(ctx, updated, "source_summary_provider", sc.Provider)
	}
	s.broadcastIssueUpdated(updated)
	s.enqueueIssueAfterSourceSummary(ctx, updated)
}

func issueSourceSummaryDescriptionFromResult(result []byte) (string, bool) {
	var payload protocol.TaskCompletedPayload
	if err := json.Unmarshal(result, &payload); err != nil {
		return "", false
	}
	body := strings.TrimSpace(util.UnescapeBackslashEscapes(payload.Output))
	body = unwrapMarkdownFence(body)
	if body == "" || isTrivialDoneOutput(body) {
		return "", false
	}
	runes := []rune(body)
	if len(runes) > 5000 {
		body = string(runes[:5000])
	}
	if !strings.Contains(body, "## 需求摘要") {
		body = "## 需求摘要\n" + body
	}
	return strings.TrimSpace(body), true
}

func unwrapMarkdownFence(body string) string {
	trimmed := strings.TrimSpace(body)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) < 3 || !strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
		return trimmed
	}
	return strings.TrimSpace(strings.Join(lines[1:len(lines)-1], "\n"))
}

func (s *TaskService) fallbackIssueSourceSummaryDescription(ctx context.Context, issueID pgtype.UUID, reason string) string {
	issue, err := s.Queries.GetIssue(ctx, issueID)
	if err != nil {
		return "## 需求摘要\n摘要生成失败，请查看 TAPD 来源卡片或重新触发摘要。\n"
	}
	title := strings.TrimSpace(issueMetadataString(issue.Metadata, "source_fetch_title"))
	body := strings.TrimSpace(firstNonEmpty(
		issueMetadataString(issue.Metadata, "source_fetch_summary"),
		issueMetadataString(issue.Metadata, "source_fetch_body_excerpt"),
	))
	if body == "" {
		body = strings.TrimSpace(issue.Description.String)
	}
	body = truncateForSummary(body, 900)
	var b strings.Builder
	b.WriteString("## 需求摘要\n")
	if title != "" {
		b.WriteString(title)
		if body != "" && body != title {
			b.WriteString("\n\n")
			b.WriteString(body)
		}
	} else if body != "" {
		b.WriteString(body)
	} else {
		b.WriteString("摘要生成失败，请查看 TAPD 来源卡片或重新触发摘要。")
	}
	if strings.TrimSpace(reason) != "" {
		b.WriteString("\n\n## 摘要状态\n")
		b.WriteString(reason)
	}
	b.WriteString("\n")
	return b.String()
}

func (s *TaskService) updateIssueDescriptionPreservingFields(ctx context.Context, issue db.Issue, description string) (db.Issue, error) {
	return s.Queries.UpdateIssue(ctx, db.UpdateIssueParams{
		ID:            issue.ID,
		Description:   pgtype.Text{String: description, Valid: true},
		AssigneeType:  issue.AssigneeType,
		AssigneeID:    issue.AssigneeID,
		StartDate:     issue.StartDate,
		DueDate:       issue.DueDate,
		ParentIssueID: issue.ParentIssueID,
		ProjectID:     issue.ProjectID,
	})
}

func (s *TaskService) setIssueMetadataString(ctx context.Context, issue db.Issue, key, value string) db.Issue {
	updated, err := s.Queries.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{
		ID:          issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Key:         key,
		Value:       mustJSONStringBytes(value),
	})
	if err != nil {
		slog.Warn("source summary completion: set metadata failed",
			"issue_id", util.UUIDToString(issue.ID),
			"key", key,
			"error", err,
		)
		return issue
	}
	return updated
}

func (s *TaskService) enqueueIssueAfterSourceSummary(ctx context.Context, issue db.Issue) {
	if issue.Status == "backlog" || !issue.AssigneeType.Valid || !issue.AssigneeID.Valid {
		return
	}
	switch issue.AssigneeType.String {
	case "agent":
		if _, err := s.EnqueueTaskForIssue(ctx, issue); err != nil {
			slog.Warn("source summary completion: enqueue assigned agent failed",
				"issue_id", util.UUIDToString(issue.ID),
				"agent_id", util.UUIDToString(issue.AssigneeID),
				"error", err,
			)
		}
	case "squad":
		squad, err := s.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
			ID:          issue.AssigneeID,
			WorkspaceID: issue.WorkspaceID,
		})
		if err != nil {
			slog.Warn("source summary completion: load squad failed",
				"issue_id", util.UUIDToString(issue.ID),
				"squad_id", util.UUIDToString(issue.AssigneeID),
				"error", err,
			)
			return
		}
		hasPending, err := s.Queries.HasPendingTaskForIssueAndAgent(ctx, db.HasPendingTaskForIssueAndAgentParams{
			IssueID: issue.ID,
			AgentID: squad.LeaderID,
		})
		if err != nil || hasPending {
			return
		}
		if _, err := s.EnqueueTaskForSquadLeader(ctx, issue, squad.LeaderID, pgtype.UUID{}); err != nil {
			slog.Warn("source summary completion: enqueue squad leader failed",
				"issue_id", util.UUIDToString(issue.ID),
				"squad_id", util.UUIDToString(squad.ID),
				"leader_id", util.UUIDToString(squad.LeaderID),
				"error", err,
			)
		}
	}
}

var (
	gongfengMRURLRe     = regexp.MustCompile(`https://git\.code\.tencent\.com/([A-Za-z0-9_.~%+/\-]+?)/(?:-/)?merge_requests/([0-9]+)`)
	gongfengMRBranchRe  = regexp.MustCompile(`(?im)(?:源分支|source\s+branch|source_branch)\s*(?:[：:]|\|)\s*` + "`?" + `([A-Za-z0-9._/\-]+)` + "`?")
	gongfengMRTitleLine = regexp.MustCompile(`(?m)(?:MR\s*(?:已创建|created)?|merge\s+request)\s*[：:]\s*(.+)$`)
)

type gongfengMRCommentRef struct {
	ProjectPath  string
	Number       int32
	HTMLURL      string
	SourceBranch string
	Title        string
}

func (s *TaskService) linkGongfengMRsFromTaskComments(ctx context.Context, task db.AgentTaskQueue) {
	if !task.IssueID.Valid {
		return
	}
	issue, err := s.Queries.GetIssue(ctx, task.IssueID)
	if err != nil {
		slog.Warn("task comment MR collection skipped: issue lookup failed",
			"task_id", util.UUIDToString(task.ID),
			"issue_id", util.UUIDToString(task.IssueID),
			"error", err,
		)
		return
	}
	comments, err := s.Queries.ListCommentsForIssue(ctx, db.ListCommentsForIssueParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Limit:       2000,
	})
	if err != nil {
		slog.Warn("task comment MR collection skipped: comments lookup failed",
			"task_id", util.UUIDToString(task.ID),
			"issue_id", util.UUIDToString(task.IssueID),
			"error", err,
		)
		return
	}
	refsByURL := map[string]gongfengMRCommentRef{}
	for _, comment := range comments {
		if !comment.SourceTaskID.Valid || comment.SourceTaskID != task.ID {
			continue
		}
		for _, ref := range parseGongfengMRRefsFromComment(comment.Content) {
			refsByURL[ref.HTMLURL] = ref
		}
	}
	for _, ref := range refsByURL {
		if err := s.linkGongfengMRCommentRef(ctx, issue, task, ref); err != nil {
			slog.Warn("task comment MR collection failed to link MR",
				"task_id", util.UUIDToString(task.ID),
				"issue_id", util.UUIDToString(issue.ID),
				"html_url", ref.HTMLURL,
				"error", err,
			)
		}
	}
}

func (s *TaskService) linkGongfengMRCommentRef(ctx context.Context, issue db.Issue, task db.AgentTaskQueue, ref gongfengMRCommentRef) error {
	repoOwner, repoName := splitGongfengProjectPath(ref.ProjectPath)
	if repoOwner == "" || repoName == "" || ref.Number <= 0 || ref.HTMLURL == "" {
		return nil
	}
	now := time.Now().UTC()
	title := strings.TrimSpace(ref.Title)
	if title == "" {
		title = fmt.Sprintf("MR !%d", ref.Number)
	}
	pr, err := s.Queries.UpsertGitHubPullRequest(ctx, db.UpsertGitHubPullRequestParams{
		WorkspaceID:         issue.WorkspaceID,
		InstallationID:      0,
		RepoOwner:           repoOwner,
		RepoName:            repoName,
		PrNumber:            ref.Number,
		Title:               title,
		State:               "open",
		HtmlUrl:             ref.HTMLURL,
		Branch:              pgtype.Text{String: ref.SourceBranch, Valid: ref.SourceBranch != ""},
		AuthorLogin:         pgtype.Text{},
		AuthorAvatarUrl:     pgtype.Text{},
		MergedAt:            pgtype.Timestamptz{},
		ClosedAt:            pgtype.Timestamptz{},
		PrCreatedAt:         pgtype.Timestamptz{Time: now, Valid: true},
		PrUpdatedAt:         pgtype.Timestamptz{Time: now, Valid: true},
		HeadSha:             "",
		MergeableState:      pgtype.Text{},
		ClearMergeableState: pgtype.Bool{},
		Additions:           0,
		Deletions:           0,
		ChangedFiles:        0,
	})
	if err != nil {
		return fmt.Errorf("upsert pull request: %w", err)
	}
	if err := s.Queries.LinkIssueToPullRequest(ctx, db.LinkIssueToPullRequestParams{
		IssueID:             issue.ID,
		PullRequestID:       pr.ID,
		CloseIntent:         false,
		LinkedByType:        pgtype.Text{String: "agent", Valid: true},
		LinkedByID:          task.AgentID,
		PreserveCloseIntent: true,
	}); err != nil {
		return fmt.Errorf("link issue pull request: %w", err)
	}
	slog.Info("linked Gongfeng MR reported by task comment",
		"task_id", util.UUIDToString(task.ID),
		"issue_id", util.UUIDToString(issue.ID),
		"mr_url", ref.HTMLURL,
		"mr_number", ref.Number,
	)
	return nil
}

func parseGongfengMRRefsFromComment(content string) []gongfengMRCommentRef {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	matches := gongfengMRURLRe.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}
	globalBranch := ""
	if len(matches) == 1 {
		if branchMatch := gongfengMRBranchRe.FindStringSubmatch(content); len(branchMatch) == 2 {
			globalBranch = strings.TrimSpace(branchMatch[1])
		}
	}
	lines := strings.Split(content, "\n")
	refs := make([]gongfengMRCommentRef, 0, len(matches))
	seen := map[string]struct{}{}
	for lineIdx, line := range lines {
		lineMatches := gongfengMRURLRe.FindAllStringSubmatch(line, -1)
		for _, match := range lineMatches {
			if len(match) != 3 {
				continue
			}
			number64, err := strconv.ParseInt(match[2], 10, 32)
			if err != nil || number64 <= 0 {
				continue
			}
			projectPath := strings.Trim(match[1], "/")
			htmlURL := canonicalGongfengMRURL(projectPath, int32(number64))
			if _, ok := seen[htmlURL]; ok {
				continue
			}
			seen[htmlURL] = struct{}{}
			number := int32(number64)
			branch := gongfengBranchNearMR(lines, lineIdx)
			if branch == "" {
				branch = globalBranch
			}
			refs = append(refs, gongfengMRCommentRef{
				ProjectPath:  projectPath,
				Number:       number,
				HTMLURL:      htmlURL,
				SourceBranch: branch,
				Title:        gongfengTitleNearMR(lines, lineIdx, number),
			})
		}
	}
	return refs
}

func canonicalGongfengMRURL(projectPath string, number int32) string {
	projectPath = strings.Trim(projectPath, "/")
	if projectPath == "" || number <= 0 {
		return ""
	}
	return fmt.Sprintf("https://git.code.tencent.com/%s/merge_requests/%d", projectPath, number)
}

func gongfengBranchNearMR(lines []string, lineIdx int) string {
	if branch := gongfengBranchFromLine(lines[lineIdx]); branch != "" {
		return branch
	}
	for i := lineIdx + 1; i < len(lines) && i <= lineIdx+8; i++ {
		if i != lineIdx+1 && strings.HasPrefix(strings.TrimSpace(lines[i]), "#") {
			break
		}
		if branch := gongfengBranchFromLine(lines[i]); branch != "" {
			return branch
		}
	}
	for i := lineIdx - 1; i >= 0 && i >= lineIdx-4; i-- {
		if branch := gongfengBranchFromLine(lines[i]); branch != "" {
			return branch
		}
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "#") {
			break
		}
	}
	return ""
}

func gongfengBranchFromLine(line string) string {
	if branchMatch := gongfengMRBranchRe.FindStringSubmatch(line); len(branchMatch) == 2 {
		return strings.Trim(strings.TrimSpace(branchMatch[1]), "`")
	}
	return ""
}

func gongfengTitleNearMR(lines []string, lineIdx int, number int32) string {
	marker := fmt.Sprintf("!%d", number)
	for i := lineIdx; i >= 0 && i >= lineIdx-8; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if strings.Contains(line, marker) && strings.HasPrefix(line, "#") {
			return strings.TrimSpace(strings.TrimLeft(line, "# "))
		}
	}
	return ""
}

func splitGongfengProjectPath(projectPath string) (string, string) {
	parts := strings.Split(strings.Trim(projectPath, "/"), "/")
	if len(parts) < 2 {
		return "", ""
	}
	return strings.Join(parts[:len(parts)-1], "/"), parts[len(parts)-1]
}

func (s *TaskService) enqueueSquadLeaderAfterWorkerStageCompletion(ctx context.Context, task db.AgentTaskQueue) {
	if !task.IssueID.Valid || task.IsLeaderTask {
		return
	}
	issue, err := s.Queries.GetIssue(ctx, task.IssueID)
	if err != nil || !issue.AssigneeType.Valid || issue.AssigneeType.String != "squad" || !issue.AssigneeID.Valid {
		return
	}
	if issue.Status == "done" || issue.Status == "cancelled" {
		return
	}
	run, ok := s.squadSOPRunForWorkerTask(ctx, task, issue)
	if !ok {
		return
	}
	agent, err := s.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID:          task.AgentID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return
	}
	if _, _, ok := matchSquadSOPStepForAgentRecord(parseSquadSOPProfileSteps(run.Profile), agent); !ok {
		return
	}
	squad, err := s.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
		ID:          issue.AssigneeID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return
	}
	leader, err := s.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID:          squad.LeaderID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil || !leader.RuntimeID.Valid || leader.ArchivedAt.Valid {
		return
	}
	hasPending, err := s.Queries.HasPendingTaskForIssueAndAgent(ctx, db.HasPendingTaskForIssueAndAgentParams{
		IssueID: issue.ID,
		AgentID: squad.LeaderID,
	})
	if err != nil || hasPending {
		return
	}
	nextTask, err := s.Queries.CreateAgentTask(ctx, db.CreateAgentTaskParams{
		AgentID:        squad.LeaderID,
		RuntimeID:      leader.RuntimeID,
		IssueID:        issue.ID,
		Priority:       priorityToInt(issue.Priority),
		TriggerSummary: pgtype.Text{String: "SOP 阶段任务已完成，继续协调下一阶段。", Valid: true},
		IsLeaderTask:   pgtype.Bool{Bool: true, Valid: true},
	})
	if err != nil {
		slog.Warn("enqueue squad leader after worker stage completion failed",
			"issue_id", util.UUIDToString(issue.ID),
			"worker_task_id", util.UUIDToString(task.ID),
			"leader_id", util.UUIDToString(squad.LeaderID),
			"error", err,
		)
		return
	}
	slog.Info("squad leader enqueued after worker stage completion",
		"task_id", util.UUIDToString(nextTask.ID),
		"issue_id", util.UUIDToString(issue.ID),
		"worker_task_id", util.UUIDToString(task.ID),
		"leader_id", util.UUIDToString(squad.LeaderID),
	)
	s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, nextTask)
	s.NotifyTaskEnqueued(ctx, nextTask)
}

func (s *TaskService) squadSOPRunForWorkerTask(ctx context.Context, task db.AgentTaskQueue, issue db.Issue) (db.SquadSopRun, bool) {
	run, err := s.Queries.GetOpenSquadSOPRunByIssue(ctx, task.IssueID)
	if err == nil {
		return run, true
	}
	runs, err := s.Queries.ListIssueSquadSOPRuns(ctx, db.ListIssueSquadSOPRunsParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil || len(runs) == 0 {
		return db.SquadSopRun{}, false
	}
	return runs[0], true
}

// FailTask marks a task as failed.
// For assignment-triggered issue tasks without an automatic retry, the
// platform blocks an in-progress issue instead of moving it back to todo.
//
// sessionID/workDir are optional: when the agent established a real session
// before failing (e.g. crashed mid-conversation, was cancelled, or hit a
// tool error), the daemon should pass them so we can preserve the resume
// pointer on both the task row and the chat_session — otherwise the next
// chat turn would silently start a brand-new session and lose memory.
//
// failureReason is a coarse classifier consumed by the auto-retry path.
// Pass "" when unknown — the server runs the raw error text through
// taskfailure.Classify so the persisted failure_reason still lands in
// the canonical refined taxonomy rather than the legacy "agent_error"
// coarse bucket. Daemon callers that already produced a refined reason
// (via classifyPoisonedError, the timeout / runtime classifier, etc.)
// will have their value preserved untouched.
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
		return nil
	}); err != nil {
		if existing, lookupErr := s.Queries.GetAgentTask(ctx, taskID); lookupErr == nil {
			if errors.Is(err, pgx.ErrNoRows) {
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
	if sc, ok := ParseIssueSourceSummaryContext(task); ok {
		s.failIssueSourceSummaryTask(ctx, task, sc, errMsg)
		s.ReconcileAgentStatus(ctx, task.AgentID)
		s.broadcastTaskEvent(ctx, protocol.EventTaskFailed, task)
		return &task, nil
	}

	// Auto-retry eligible failures (orphan, timeout, runtime_offline,
	// runtime_recovery). The helper itself enforces attempt < max_attempts
	// and only triggers for issue/chat tasks.
	retried, _ := s.MaybeRetryFailedTask(ctx, task)
	if retried != nil {
		s.reassignPromptEvaluationRunToRetry(ctx, task, *retried)
	} else {
		s.syncPromptEvaluationRunForTask(ctx, task, "task_failed")
	}

	deliveryCommentPosted := retried == nil && s.squadSOPTaskHasDeliveryComment(ctx, task)

	var failureComment string
	if errMsg != "" && task.IssueID.Valid && retried == nil && !deliveryCommentPosted {
		if body, ok := s.squadSOPFailureComment(ctx, task, errMsg, failureReason); ok {
			failureComment = body
		}
	}
	if deliveryCommentPosted {
		s.linkGongfengMRsFromTaskComments(ctx, task)
		s.syncSquadSOPTaskStepWithResult(ctx, task, "步骤完成", "已完成", nil)
		s.enqueueSquadLeaderAfterWorkerStageCompletion(ctx, task)
		s.autoReviewIssueForTask(ctx, task)
	} else if s.shouldSyncSquadSOPTaskFailure(ctx, task, retried) {
		s.syncSquadSOPTaskStep(ctx, task, "步骤失败", "已失败")
	}
	if retried == nil && !deliveryCommentPosted {
		s.autoBlockIssueForTaskFailure(ctx, task)
	}

	// Skip the per-failure system comment when we'll immediately retry —
	// the new task will surface its own status to the user, and we don't
	// want to spam the issue with "task timed out" messages on every
	// daemon hiccup.
	if errMsg != "" && task.IssueID.Valid && retried == nil && !deliveryCommentPosted {
		body := redact.Text(errMsg)
		if failureComment != "" {
			body = failureComment
		}
		s.createAgentComment(ctx, task.IssueID, task.AgentID, body, "system", task.TriggerCommentID, task.ID)
	}

	// Mirror the issue fallback for chat tasks: write an assistant
	// chat_message tagged with the daemon-reported failure_reason so the
	// conversation history shows what happened. Skip when auto-retry is
	// pending (the new attempt will write its own outcome) — same guard as
	// the issue path above.
	if task.ChatSessionID.Valid && retried == nil {
		if _, err := s.Queries.CreateChatMessage(ctx, db.CreateChatMessageParams{
			ChatSessionID: task.ChatSessionID,
			Role:          "assistant",
			Content:       redact.Text(errMsg),
			TaskID:        pgtype.UUID{Bytes: task.ID.Bytes, Valid: true},
			FailureReason: pgtype.Text{String: failureReason, Valid: failureReason != ""},
			ElapsedMs:     computeChatElapsedMs(task),
		}); err != nil {
			slog.Error("failed to save failure chat message",
				"task_id", util.UUIDToString(task.ID),
				"chat_session_id", util.UUIDToString(task.ChatSessionID),
				"error", err)
		} else if err := s.Queries.SetUnreadSinceIfNull(ctx, task.ChatSessionID); err != nil {
			slog.Warn("failed to set unread_since on failure",
				"chat_session_id", util.UUIDToString(task.ChatSessionID),
				"error", err)
		}
	}

	// Quick-create tasks: push a failure inbox notification to the
	// requester so they can either retry or fall back to the advanced form
	// without losing their original prompt. Skipped when an auto-retry is
	// pending — the new attempt will write its own outcome.
	if retried == nil {
		if qc, ok := s.parseQuickCreateContext(task); ok {
			s.notifyQuickCreateFailed(ctx, task, qc, errMsg)
		}
	}
	// Reconcile agent status
	s.ReconcileAgentStatus(ctx, task.AgentID)

	// Broadcast
	s.broadcastTaskEvent(ctx, protocol.EventTaskFailed, task)

	return &task, nil
}

func (s *TaskService) shouldSyncSquadSOPTaskFailure(ctx context.Context, task db.AgentTaskQueue, retried *db.AgentTaskQueue) bool {
	if retried != nil {
		return false
	}
	if !task.IssueID.Valid {
		return true
	}
	hasActive, err := s.Queries.HasActiveTaskForIssue(ctx, task.IssueID)
	if err != nil {
		slog.Warn("failed to check active issue tasks before failing squad SOP run",
			"issue_id", util.UUIDToString(task.IssueID),
			"task_id", util.UUIDToString(task.ID),
			"error", err,
		)
		return true
	}
	if hasActive {
		slog.Info("squad SOP task failure did not close run because issue still has active tasks",
			"issue_id", util.UUIDToString(task.IssueID),
			"task_id", util.UUIDToString(task.ID),
		)
		return false
	}
	return true
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

// MaybeRetryFailedTask spawns a fresh queued attempt for a recently-failed
// task when the failure was infrastructure-shaped (daemon crash, runtime
// went offline, dispatch/run timeout) and the task hasn't exhausted its
// max_attempts budget. The child task inherits agent/runtime/issue/chat
// links and, for resume-safe failures, the parent's session_id/work_dir so
// the agent can resume the conversation when the backend supports it. Returns
// the new task, or nil when no retry was created.
//
// Autopilot tasks are NOT auto-retried here; the autopilot scheduler owns
// its own re-run cadence and we don't want to double-fire it.
func (s *TaskService) MaybeRetryFailedTask(ctx context.Context, parent db.AgentTaskQueue) (*db.AgentTaskQueue, error) {
	if parent.Status != "failed" {
		return nil, nil
	}
	reason := ""
	if parent.FailureReason.Valid {
		reason = parent.FailureReason.String
	}
	if !retryableReasons[reason] {
		return nil, nil
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
		return nil, nil
	}
	if parent.AutopilotRunID.Valid {
		// Autopilot has its own retry semantics; do not double-trigger.
		return nil, nil
	}
	if !parent.IssueID.Valid && !parent.ChatSessionID.Valid {
		return nil, nil
	}

	child, err := s.Queries.CreateRetryTask(ctx, parent.ID)
	if err != nil {
		slog.Warn("task auto-retry failed",
			"parent_task_id", util.UUIDToString(parent.ID),
			"reason", reason,
			"error", err,
		)
		return nil, err
	}
	slog.Info("task auto-retry enqueued",
		"parent_task_id", util.UUIDToString(parent.ID),
		"child_task_id", util.UUIDToString(child.ID),
		"reason", reason,
		"attempt", child.Attempt,
		"max_attempts", child.MaxAttempts,
	)
	// Retry creates a fresh queued row, same status transition (∅ → queued)
	// as EnqueueTaskFor*. Broadcast queued first, then notify the daemon —
	// see EnqueueTaskForIssue for ordering rationale.
	s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, child)
	s.NotifyTaskEnqueued(ctx, child)
	return &child, nil
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
	cancelled, err := s.Queries.CancelAgentTasksByIssueAndAgent(ctx, db.CancelAgentTasksByIssueAndAgentParams{
		IssueID: issueID,
		AgentID: agentID,
	})
	if err != nil {
		slog.Warn("rerun: cancel prior tasks failed",
			"issue_id", util.UUIDToString(issueID),
			"agent_id", util.UUIDToString(agentID),
			"error", err,
		)
	}
	for _, t := range cancelled {
		s.captureTaskCancelled(ctx, t)
		s.ReconcileAgentStatus(ctx, t.AgentID)
		s.broadcastTaskEvent(ctx, protocol.EventTaskCancelled, t)
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

// HandleFailedTasks runs the post-failure side effects for a batch of
// freshly-failed tasks: optional auto-retry, task:failed event broadcast,
// agent status reconciliation, and (when an issue has no remaining active
// task and isn't being retried) blocking the issue so the user sees that the
// current attempt needs attention.
//
// All callers that surface a task as failed — sweepers, FailTask,
// recover-orphans — funnel through here so the same UI-consistency
// guarantees apply on every code path.
func (s *TaskService) HandleFailedTasks(ctx context.Context, tasks []db.AgentTaskQueue) int {
	if len(tasks) == 0 {
		return 0
	}

	affectedAgents := make(map[string]pgtype.UUID)
	processedIssues := make(map[string]bool)
	retriedIssues := make(map[string]bool)
	retried := 0

	for _, t := range tasks {
		var retryChild *db.AgentTaskQueue
		// Auto-retry first so the issue stays in_progress rather than
		// flapping todo → in_progress within a tick.
		if child, _ := s.MaybeRetryFailedTask(ctx, t); child != nil {
			retryChild = child
			retried++
			if t.IssueID.Valid {
				retriedIssues[util.UUIDToString(t.IssueID)] = true
			}
		}

		failureReason := "agent_error"
		if t.FailureReason.Valid && t.FailureReason.String != "" {
			failureReason = t.FailureReason.String
		}
		s.captureTaskFailed(ctx, t)
		if retryChild != nil {
			s.reassignPromptEvaluationRunToRetry(ctx, t, *retryChild)
		} else {
			s.syncPromptEvaluationRunForTask(ctx, t, "task_failed_batch")
		}

		workspaceID := ""
		if t.IssueID.Valid {
			if issue, err := s.Queries.GetIssue(ctx, t.IssueID); err == nil {
				workspaceID = util.UUIDToString(issue.WorkspaceID)
				// Block stuck in_progress issues only when no other active
				// task exists for the issue and no retry was just enqueued.
				issueKey := util.UUIDToString(t.IssueID)
				if issue.Status == "in_progress" && !processedIssues[issueKey] && !retriedIssues[issueKey] {
					processedIssues[issueKey] = true
					hasActive, checkErr := s.Queries.HasActiveTaskForIssue(ctx, t.IssueID)
					if checkErr != nil {
						slog.Warn("handle failed tasks: active check failed",
							"issue_id", issueKey,
							"error", checkErr,
						)
					} else if !hasActive && shouldAutoBlockIssueForTaskFailure(t) {
						s.autoTransitionIssueStatus(ctx, t, "in_progress", "blocked", "failed_task_batch")
					}
				}
			}
		}
		if workspaceID == "" {
			workspaceID = s.ResolveTaskWorkspaceID(ctx, t)
		}

		if workspaceID != "" {
			s.Bus.Publish(events.Event{
				Type:        protocol.EventTaskFailed,
				WorkspaceID: workspaceID,
				ActorType:   "system",
				Payload: map[string]any{
					"task_id":        util.UUIDToString(t.ID),
					"agent_id":       util.UUIDToString(t.AgentID),
					"issue_id":       util.UUIDToString(t.IssueID),
					"status":         "failed",
					"failure_reason": failureReason,
				},
			})
		}

		affectedAgents[util.UUIDToString(t.AgentID)] = t.AgentID
	}

	for _, agentID := range affectedAgents {
		s.ReconcileAgentStatus(ctx, agentID)
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
	defer tx.Rollback(ctx)
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

// ReconcileAgentStatus refreshes agent status from the current active task set.
func (s *TaskService) ReconcileAgentStatus(ctx context.Context, agentID pgtype.UUID) {
	agent, err := s.Queries.RefreshAgentStatusFromTasks(ctx, agentID)
	if err != nil {
		return
	}
	slog.Debug("agent status reconciled", "agent_id", util.UUIDToString(agentID), "status", agent.Status)
	s.publishAgentStatus(agent)
}

func (s *TaskService) updateAgentStatus(ctx context.Context, agentID pgtype.UUID, status string) {
	agent, err := s.Queries.UpdateAgentStatus(ctx, db.UpdateAgentStatusParams{
		ID:     agentID,
		Status: status,
	})
	if err != nil {
		return
	}
	s.publishAgentStatus(agent)
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

// computeChatElapsedMs returns the wall-clock duration from task creation
// (user hit send) to terminal state (completed/failed). Stored on the
// assistant chat_message so the UI can render "Replied in 38s" /
// "Failed after 12s". Uses created_at — not started_at — because users
// experience total wait time, including queue + dispatch, not just the
// daemon's actual run time.
func computeChatElapsedMs(task db.AgentTaskQueue) pgtype.Int8 {
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
		json.Unmarshal(task.Context, &payload)
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
	workspaceID := s.ResolveTaskWorkspaceID(ctx, task)
	if workspaceID == "" {
		return
	}
	payload := map[string]any{
		"task_id":  util.UUIDToString(task.ID),
		"agent_id": util.UUIDToString(task.AgentID),
		"issue_id": util.UUIDToString(task.IssueID),
		"status":   task.Status,
	}
	if task.ChatSessionID.Valid {
		payload["chat_session_id"] = util.UUIDToString(task.ChatSessionID)
	}
	s.Bus.Publish(events.Event{
		Type:        eventType,
		WorkspaceID: workspaceID,
		ActorType:   "system",
		ActorID:     "",
		Payload:     payload,
	})
}

// ResolveTaskWorkspaceID determines the workspace ID for a task.
// For issue tasks, it comes from the issue. For chat tasks, from the chat session.
// For autopilot tasks, from the autopilot via its run.
// Returns "" when none of the links resolve — callers treat that as "not found".
func (s *TaskService) ResolveTaskWorkspaceID(ctx context.Context, task db.AgentTaskQueue) string {
	if task.IssueID.Valid {
		if issue, err := s.Queries.GetIssue(ctx, task.IssueID); err == nil {
			return util.UUIDToString(issue.WorkspaceID)
		}
	}
	if task.ChatSessionID.Valid {
		if cs, err := s.Queries.GetChatSession(ctx, task.ChatSessionID); err == nil {
			return util.UUIDToString(cs.WorkspaceID)
		}
	}
	if task.AutopilotRunID.Valid {
		if run, err := s.Queries.GetAutopilotRun(ctx, task.AutopilotRunID); err == nil {
			if ap, err := s.Queries.GetAutopilot(ctx, run.AutopilotID); err == nil {
				return util.UUIDToString(ap.WorkspaceID)
			}
		}
	}
	// Quick-create tasks have no issue / chat / autopilot link — workspace
	// lives in the context JSONB. Returning "" here is what blocked
	// requireDaemonTaskAccess (404 on /start, /progress, /complete, /fail
	// for the daemon) and silently dropped task:dispatch / task:completed
	// broadcasts, which is why quick-create tasks appeared stuck queued.
	if qc, ok := s.parseQuickCreateContext(task); ok {
		return qc.WorkspaceID
	}
	return ""
}

func (s *TaskService) broadcastChatDone(ctx context.Context, task db.AgentTaskQueue, msg *db.ChatMessage) {
	workspaceID := s.ResolveTaskWorkspaceID(ctx, task)
	if workspaceID == "" {
		return
	}
	payload := protocol.ChatDonePayload{
		ChatSessionID: util.UUIDToString(task.ChatSessionID),
		TaskID:        util.UUIDToString(task.ID),
	}
	if msg != nil {
		payload.MessageID = util.UUIDToString(msg.ID)
		payload.Content = msg.Content
		if msg.CreatedAt.Valid {
			payload.CreatedAt = msg.CreatedAt.Time.UTC().Format(time.RFC3339Nano)
		}
		if msg.ElapsedMs.Valid {
			payload.ElapsedMs = msg.ElapsedMs.Int64
		}
	}
	s.Bus.Publish(events.Event{
		Type:          protocol.EventChatDone,
		WorkspaceID:   workspaceID,
		ActorType:     "system",
		ActorID:       "",
		ChatSessionID: util.UUIDToString(task.ChatSessionID),
		Payload:       payload,
	})
}

func (s *TaskService) broadcastIssueUpdated(issue db.Issue) {
	prefix := s.getIssuePrefix(issue.WorkspaceID)
	s.Bus.Publish(events.Event{
		Type:        protocol.EventIssueUpdated,
		WorkspaceID: util.UUIDToString(issue.WorkspaceID),
		ActorType:   "system",
		ActorID:     "",
		Payload:     map[string]any{"issue": issueToMap(issue, prefix)},
	})
}

func (s *TaskService) getIssuePrefix(workspaceID pgtype.UUID) string {
	ws, err := s.Queries.GetWorkspace(context.Background(), workspaceID)
	if err != nil {
		return ""
	}
	return ws.IssuePrefix
}

func (s *TaskService) createAgentComment(ctx context.Context, issueID, agentID pgtype.UUID, content, commentType string, parentID, sourceTaskID pgtype.UUID) {
	if content == "" {
		return
	}
	// Look up issue to get workspace ID for mention expansion and broadcasting.
	issue, err := s.Queries.GetIssue(ctx, issueID)
	if err != nil {
		return
	}
	// Resolve the thread root for thread-level side effects without overwriting
	// parentID. The stored parent_id must remain the exact comment being replied
	// to; recursive thread reads recover the root when needed.
	var parentComment *db.Comment
	var rootComment *db.Comment
	if parentID.Valid {
		if parent, err := s.Queries.GetComment(ctx, parentID); err == nil && parent.IssueID == issueID {
			parentComment = &parent
		}
		if root, err := s.Queries.GetThreadRoot(ctx, db.GetThreadRootParams{
			CommentID:   parentID,
			WorkspaceID: issue.WorkspaceID,
		}); err == nil {
			rootComment = &root
		}
	}
	comment, err := s.Queries.CreateComment(ctx, db.CreateCommentParams{
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
		return
	}
	s.Bus.Publish(events.Event{
		Type:        protocol.EventCommentCreated,
		WorkspaceID: util.UUIDToString(issue.WorkspaceID),
		ActorType:   "agent",
		ActorID:     util.UUIDToString(agentID),
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
	})
	s.AutoUnresolveThreadOnReply(ctx, rootComment, util.UUIDToString(issue.WorkspaceID), "agent", util.UUIDToString(agentID))
	if commentType == "comment" && s.AgentCommentCreated != nil {
		s.AgentCommentCreated(ctx, issue, comment, parentComment)
	}
}

// AutoUnresolveThreadOnReply clears resolved_at on the thread root when a
// reply lands in a resolved thread, and broadcasts comment:unresolved. Shared
// between the user-facing Handler.CreateComment path and the agent-facing
// TaskService.createAgentComment path so the resolved-then-replied state can
// never desync (one of the bugs Emacs flagged on PR #2300). Errors are logged
// — the reply itself already committed, the desync is recoverable on next read.
func (s *TaskService) AutoUnresolveThreadOnReply(ctx context.Context, parent *db.Comment, workspaceID, actorType, actorID string) {
	if parent == nil || !parent.ResolvedAt.Valid {
		return
	}
	updated, err := s.Queries.UnresolveComment(ctx, parent.ID)
	if err != nil {
		slog.Warn("auto-unresolve on reply failed", "error", err, "comment_id", util.UUIDToString(parent.ID))
		return
	}
	s.Bus.Publish(events.Event{
		Type:        protocol.EventCommentUnresolved,
		WorkspaceID: workspaceID,
		ActorType:   actorType,
		ActorID:     actorID,
		Payload: map[string]any{
			"comment": map[string]any{
				"id":               util.UUIDToString(updated.ID),
				"issue_id":         util.UUIDToString(updated.IssueID),
				"author_type":      updated.AuthorType,
				"author_id":        util.UUIDToString(updated.AuthorID),
				"content":          updated.Content,
				"type":             updated.Type,
				"parent_id":        util.UUIDToPtr(updated.ParentID),
				"created_at":       util.TimestampToString(updated.CreatedAt),
				"updated_at":       util.TimestampToString(updated.UpdatedAt),
				"resolved_at":      util.TimestampToPtr(updated.ResolvedAt),
				"resolved_by_type": util.TextToPtr(updated.ResolvedByType),
				"resolved_by_id":   util.UUIDToPtr(updated.ResolvedByID),
			},
		},
	})
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

// parseQuickCreateContext returns the quick-create payload if the task's
// context JSONB contains type == "quick_create"; otherwise the bool is
// false so callers can short-circuit. Tasks linked to an issue / chat /
// autopilot are never quick-create even if they happen to carry a
// context blob, so those are filtered up front.
func (s *TaskService) parseQuickCreateContext(task db.AgentTaskQueue) (QuickCreateContext, bool) {
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

// notifyQuickCreateCompleted writes a success inbox notification to the
// requester pointing at the issue the agent just created. The issue is
// stamped with origin_type=quick_create + origin_id=<task_id> by the
// daemon-injected MULTICA_QUICK_CREATE_TASK_ID env var, so this lookup is
// deterministic — robust against the same agent creating other issues in
// parallel (e.g. assignment task running while max_concurrent_tasks > 1
// permits another quick-create alongside it).
func (s *TaskService) notifyQuickCreateCompleted(ctx context.Context, task db.AgentTaskQueue, qc QuickCreateContext) {
	requesterID, err := util.ParseUUID(qc.RequesterID)
	if err != nil {
		slog.Warn("quick-create completion: invalid requester id", "task_id", util.UUIDToString(task.ID), "error", err)
		return
	}
	workspaceID, err := util.ParseUUID(qc.WorkspaceID)
	if err != nil {
		slog.Warn("quick-create completion: invalid workspace id", "task_id", util.UUIDToString(task.ID), "error", err)
		return
	}
	issue, err := s.Queries.GetIssueByOrigin(ctx, db.GetIssueByOriginParams{
		WorkspaceID: workspaceID,
		OriginType:  pgtype.Text{String: "quick_create", Valid: true},
		OriginID:    task.ID,
	})
	if err != nil {
		// No issue created — agent ran to completion but the CLI call must
		// have failed. Surface as a failure inbox so the user sees something.
		slog.Warn("quick-create completion: no issue found, writing failure inbox",
			"task_id", util.UUIDToString(task.ID),
			"agent_id", util.UUIDToString(task.AgentID),
			"workspace_id", qc.WorkspaceID,
		)
		s.notifyQuickCreateFailed(ctx, task, qc, "agent finished without creating an issue")
		return
	}

	// Link the new issue back to this task so subsequent reads of the task
	// (Activity tab, Recent work, etc.) render it as a normal issue task
	// (kind = "direct") instead of staying on the "Creating issue" active-
	// wording label. Best-effort: a write failure here doesn't block the
	// inbox notification, which is the more important signal to the user.
	if err := s.Queries.LinkTaskToIssue(ctx, db.LinkTaskToIssueParams{
		ID:      task.ID,
		IssueID: issue.ID,
	}); err != nil {
		slog.Warn("quick-create completion: link task→issue failed",
			"task_id", util.UUIDToString(task.ID),
			"issue_id", util.UUIDToString(issue.ID),
			"error", err,
		)
	}

	// Subscribe the requester so they receive notifications for follow-up
	// comments and updates. The DB row's creator_type/creator_id is the
	// agent (it ran the CLI), but the human who triggered the quick-create
	// is the semantic creator from a UX perspective — without this they
	// only see the one-shot completion inbox and miss everything after.
	// Best-effort: log on failure but don't block the inbox notification.
	if err := s.Queries.AddIssueSubscriber(ctx, db.AddIssueSubscriberParams{
		IssueID:  issue.ID,
		UserType: "member",
		UserID:   requesterID,
		Reason:   "creator",
	}); err != nil {
		slog.Warn("quick-create completion: subscribe requester failed",
			"task_id", util.UUIDToString(task.ID),
			"issue_id", util.UUIDToString(issue.ID),
			"requester_id", qc.RequesterID,
			"error", err,
		)
	} else {
		s.Bus.Publish(events.Event{
			Type:        protocol.EventSubscriberAdded,
			WorkspaceID: qc.WorkspaceID,
			ActorType:   "agent",
			ActorID:     util.UUIDToString(task.AgentID),
			Payload: map[string]any{
				"issue_id":  util.UUIDToString(issue.ID),
				"user_type": "member",
				"user_id":   qc.RequesterID,
				"reason":    "creator",
			},
		})
	}
	prefix := s.getIssuePrefix(workspaceID)
	identifier := fmt.Sprintf("%s-%d", prefix, issue.Number)
	details, _ := json.Marshal(map[string]any{
		"task_id":         util.UUIDToString(task.ID),
		"agent_id":        util.UUIDToString(task.AgentID),
		"issue_id":        util.UUIDToString(issue.ID),
		"identifier":      identifier,
		"original_prompt": qc.Prompt,
	})
	item, err := s.Queries.CreateInboxItem(ctx, db.CreateInboxItemParams{
		WorkspaceID:   workspaceID,
		RecipientType: "member",
		RecipientID:   requesterID,
		Type:          "quick_create_done",
		Severity:      "info",
		IssueID:       issue.ID,
		Title:         issue.Title,
		Body:          pgtype.Text{},
		ActorType:     pgtype.Text{String: "agent", Valid: true},
		ActorID:       task.AgentID,
		Details:       details,
	})
	if err != nil {
		slog.Error("quick-create completion: inbox write failed", "task_id", util.UUIDToString(task.ID), "error", err)
		return
	}
	s.publishQuickCreateInbox(item, qc.WorkspaceID, util.UUIDToString(task.AgentID), issue.Status)
}

// notifyQuickCreateFailed writes a failure inbox notification carrying the
// original prompt + agent ID so the frontend can render an "Edit as
// advanced form" entry that pre-fills the legacy create-issue modal
// without asking the user to retype.
func (s *TaskService) notifyQuickCreateFailed(ctx context.Context, task db.AgentTaskQueue, qc QuickCreateContext, errMsg string) {
	requesterID, err := util.ParseUUID(qc.RequesterID)
	if err != nil {
		return
	}
	workspaceID, err := util.ParseUUID(qc.WorkspaceID)
	if err != nil {
		return
	}
	if errMsg == "" {
		errMsg = "Quick create did not finish successfully"
	}
	details, _ := json.Marshal(map[string]any{
		"task_id":         util.UUIDToString(task.ID),
		"agent_id":        util.UUIDToString(task.AgentID),
		"original_prompt": qc.Prompt,
		"error":           redact.Text(errMsg),
	})
	item, err := s.Queries.CreateInboxItem(ctx, db.CreateInboxItemParams{
		WorkspaceID:   workspaceID,
		RecipientType: "member",
		RecipientID:   requesterID,
		Type:          "quick_create_failed",
		Severity:      "action_required",
		IssueID:       pgtype.UUID{},
		Title:         "Quick create failed",
		Body:          pgtype.Text{String: redact.Text(errMsg), Valid: true},
		ActorType:     pgtype.Text{String: "agent", Valid: true},
		ActorID:       task.AgentID,
		Details:       details,
	})
	if err != nil {
		slog.Error("quick-create failure: inbox write failed", "task_id", util.UUIDToString(task.ID), "error", err)
		return
	}
	s.publishQuickCreateInbox(item, qc.WorkspaceID, util.UUIDToString(task.AgentID), "")
}

// publishQuickCreateInbox emits the WS event so the requester's inbox list
// updates immediately. Mirrors the payload shape used by the other inbox
// listeners (notification_listeners.go).
func (s *TaskService) publishQuickCreateInbox(item db.InboxItem, workspaceID, agentID, issueStatus string) {
	resp := map[string]any{
		"id":             util.UUIDToString(item.ID),
		"workspace_id":   util.UUIDToString(item.WorkspaceID),
		"recipient_type": item.RecipientType,
		"recipient_id":   util.UUIDToString(item.RecipientID),
		"type":           item.Type,
		"severity":       item.Severity,
		"issue_id":       util.UUIDToPtr(item.IssueID),
		"title":          item.Title,
		"body":           util.TextToPtr(item.Body),
		"read":           item.Read,
		"archived":       item.Archived,
		"created_at":     util.TimestampToString(item.CreatedAt),
		"actor_type":     util.TextToPtr(item.ActorType),
		"actor_id":       util.UUIDToPtr(item.ActorID),
		"details":        json.RawMessage(item.Details),
		"issue_status":   issueStatus,
	}
	s.Bus.Publish(events.Event{
		Type:        protocol.EventInboxNew,
		WorkspaceID: workspaceID,
		ActorType:   "agent",
		ActorID:     agentID,
		Payload:     map[string]any{"item": resp},
	})
}

// agentToMap builds a simple map for broadcasting agent status updates.
func agentToMap(a db.Agent) map[string]any {
	var rc any
	if a.RuntimeConfig != nil {
		json.Unmarshal(a.RuntimeConfig, &rc)
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
