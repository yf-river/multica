package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

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

	content := session.Title
	sourceID := session.ID

	actorID := session.CreatorID
	if task.InitiatorUserID.Valid {
		actorID = task.InitiatorUserID
	}
	title := firstNonEmpty(session.Title, "Chat 消息")
	metadata := baseUserInputMetadata("chat", "member", actorID, "chat_message", sourceID, title, content)
	metadata["chat_session_id"] = util.UUIDToString(session.ID)
	metadata["source_url"] = "/chats/" + util.UUIDToString(session.ID)
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
	putStringIfPresent(metadata, "priority", qc.Priority)
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
	snapshot, truncated := truncateSnapshot(content, 4000)
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
	workspaceID, _ := util.ParseUUID(s.ResolveTaskWorkspaceID(ctx, task))
	issueID := task.IssueID
	runtimeID := task.RuntimeID
	squadID := pgtype.UUID{}
	projectID := pgtype.UUID{}

	if task.IssueID.Valid {
		if issue, err := s.Queries.GetIssue(ctx, task.IssueID); err == nil {
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
				if !projectID.Valid {
					projectID = ap.ProjectID
				}
			}
		}
	}
	if qc, ok := ParseQuickCreateContext(task); ok {
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
			metadata, err = mergeTaskTraceMetadata(metadata, map[string]any{
				"deleted_chat_session_id": sessionID,
				"chat_session_missing":    true,
			})
			if err != nil {
				slog.Warn("encode missing chat session trace metadata failed", "error", err, "task_id", util.UUIDToString(task.ID))
				return db.CreateTaskTraceEventParams{}, false
			}
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

func mergeTaskTraceMetadata(raw []byte, extra map[string]any) ([]byte, error) {
	base := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &base); err != nil {
			return nil, fmt.Errorf("decode task trace metadata: %w", err)
		}
		if base == nil {
			return nil, errors.New("task trace metadata must be a JSON object")
		}
	}
	for k, v := range extra {
		base[k] = v
	}
	encoded, err := json.Marshal(base)
	if err != nil {
		return nil, fmt.Errorf("encode task trace metadata: %w", err)
	}
	return encoded, nil
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
