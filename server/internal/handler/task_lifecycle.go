package handler

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// RecoverOrphanedTasks atomically fails a Runtime's stale active tasks and
// materializes eligible retries when that Runtime reconnects.
func (h *Handler) RecoverOrphanedTasks(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	if _, ok := h.requireDaemonRuntimeAccess(w, r, runtimeID); !ok {
		return
	}

	batch, err := h.TaskService.RecoverOrphanedTasksForRuntime(r.Context(), parseUUID(runtimeID))
	if err != nil {
		slog.Warn("recover-orphans failed", "runtime_id", runtimeID, "error", err)
		writeError(w, http.StatusInternalServerError, "recover orphans failed")
		return
	}

	// Use the same post-failure projection as the Runtime sweeper.
	h.TaskService.HandleFailedTasks(r.Context(), batch.Tasks)

	if len(batch.Tasks) > 0 {
		slog.Info("recover-orphans completed",
			"runtime_id", runtimeID,
			"orphaned", len(batch.Tasks),
			"retried", batch.Retried,
		)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"orphaned": len(batch.Tasks),
		"retried":  batch.Retried,
	})
}

// PinTaskSession persists resume state as soon as the daemon discovers it.
func (h *Handler) PinTaskSession(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	if _, ok := h.requireDaemonTaskAccess(w, r, taskID); !ok {
		return
	}

	var req protocol.DaemonTaskSessionRequest
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	if req.SessionID == "" && req.WorkDir == "" {
		writeError(w, http.StatusBadRequest, "session_id or work_dir required")
		return
	}

	params := db.UpdateAgentTaskSessionParams{ID: parseUUID(taskID)}
	if req.SessionID != "" {
		params.SessionID = pgtype.Text{String: req.SessionID, Valid: true}
	}
	if req.WorkDir != "" {
		params.WorkDir = pgtype.Text{String: req.WorkDir, Valid: true}
	}
	if err := h.Queries.UpdateAgentTaskSession(r.Context(), params); err != nil {
		slog.Warn("pin-session failed", "task_id", taskID, "error", err)
		writeError(w, http.StatusInternalServerError, "pin session failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RerunIssueRequest selects exactly one explicit rerun target.
type RerunIssueRequest struct {
	// TaskID reruns the Agent and role recorded on an execution-log row.
	TaskID string `json:"task_id,omitempty"`
	// Target selects the current assignee without an execution-log row.
	Target string `json:"target,omitempty"`
}

// RerunIssue explicitly targets a past task or the current assignee and always
// starts a fresh session. Automatic infrastructure retries retain sessions.
func (h *Handler) RerunIssue(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, id)
	if !ok {
		return
	}

	var req RerunIssueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.TaskID = strings.TrimSpace(req.TaskID)
	req.Target = strings.TrimSpace(req.Target)
	if req.TaskID == "" && req.Target == "" {
		writeError(w, http.StatusBadRequest, "rerun target is required")
		return
	}
	if req.TaskID != "" && req.Target != "" {
		writeError(w, http.StatusBadRequest, "task_id and target are mutually exclusive")
		return
	}
	if req.Target != "" && req.Target != "current_assignee" {
		writeError(w, http.StatusBadRequest, "target must be current_assignee")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	actorID := parseUUID(userID)
	idempotencyKey, ok := requireIdempotencyKey(w, r)
	if !ok {
		return
	}
	requestHash, err := hashRequestFingerprint(req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fingerprint issue rerun")
		return
	}
	loadReplay := func() (AgentTaskResponse, bool, error) {
		return loadResourceCreateReplay(
			r.Context(), h.Queries, issue.WorkspaceID, actorID, resourceTypeIssueRerun,
			idempotencyKey, requestHash,
			func(response AgentTaskResponse) bool { return response.ID != "" },
		)
	}
	writeReplayError := resourceCreateReplayErrorWriter(
		"Idempotency-Key was already used with a different request",
		"failed to replay issue rerun",
	)
	if handleResourceCreateReplay(w, http.StatusAccepted, loadReplay, writeReplayError) {
		return
	}

	var sourceTaskID pgtype.UUID
	if req.TaskID != "" {
		parsed, ok := parseUUIDOrBadRequest(w, req.TaskID, "task_id")
		if !ok {
			return
		}
		sourceTaskID = parsed
	}

	tx, qtx, ok := h.beginResourceCreateTransaction(w, r.Context(), "failed to start issue rerun transaction")
	if !ok {
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	if !handleResourceCreateReservation(
		w, r.Context(), tx,
		reserveResourceCreateRequest(r.Context(), qtx, issue.WorkspaceID, actorID, resourceTypeIssueRerun, idempotencyKey, requestHash),
		loadReplay,
		func(w http.ResponseWriter, replayErr error) {
			writeError(w, http.StatusInternalServerError, "issue rerun replay disappeared after conflict")
		},
		"failed to reserve issue rerun request", http.StatusAccepted,
	) {
		return
	}
	result, err := h.TaskService.RerunIssueInTx(r.Context(), qtx, issue.ID, sourceTaskID, pgtype.UUID{})
	if err != nil {
		slog.Warn("issue rerun failed", "issue_id", id, "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	response := taskToResponse(result.Task, uuidToString(issue.WorkspaceID))
	if err := completeResourceCreateRequest(
		r.Context(), qtx, issue.WorkspaceID, actorID, resourceTypeIssueRerun,
		idempotencyKey, requestHash, result.Task.ID, response,
	); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to complete issue rerun request")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit issue rerun")
		return
	}
	h.TaskService.PublishRerunIssue(r.Context(), result)
	writeJSON(w, http.StatusAccepted, response)
}
