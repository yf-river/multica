// task_trace.go —— 任务执行轨迹（task trace）事件记录：把每次 task 的关键阶段（用户输入已接收、入队、
// 完成/失败等）落成带元数据的轨迹事件。核心是「用户输入」侧：按任务来源（issue 评论/chat/quick-create/
// autopilot/direct）分别构建元数据——包括原文快照、摘要、sha256、附件与来源 URL——并保证每个任务只记
// 一条 user_input.received。事件行还尽量回填 issue/squad/project 等维度字段，供轨迹面板过滤聚合。
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

// captureTaskUserInput 记录任务的「用户输入已接收」轨迹事件。若该任务已存在此事件则跳过（幂等）；
// 构建不出输入元数据时也静默不记。
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

// buildTaskUserInputMetadata 按任务携带的来源线索挑选对应的元数据构建器：触发评论 > chat 会话 >
// autopilot run > quick-create > issue > 直接任务。
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

// buildIssueUserInputMetadata 以关联 issue 为输入源构建元数据；autopilot 来源的 issue 再补 run 与
// 触发 payload 摘要。issue 加载失败时降级为直接任务元数据。
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

// buildCommentUserInputMetadata 以触发评论为输入源构建元数据；评论缺失时降级为 issue 维度。
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

// buildChatUserInputMetadata 以 chat 会话为输入源构建元数据（actor 优先取发起人）；会话缺失时降级。
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

// buildQuickCreateUserInputMetadata 从 quick-create 上下文（任务 Context JSON）构建元数据。
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

// buildAutopilotUserInputMetadata 以 autopilot run（及其触发 payload 摘要）为输入源构建元数据；
// run/autopilot 行缺失时降级为直接任务元数据。
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

// buildDirectUserInputMetadata 是兜底：无评论/会话/autopilot/quick-create 来源的「直接任务」元数据。
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

// baseUserInputMetadata 组装各类用户输入元数据的公共字段：来源/actor 标识、标题与内容快照（含截断
// 标志与 sha256）、空的附件数组兜底。
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

// textWithTitle 把标题与正文拼成一段展示文本（各自空则只用另一个）。
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

// truncateSnapshot 按 rune 数截断快照（多余返回截断标志）。按 rune 而非字节，避免切坏多字节字符。
func truncateSnapshot(s string, maxRunes int) (string, bool) {
	rs := []rune(strings.TrimSpace(s))
	if len(rs) <= maxRunes {
		return string(rs), false
	}
	return string(rs[:maxRunes]), true
}

// userInputContentSHA256 对输入内容做 sha256，供后续对账去重。
func userInputContentSHA256(s string) string {
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", sum[:])
}

// summarizeRawJSON 把原始 JSON 归一化（解出来再紧凑序列化，压掉空白差异）后截断成摘要。
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

// attachmentMetadataList 把附件行列表折叠成轨迹元数据里的小型 JSON 数组。
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

// putStringIfPresent 值非空才写入 metadata。
func putStringIfPresent(metadata map[string]any, key, value string) {
	if strings.TrimSpace(value) != "" {
		metadata[key] = value
	}
}

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// taskTraceOptions 是一次轨迹事件的可选字段：各段耗时（ms）、模型/Token 用量、失败信息与自定义
// Metadata（JSON）。
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

// recordTaskTraceEvent 把一条轨迹事件落库；失败只记日志（轨迹是尽力而为的观测数据）。
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

// buildTaskTraceEventParams 把任务与选项组装成轨迹事件行参数，并顺带回填 issue/squad/project 维度。
// workspace id 解析失败时返回 ok=false（不入库）。
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

	// task_trace_event.chat_session_id 是硬外键。DeleteChatSession 会在删除前、于同一事务里写取消轨迹；
	// 其他提交后取消路径可能与会话硬删除赛跑。这里倾向保留取消证据：会话没了时只丢弃 FK 列，把 id
	// 留在 metadata 里供日后取证。
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

// mergeTaskTraceMetadata 把 extra 键值合并进既有 JSON metadata；原值必须能解成 JSON 对象。
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

// durationMilliseconds 算两时间戳的毫秒差；任一端无效或差为负时回退（无效→零值，负→0）。
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
