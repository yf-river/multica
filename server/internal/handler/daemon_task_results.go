package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/redact"
)

func (h *Handler) CompleteTask(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")

	// Verify the caller owns this task's workspace.
	_, workspaceID, ok := h.requireDaemonTaskAccessWithWorkspace(w, r, taskID)
	if !ok {
		return
	}

	var req TaskCompleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	result, _ := json.Marshal(req)
	task, err := h.TaskService.CompleteTask(r.Context(), parseUUID(taskID), result, req.SessionID, req.WorkDir)
	if err != nil {
		slog.Warn("complete task failed", "task_id", taskID, "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	h.emitIssueExecutedOnFirstCompletion(r, task)
	h.captureTaskUsageUnavailableIfMissing(r.Context(), *task)

	slog.Info("task completed", "task_id", taskID, "agent_id", uuidToString(task.AgentID))
	writeJSON(w, http.StatusOK, taskToResponse(*task, workspaceID))
}

func (h *Handler) captureTaskUsageUnavailableIfMissing(ctx context.Context, task db.AgentTaskQueue) {
	usages, err := h.Queries.GetTaskUsage(ctx, task.ID)
	if err != nil {
		slog.Warn("complete task: failed to inspect task usage",
			"task_id", uuidToString(task.ID),
			"error", err,
		)
		return
	}
	if len(usages) > 0 {
		return
	}
	h.TaskService.CaptureTaskUsageUnavailable(ctx, task, "daemon 完成任务时没有随结果上报模型 token 用量")
}

// emitIssueExecutedOnFirstCompletion atomically flips issue.first_executed_at
// and fires the issue_executed analytics event iff this is the first task on
// the issue to reach terminal done. Retries / re-assignments / comment-
// triggered follow-ups hit the WHERE first_executed_at IS NULL clause and
// no-op, so the funnel counts unique issues, not tasks.
func (h *Handler) emitIssueExecutedOnFirstCompletion(r *http.Request, task *db.AgentTaskQueue) {
	if task == nil {
		return
	}
	marked, err := h.Queries.MarkIssueFirstExecuted(r.Context(), task.IssueID)
	if err != nil {
		if !isNotFound(err) {
			slog.Warn("analytics: mark issue first-executed failed", "issue_id", uuidToString(task.IssueID), "error", err)
		}
		return
	}
	var durationMS int64
	if task.StartedAt.Valid && task.CompletedAt.Valid {
		durationMS = task.CompletedAt.Time.Sub(task.StartedAt.Time).Milliseconds()
	}
	taskContext := h.TaskService.AnalyticsContextForTask(r.Context(), *task)
	// distinct_id prefers the human creator so agent-driven events flow into
	// the issue-author's person profile (same place signup and
	// workspace_created land). Agent-created issues keep the agent id with a
	// prefix so PostHog doesn't merge them into a user by accident.
	distinct := uuidToString(marked.CreatorID)
	if marked.CreatorType == "agent" {
		distinct = "agent:" + distinct
	}
	obsmetrics.RecordEvent(h.Analytics, h.Metrics, analytics.IssueExecuted(
		distinct,
		uuidToString(marked.WorkspaceID),
		uuidToString(marked.ID),
		uuidToString(task.ID),
		uuidToString(task.AgentID),
		taskContext.Source,
		taskContext.RuntimeMode,
		taskContext.Provider,
		durationMS,
	))
}

// ReportTaskUsage stores per-task token usage. Called independently of
// complete/fail so usage is captured even when tasks fail or are blocked.
type TaskUsagePayload struct {
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
}

func (h *Handler) ReportTaskUsage(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")

	// Verify the caller owns this task's workspace.
	task, ok := h.requireDaemonTaskAccess(w, r, taskID)
	if !ok {
		return
	}

	var req struct {
		Usage []TaskUsagePayload `json:"usage"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Usage) == 0 {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	// Provider is lowercased on write so client-side pricing lookups tolerate
	// case drift. An empty provider is stamped from the task's runtime, so
	// generic model ids like `auto` still resolve to a provider instead of
	// landing as '' and pricing $0.
	type normalizedUsage struct {
		provider string
		usage    TaskUsagePayload
	}
	normalized := make([]normalizedUsage, 0, len(req.Usage))
	var runtimeProvider string
	runtimeProviderLoaded := false
	for _, u := range req.Usage {
		u.Model = strings.TrimSpace(u.Model)
		if u.Model == "" {
			writeError(w, http.StatusBadRequest, "usage model is required")
			return
		}
		if u.InputTokens < 0 || u.OutputTokens < 0 || u.CacheReadTokens < 0 || u.CacheWriteTokens < 0 {
			writeError(w, http.StatusBadRequest, "usage token counts must be non-negative")
			return
		}
		provider := normalizeProvider(u.Provider)
		if provider == "" {
			if !runtimeProviderLoaded {
				rt, err := h.Queries.GetAgentRuntime(r.Context(), task.RuntimeID)
				if err != nil {
					writeError(w, http.StatusInternalServerError, "failed to load runtime provider")
					return
				}
				runtimeProvider = normalizeProvider(rt.Provider)
				runtimeProviderLoaded = true
			}
			provider = runtimeProvider
		}
		if provider == "" {
			writeError(w, http.StatusInternalServerError, "runtime provider is unavailable")
			return
		}
		normalizedPayload, err := h.normalizeTaskUsagePayload(r.Context(), task, provider, u)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to normalize task usage")
			return
		}
		normalized = append(normalized, normalizedUsage{provider: provider, usage: normalizedPayload})
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to begin task usage update")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	for _, item := range normalized {
		u := item.usage
		if err := qtx.UpsertTaskUsage(r.Context(), db.UpsertTaskUsageParams{
			TaskID:           parseUUID(taskID),
			Provider:         item.provider,
			Model:            u.Model,
			InputTokens:      u.InputTokens,
			OutputTokens:     u.OutputTokens,
			CacheReadTokens:  u.CacheReadTokens,
			CacheWriteTokens: u.CacheWriteTokens,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to store task usage")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit task usage")
		return
	}

	for _, item := range normalized {
		u := item.usage
		h.TaskService.CaptureTaskUsage(r.Context(), task, item.provider, u.Model, u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) normalizeTaskUsagePayload(ctx context.Context, task db.AgentTaskQueue, provider string, u TaskUsagePayload) (TaskUsagePayload, error) {
	if provider != "codebuddy" {
		return u, nil
	}
	previous, ok, err := h.previousCodebuddySessionUsage(ctx, task, provider, u.Model)
	if err != nil {
		return TaskUsagePayload{}, err
	}
	if ok {
		u.InputTokens = nonNegativeDelta(u.InputTokens, previous.InputTokens)
		u.OutputTokens = nonNegativeDelta(u.OutputTokens, previous.OutputTokens)
		u.CacheReadTokens = nonNegativeDelta(u.CacheReadTokens, previous.CacheReadTokens)
	}
	u.CacheWriteTokens = 0
	return u, nil
}

type taskUsageTotals struct {
	InputTokens     int64
	OutputTokens    int64
	CacheReadTokens int64
}

func (h *Handler) previousCodebuddySessionUsage(ctx context.Context, task db.AgentTaskQueue, provider, model string) (taskUsageTotals, bool, error) {
	if h == nil || h.DB == nil || !task.SessionID.Valid || strings.TrimSpace(task.SessionID.String) == "" || strings.TrimSpace(model) == "" {
		return taskUsageTotals{}, false, nil
	}
	var previous taskUsageTotals
	err := h.DB.QueryRow(ctx, `
		SELECT
			COALESCE(SUM(tu.input_tokens), 0)::bigint,
			COALESCE(SUM(tu.output_tokens), 0)::bigint,
			COALESCE(SUM(tu.cache_read_tokens), 0)::bigint
		FROM task_usage tu
		JOIN agent_task_queue atq ON atq.id = tu.task_id
		WHERE atq.session_id = $1
		  AND atq.id <> $2
		  AND atq.created_at < $3
		  AND tu.provider = $4
		  AND tu.model = $5
	`, task.SessionID.String, task.ID, task.CreatedAt, provider, model).Scan(
		&previous.InputTokens,
		&previous.OutputTokens,
		&previous.CacheReadTokens,
	)
	if err != nil {
		return taskUsageTotals{}, false, err
	}
	return previous, previous.InputTokens > 0 || previous.OutputTokens > 0 || previous.CacheReadTokens > 0, nil
}

func nonNegativeDelta(current, previous int64) int64 {
	if current <= previous {
		return 0
	}
	return current - previous
}

// GetTaskStatus returns the current status of a task.
// Used by the daemon to detect terminal/interruption signals (cancelled,
// failed, completed) while a task is executing mid-flight.
func (h *Handler) GetTaskStatus(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")

	// Verify the caller owns this task's workspace.
	task, ok := h.requireDaemonTaskAccess(w, r, taskID)
	if !ok {
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": task.Status})
}

// FailTask marks a running task as failed.
type TaskFailRequest struct {
	Error         string `json:"error"`
	SessionID     string `json:"session_id,omitempty"`
	WorkDir       string `json:"work_dir,omitempty"`
	FailureReason string `json:"failure_reason,omitempty"`
}

func (h *Handler) FailTask(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")

	// Verify the caller owns this task's workspace.
	_, workspaceID, ok := h.requireDaemonTaskAccessWithWorkspace(w, r, taskID)
	if !ok {
		return
	}

	var req TaskFailRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	task, err := h.TaskService.FailTask(r.Context(), parseUUID(taskID), req.Error, req.SessionID, req.WorkDir, req.FailureReason)
	if err != nil {
		slog.Warn("fail task failed", "task_id", taskID, "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	slog.Info("task failed", "task_id", taskID, "agent_id", uuidToString(task.AgentID), "task_error", req.Error, "failure_reason", task.FailureReason.String)
	writeJSON(w, http.StatusOK, taskToResponse(*task, workspaceID))
}

// ---------------------------------------------------------------------------
// Task Messages (live agent output)
// ---------------------------------------------------------------------------

type TaskMessageRequest struct {
	Seq     int            `json:"seq"`
	Type    string         `json:"type"`
	Tool    string         `json:"tool,omitempty"`
	Content string         `json:"content,omitempty"`
	Input   map[string]any `json:"input,omitempty"`
	Output  string         `json:"output,omitempty"`
}

type TaskMessageBatchRequest struct {
	Messages []TaskMessageRequest `json:"messages"`
}

// ReportTaskMessages receives a batch of agent execution messages from the daemon.
func (h *Handler) ReportTaskMessages(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")

	var req TaskMessageBatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// Verify the caller owns this task's workspace.
	task, workspaceID, ok := h.requireDaemonTaskAccessWithWorkspace(w, r, taskID)
	if !ok {
		return
	}
	if len(req.Messages) == 0 {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	type preparedTaskMessage struct {
		request TaskMessageRequest
		input   []byte
	}
	prepared := make([]preparedTaskMessage, 0, len(req.Messages))
	for _, msg := range req.Messages {
		// Redact sensitive information before persisting or broadcasting.
		msg.Type = sanitizePostgresText(msg.Type)
		msg.Tool = sanitizePostgresText(msg.Tool)
		msg.Content = sanitizePostgresText(redact.Text(msg.Content))
		msg.Output = sanitizePostgresText(redact.Text(msg.Output))
		msg.Input = sanitizeTaskMessageInput(redact.InputMap(msg.Input))

		var inputJSON []byte
		if msg.Input != nil {
			var err error
			inputJSON, err = json.Marshal(msg.Input)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid task message input")
				return
			}
		}
		prepared = append(prepared, preparedTaskMessage{request: msg, input: inputJSON})
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to begin task message update")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	createdMessages := make([]db.TaskMessage, 0, len(prepared))
	for _, item := range prepared {
		msg := item.request
		created, err := qtx.CreateTaskMessageIdempotent(r.Context(), db.CreateTaskMessageIdempotentParams{
			TaskID:  parseUUID(taskID),
			Seq:     int32(msg.Seq),
			Type:    msg.Type,
			Tool:    pgtype.Text{String: msg.Tool, Valid: msg.Tool != ""},
			Content: pgtype.Text{String: msg.Content, Valid: msg.Content != ""},
			Input:   item.input,
			Output:  pgtype.Text{String: msg.Output, Valid: msg.Output != ""},
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to persist task message")
			return
		}
		if created.Inserted {
			createdMessages = append(createdMessages, db.TaskMessage{
				ID:        created.ID,
				TaskID:    created.TaskID,
				Seq:       created.Seq,
				Type:      created.Type,
				Tool:      created.Tool,
				Content:   created.Content,
				Input:     created.Input,
				Output:    created.Output,
				CreatedAt: created.CreatedAt,
			})
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit task messages")
		return
	}

	for _, created := range createdMessages {
		h.publishTask(protocol.EventTaskMessage, workspaceID, "system", "", taskID,
			taskMessageToPayload(created, taskID, uuidToString(task.IssueID)))
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func sanitizeTaskMessageInput(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	sanitized, _ := sanitizeTaskMessageValue(input).(map[string]any)
	return sanitized
}

func sanitizeTaskMessageValue(value any) any {
	switch typed := value.(type) {
	case string:
		return sanitizePostgresText(typed)
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[sanitizePostgresText(key)] = sanitizeTaskMessageValue(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = sanitizeTaskMessageValue(item)
		}
		return out
	default:
		return value
	}
}

func taskMessageToPayload(m db.TaskMessage, taskID, issueID string) protocol.TaskMessagePayload {
	var input map[string]any
	if m.Input != nil {
		if err := json.Unmarshal(m.Input, &input); err != nil {
			slog.Warn("decode task message input failed", "task_id", taskID, "error", err)
		}
	}
	createdAt := ""
	if m.CreatedAt.Valid {
		createdAt = m.CreatedAt.Time.UTC().Format(time.RFC3339Nano)
	}
	return protocol.TaskMessagePayload{
		TaskID:    taskID,
		IssueID:   issueID,
		Seq:       int(m.Seq),
		Type:      m.Type,
		Tool:      m.Tool.String,
		Content:   m.Content.String,
		Input:     input,
		Output:    m.Output.String,
		CreatedAt: createdAt,
	}
}

// ListTaskMessages returns the persisted messages for a task (for catch-up after reconnect).
func (h *Handler) ListTaskMessages(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")

	// Verify the caller owns this task's workspace.
	task, ok := h.requireDaemonTaskAccess(w, r, taskID)
	if !ok {
		return
	}

	var (
		messages []db.TaskMessage
		err      error
	)
	if sinceStr := r.URL.Query().Get("since"); sinceStr != "" {
		sinceSeq, parseErr := strconv.Atoi(sinceStr)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "invalid since parameter")
			return
		}
		messages, err = h.Queries.ListTaskMessagesSince(r.Context(), db.ListTaskMessagesSinceParams{
			TaskID: parseUUID(taskID),
			Seq:    int32(sinceSeq),
		})
	} else {
		messages, err = h.Queries.ListTaskMessages(r.Context(), parseUUID(taskID))
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list task messages")
		return
	}

	issueID := uuidToString(task.IssueID)

	resp := make([]protocol.TaskMessagePayload, len(messages))
	for i, m := range messages {
		resp[i] = taskMessageToPayload(m, taskID, issueID)
	}

	writeJSON(w, http.StatusOK, resp)
}

// GetActiveTaskForIssue returns all currently active tasks for an issue.
// Returns { tasks: [...] } array (may be empty).
func (h *Handler) GetActiveTaskForIssue(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}

	tasks, err := h.Queries.ListActiveTasksByIssue(r.Context(), issue.ID)
	if err != nil {
		if writeClientClosedIfCanceled(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to list active tasks")
		return
	}

	workspaceID := uuidToString(issue.WorkspaceID)
	resp := make([]AgentTaskResponse, len(tasks))
	for i, t := range tasks {
		resp[i] = taskToResponse(t, workspaceID)
	}

	writeJSON(w, http.StatusOK, map[string]any{"tasks": resp})
}

// CancelTask cancels a running or queued task by ID.
// Verifies both that the URL-parameter issue belongs to the caller's workspace
// and that the task belongs to that same issue — a task UUID from a different
// issue (in any workspace) must not be cancellable through this route.
func (h *Handler) CancelTask(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}

	taskID := chi.URLParam(r, "taskId")
	taskUUID, ok := parseUUIDOrBadRequest(w, taskID, "task_id")
	if !ok {
		return
	}
	existing, err := h.Queries.GetAgentTask(r.Context(), taskUUID)
	if err != nil {
		writeEntityLoadError(w, r, err, "task", "task_id", taskID)
		return
	}
	if uuidToString(existing.IssueID) != uuidToString(issue.ID) {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}

	task, err := h.TaskService.CancelTask(r.Context(), existing.ID)
	if err != nil {
		slog.Warn("cancel task failed", "task_id", taskID, "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	slog.Info("task cancelled by user", "task_id", taskID, "issue_id", uuidToString(task.IssueID))
	writeJSON(w, http.StatusOK, taskToResponse(*task, uuidToString(issue.WorkspaceID)))
}

// ListTasksByIssue returns all tasks (any status) for an issue — used for execution history.
func (h *Handler) ListTasksByIssue(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}

	tasks, err := h.Queries.ListTasksByIssue(r.Context(), issue.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list tasks")
		return
	}

	workspaceID := uuidToString(issue.WorkspaceID)
	resp := make([]AgentTaskResponse, len(tasks))
	for i, t := range tasks {
		resp[i] = taskToResponse(t, workspaceID)
	}

	writeJSON(w, http.StatusOK, resp)
}

// ListTaskMessagesByUser returns task messages for a task.
// Used by the frontend under regular user auth (not daemon auth).
// Verifies the task belongs to the caller's workspace.
func (h *Handler) ListTaskMessagesByUser(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	taskUUID, ok := parseUUIDOrBadRequest(w, taskID, "task_id")
	if !ok {
		return
	}

	task, err := h.Queries.GetAgentTask(r.Context(), taskUUID)
	if err != nil {
		writeEntityLoadError(w, r, err, "task", "task_id", taskID)
		return
	}

	// Verify the task belongs to the caller's workspace.
	wsID := h.TaskService.ResolveTaskWorkspaceID(r.Context(), task)
	if wsID == "" || wsID != middleware.WorkspaceIDFromContext(r.Context()) {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}

	var (
		messages []db.TaskMessage
		queryErr error
	)
	if sinceStr := r.URL.Query().Get("since"); sinceStr != "" {
		sinceSeq, parseErr := strconv.Atoi(sinceStr)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "invalid since parameter")
			return
		}
		messages, queryErr = h.Queries.ListTaskMessagesSince(r.Context(), db.ListTaskMessagesSinceParams{
			TaskID: taskUUID,
			Seq:    int32(sinceSeq),
		})
	} else {
		messages, queryErr = h.Queries.ListTaskMessages(r.Context(), taskUUID)
	}
	if queryErr != nil {
		if writeClientClosedIfCanceled(w, queryErr) {
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to list task messages")
		return
	}

	issueID := uuidToString(task.IssueID)

	resp := make([]protocol.TaskMessagePayload, len(messages))
	for i, m := range messages {
		resp[i] = taskMessageToPayload(m, taskID, issueID)
	}

	writeJSON(w, http.StatusOK, resp)
}

// GetIssueUsage returns aggregated token usage for all tasks belonging to an issue.
func (h *Handler) GetIssueUsage(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}

	row, err := h.Queries.GetIssueUsageSummary(r.Context(), issue.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get issue usage")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"total_input_tokens":       row.TotalInputTokens,
		"total_output_tokens":      row.TotalOutputTokens,
		"total_cache_read_tokens":  row.TotalCacheReadTokens,
		"total_cache_write_tokens": row.TotalCacheWriteTokens,
		"task_count":               row.TaskCount,
	})
}

type TaskTraceEventResponse struct {
	ID               string         `json:"id"`
	WorkspaceID      string         `json:"workspace_id"`
	TaskID           string         `json:"task_id"`
	IssueID          *string        `json:"issue_id"`
	AgentID          string         `json:"agent_id"`
	RuntimeID        *string        `json:"runtime_id"`
	SquadID          *string        `json:"squad_id"`
	ProjectID        *string        `json:"project_id"`
	Source           string         `json:"source"`
	EventType        string         `json:"event_type"`
	EventName        string         `json:"event_name"`
	Status           string         `json:"status"`
	Attempt          int32          `json:"attempt"`
	DurationMs       *int64         `json:"duration_ms,omitempty"`
	QueueWaitMs      *int64         `json:"queue_wait_ms,omitempty"`
	RunMs            *int64         `json:"run_ms,omitempty"`
	TotalMs          *int64         `json:"total_ms,omitempty"`
	Provider         string         `json:"provider"`
	Model            string         `json:"model"`
	InputTokens      int64          `json:"input_tokens"`
	OutputTokens     int64          `json:"output_tokens"`
	CacheReadTokens  int64          `json:"cache_read_tokens"`
	CacheWriteTokens int64          `json:"cache_write_tokens"`
	FailureReason    string         `json:"failure_reason"`
	ErrorType        string         `json:"error_type"`
	TriggerCommentID *string        `json:"trigger_comment_id"`
	AutopilotRunID   *string        `json:"autopilot_run_id"`
	ChatSessionID    *string        `json:"chat_session_id"`
	Metadata         map[string]any `json:"metadata"`
	CreatedAt        string         `json:"created_at"`
}

func taskTraceEventToResponse(ev db.TaskTraceEvent) (TaskTraceEventResponse, error) {
	var metadata map[string]any
	if err := json.Unmarshal(ev.Metadata, &metadata); err != nil || metadata == nil {
		return TaskTraceEventResponse{}, fmt.Errorf("decode task trace metadata: expected JSON object")
	}
	return TaskTraceEventResponse{
		ID:               uuidToString(ev.ID),
		WorkspaceID:      uuidToString(ev.WorkspaceID),
		TaskID:           uuidToString(ev.TaskID),
		IssueID:          uuidToPtr(ev.IssueID),
		AgentID:          uuidToString(ev.AgentID),
		RuntimeID:        uuidToPtr(ev.RuntimeID),
		SquadID:          uuidToPtr(ev.SquadID),
		ProjectID:        uuidToPtr(ev.ProjectID),
		Source:           ev.Source,
		EventType:        ev.EventType,
		EventName:        ev.EventName,
		Status:           ev.Status,
		Attempt:          ev.Attempt,
		DurationMs:       int8ToPtr(ev.DurationMs),
		QueueWaitMs:      int8ToPtr(ev.QueueWaitMs),
		RunMs:            int8ToPtr(ev.RunMs),
		TotalMs:          int8ToPtr(ev.TotalMs),
		Provider:         ev.Provider,
		Model:            ev.Model,
		InputTokens:      ev.InputTokens,
		OutputTokens:     ev.OutputTokens,
		CacheReadTokens:  ev.CacheReadTokens,
		CacheWriteTokens: ev.CacheWriteTokens,
		FailureReason:    ev.FailureReason,
		ErrorType:        ev.ErrorType,
		TriggerCommentID: uuidToPtr(ev.TriggerCommentID),
		AutopilotRunID:   uuidToPtr(ev.AutopilotRunID),
		ChatSessionID:    uuidToPtr(ev.ChatSessionID),
		Metadata:         metadata,
		CreatedAt:        timestampToString(ev.CreatedAt),
	}, nil
}

func taskTraceEventsToResponse(events []db.TaskTraceEvent) ([]TaskTraceEventResponse, error) {
	response := make([]TaskTraceEventResponse, len(events))
	for i, event := range events {
		converted, err := taskTraceEventToResponse(event)
		if err != nil {
			return nil, err
		}
		response[i] = converted
	}
	return response, nil
}

// ListIssueTaskTraceEvents returns the durable task trace events for an issue.
func (h *Handler) ListIssueTaskTraceEvents(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}

	events, err := h.Queries.ListIssueTaskTraceEvents(r.Context(), issue.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list issue trace events")
		return
	}
	resp, err := taskTraceEventsToResponse(events)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to decode issue trace events")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": resp})
}

// GetIssueGCCheck returns minimal issue info needed by the daemon GC loop.
// Gated on workspace membership so a daemon user cannot read issue metadata
// from another workspace via UUID enumeration.
func (h *Handler) GetIssueGCCheck(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "issueId")
	issueUUID, ok := parseUUIDOrBadRequest(w, issueID, "issue_id")
	if !ok {
		return
	}
	issue, err := h.Queries.GetIssue(r.Context(), issueUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "issue not found")
		return
	}
	if !h.requireDaemonWorkspaceAccess(w, r, uuidToString(issue.WorkspaceID)) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     issue.Status,
		"updated_at": issue.UpdatedAt.Time,
	})
}

// GetChatSessionGCCheck returns the status and updated_at of a chat session
// for the daemon GC loop. A 404 here means the session was hard-deleted
// (DeleteChatSession in chat.go runs a real DELETE), which the daemon treats
// as an immediate-clean signal — the user's explicit delete is the strongest
// reclaim authorization we can get.
//
// Same anti-enumeration shape as GetIssueGCCheck: workspace mismatch returns
// the same 404 so a daemon user can't probe other workspaces.
func (h *Handler) GetChatSessionGCCheck(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")
	sessionUUID, ok := parseUUIDOrBadRequest(w, sessionID, "session_id")
	if !ok {
		return
	}
	session, err := h.Queries.GetChatSession(r.Context(), sessionUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "chat session not found")
		return
	}
	if !h.requireDaemonWorkspaceAccess(w, r, uuidToString(session.WorkspaceID)) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "active",
		"updated_at": session.UpdatedAt.Time,
	})
}

// GetAutopilotRunGCCheck returns an autopilot run's status to the daemon GC
// loop. The daemon decides purely on terminal status:
// an autopilot run's workdir is never reused, so a terminal run is reclaimed on
// sight while non-terminal status is a skip signal.
//
// Workspace ownership is resolved via the parent autopilot row.
func (h *Handler) GetAutopilotRunGCCheck(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runId")
	runUUID, ok := parseUUIDOrBadRequest(w, runID, "run_id")
	if !ok {
		return
	}
	run, err := h.Queries.GetAutopilotRun(r.Context(), runUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "autopilot run not found")
		return
	}
	autopilot, err := h.Queries.GetAutopilot(r.Context(), run.AutopilotID)
	if err != nil {
		// Parent autopilot is gone — treat as not found rather than 500
		// so the daemon can fall through to its orphan-by-mtime path.
		writeError(w, http.StatusNotFound, "autopilot run not found")
		return
	}
	if !h.requireDaemonWorkspaceAccess(w, r, uuidToString(autopilot.WorkspaceID)) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": run.Status,
	})
}

// GetTaskGCCheck returns the agent_task_queue status for quick-create cleanup.
// Quick-create tasks have no parent record (no issue_id at WriteGCMeta time,
// no chat session, no autopilot run) so the daemon keys GC directly on the
// task row itself.
func (h *Handler) GetTaskGCCheck(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	task, ok := h.requireDaemonTaskAccess(w, r, taskID)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": task.Status,
	})
}
