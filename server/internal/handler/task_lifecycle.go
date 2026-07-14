package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// RecoverOrphanedTasks is called by the daemon at startup for each runtime
// it owns. It atomically fails any dispatched/running tasks the server still
// believes belong to that runtime and materializes each eligible retry in the
// same transaction, so the user sees a fresh attempt instead of a stuck row.
//
// This is the targeted fix for "issue stuck at in_progress when daemon
// restarts mid-task": the runtime heartbeat sweeper takes up to 75s + the
// in-process task timeout (2.5h) to notice such tasks; the daemon itself
// knows the moment it comes back up, so we let it report orphan recovery.
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

	// Funnel through the shared post-failure pipeline so we get the same
	// task:failed events, agent reconcile, issue rollback, and auto-retry
	// behaviour as the runtime sweeper. This was previously a fast-path
	// that bypassed those side effects, leaving the UI stale when no retry
	// was created (max_attempts exhausted, autopilot, non-retryable reason).
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

// PinTaskSession lets the daemon persist the agent's session_id and
// work_dir as soon as they're known — typically right after the agent
// emits its first system message — so a crash mid-run doesn't lose the
// resume pointer needed to continue the conversation on the next attempt.
type PinTaskSessionRequest struct {
	SessionID string `json:"session_id,omitempty"`
	WorkDir   string `json:"work_dir,omitempty"`
}

func (h *Handler) PinTaskSession(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	if _, ok := h.requireDaemonTaskAccess(w, r, taskID); !ok {
		return
	}

	var req PinTaskSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
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
	// TaskID identifies the execution-log row the user clicked retry on.
	// When set, the rerun targets the agent that ran that specific task
	// (and reuses its leader/worker role) rather than the issue's current
	// assignee — so clicking retry on row that belonged to a now-displaced
	// agent re-fires that same agent, not the new assignee.
	TaskID string `json:"task_id,omitempty"`
	// Target is current_assignee for a CLI-level issue rerun that does not
	// originate from a specific execution-log row.
	Target string `json:"target,omitempty"`
}

// RerunIssue manually re-enqueues an agent run for the issue. The request must
// explicitly select the current assignee or the agent that ran a specific past
// task. The new task is flagged force_fresh_session=true:
// the daemon claim handler skips the (agent_id, issue_id) session-resume
// lookup so the agent starts a clean session. A user clicking rerun has just
// judged the prior output bad — replaying the same conversation would replay
// the same poisoned state. (Automatic retry, by contrast, intentionally
// inherits the session — that path handles infrastructure failures, not bad
// output.)
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
	idempotencyKey, ok := optionalIdempotencyKey(w, r)
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
	if replay, found, replayErr := loadReplay(); replayErr != nil {
		writeResourceCreateReplayError(
			w, replayErr,
			"Idempotency-Key was already used with a different request",
			"failed to replay issue rerun",
		)
		return
	} else if found {
		w.Header().Set("Idempotency-Replayed", "true")
		writeJSON(w, http.StatusAccepted, replay)
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

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start issue rerun transaction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	err = reserveResourceCreateRequest(r.Context(), qtx, issue.WorkspaceID, actorID, resourceTypeIssueRerun, idempotencyKey, requestHash)
	if errors.Is(err, pgx.ErrNoRows) {
		replay, replayErr := loadReplayAfterReservationConflict(r.Context(), tx, loadReplay)
		if replayErr != nil {
			writeError(w, http.StatusInternalServerError, "issue rerun replay disappeared after conflict")
			return
		}
		w.Header().Set("Idempotency-Replayed", "true")
		writeJSON(w, http.StatusAccepted, replay)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reserve issue rerun request")
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
