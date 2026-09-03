package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// GetIssueExecutionTree returns the issue's task and trace timeline used by
// the run-review page. The current task model is flat; trace rows retain the
// detailed execution evidence.
func (h *Handler) GetIssueExecutionTree(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}
	tasks, err := h.Queries.ListTasksByIssue(r.Context(), issue.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list issue tasks")
		return
	}
	traces, err := h.Queries.ListIssueTaskTraceEvents(r.Context(), issue.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list issue trace events")
		return
	}
	traceResponse, err := taskTraceEventsToResponse(traces)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to decode issue trace events")
		return
	}
	taskResponses := make([]AgentTaskResponse, 0, len(tasks))
	for _, task := range tasks {
		taskResponses = append(taskResponses, taskToResponse(task, uuidToString(issue.WorkspaceID)))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"issue_id":     issue.ID,
		"tasks":        taskResponses,
		"trace_events": traceResponse,
	})
}
