package service

// Task analytics + user-input metadata + dispatch-comment helpers.
// Split out of task.go (which used to host every TaskService method) to keep
// the lifecycle core readable. No logic change; pure code move.
//
// Functions here cover three themes:
//   - trivial-done / dispatch-comment detection used by CompleteTask and the
//     issue-source-summary flow,
//   - capture* / CaptureTask* analytics + usage events published through the
//     analytics.Client and obsmetrics.BusinessMetrics,
//   - build*UserInputMetadata helpers that normalize the task-creation payload
//     stored on the agent_task_queue row.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

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
	s.recordTaskTraceEvent(ctx, task, "task.cancelled", "任务已取消", taskTraceOptions{
		DurationMs:    taskTotalMilliseconds(task),
		QueueWaitMs:   taskQueueWaitMilliseconds(task),
		RunMs:         taskRunMilliseconds(task),
		TotalMs:       taskTotalMilliseconds(task),
		FailureReason: "cancelled",
		ErrorType:     "cancelled",
	})
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
