package handler

import (
	"log/slog"
	"net/http"

	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 运行消息和交互共用任务可见性检查，包含私有聊天与 task token 的边界。
func (h *Handler) loadTaskForUser(w http.ResponseWriter, r *http.Request, taskID string) (db.AgentTaskQueue, bool) {
	taskUUID, ok := parseUUIDOrBadRequest(w, taskID, "task_id")
	if !ok {
		return db.AgentTaskQueue{}, false
	}

	task, err := h.Queries.GetAgentTask(r.Context(), taskUUID)
	if err != nil {
		if !isNotFound(err) {
			slog.Warn("get agent task failed", "task_id", taskID, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to load task")
			return db.AgentTaskQueue{}, false
		}
		writeError(w, http.StatusNotFound, "task not found")
		return db.AgentTaskQueue{}, false
	}

	// Verify the task belongs to the caller's workspace. A failed lookup is a
	// 5xx here too: this endpoint does not drive the daemon's interrupt, but
	// telling a reader "this task does not exist" because the DB blinked is
	// the same lie, and it trains clients to give up on a retryable error.
	wsID, err := h.TaskService.ResolveTaskWorkspaceIDChecked(r.Context(), task)
	if err != nil {
		slog.Warn("resolve task workspace failed", "task_id", taskID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load task")
		return db.AgentTaskQueue{}, false
	}
	// Mismatch stays a 404 rather than a 403: a task in another workspace must
	// be indistinguishable from one that does not exist.
	if wsID == "" || wsID != middleware.WorkspaceIDFromContext(r.Context()) {
		writeError(w, http.StatusNotFound, "task not found")
		return db.AgentTaskQueue{}, false
	}
	if task.IssueID.Valid {
		issue, err := h.Queries.GetIssue(r.Context(), task.IssueID)
		if err != nil || !healthIssueVisible(issue, requestUserID(r)) {
			writeError(w, 404, "task not found")
			return db.AgentTaskQueue{}, false
		}
	}

	if task.ChatSessionID.Valid {
		if r.Header.Get("X-Actor-Source") == "task_token" {
			sourceID, valid := parseUUIDOrBadRequest(w, r.Header.Get("X-Task-ID"), "task_id")
			if !valid {
				return db.AgentTaskQueue{}, false
			}
			source, err := h.Queries.GetAgentTask(r.Context(), sourceID)
			if err != nil || !rerunSourceMatchesTaskScope(task, source) {
				writeError(w, http.StatusForbidden, "not your chat session")
				return db.AgentTaskQueue{}, false
			}
		} else if _, ok := h.gateChatSessionForUser(w, r, requestUserID(r), wsID, uuidToString(task.ChatSessionID)); !ok {
			return db.AgentTaskQueue{}, false
		}
	}

	return task, true
}
