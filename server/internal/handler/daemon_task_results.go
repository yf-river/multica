package handler

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

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
