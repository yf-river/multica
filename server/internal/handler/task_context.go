package handler

// Runtime context events for one task run.
//
// The DSH adapter forwards the runtime's per-request context frames as
// task_message rows with type "runtime". They are not model-visible messages;
// the run view consumes them to draw context pressure, composition and
// compaction history without parsing provider session logs.

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// GetTaskContext returns the runtime telemetry events recorded for one task.
// GET /api/issues/{id}/tasks/{taskId}/context
func (h *Handler) GetTaskContext(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	taskID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "taskId"), "task_id")
	if !ok {
		return
	}
	task, err := h.Queries.GetAgentTaskInWorkspace(r.Context(), db.GetAgentTaskInWorkspaceParams{
		ID:          taskID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil || uuidToString(task.IssueID) != uuidToString(issue.ID) {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	messages, err := h.Queries.ListTaskMessages(r.Context(), task.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read task context")
		return
	}
	events := make([]json.RawMessage, 0, len(messages))
	for _, message := range messages {
		if message.Type != "runtime" || !message.Content.Valid || message.Content.String == "" {
			continue
		}
		// Only forward rows that are valid JSON; a partial write must not break
		// the whole panel.
		if !json.Valid([]byte(message.Content.String)) {
			continue
		}
		events = append(events, json.RawMessage(message.Content.String))
		if len(events) >= 5000 {
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}
