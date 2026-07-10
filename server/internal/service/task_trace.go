package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

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
		if qc, ok := ParseQuickCreateContext(task); ok {
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
}

// CaptureCancelledTaskTracesInTx records cancel traces inside the caller's
// transaction. Used by DeleteChatSession so task_trace_event.chat_session_id
// still points at a live session row when the insert runs.
func (s *TaskService) CaptureCancelledTaskTracesInTx(ctx context.Context, q *db.Queries, cancelled []db.AgentTaskQueue) {
	for _, task := range cancelled {
		s.recordTaskCancelledTrace(ctx, q, task)
	}
}

// NotifyCancelledTasks finalizes and publishes persisted cancellations without
// writing another cancel trace. Call after commit when traces were already
// recorded in the business transaction (for example chat session deletion).
func (s *TaskService) NotifyCancelledTasks(ctx context.Context, cancelled []db.AgentTaskQueue, persistedEvents []events.Event) {
	for _, t := range cancelled {
		s.finalizeTaskCancelledSideEffects(ctx, t)
	}
	s.reconcileCancelledTaskAgents(ctx, cancelled)
	s.publishTaskEvents(persistedEvents)
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
		if _, ok := ParseQuickCreateContext(task); ok {
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
	if qc, ok := ParseQuickCreateContext(task); ok {
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
	if qc, ok := ParseQuickCreateContext(task); ok {
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
