package handler

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Dashboard endpoints share a bounded time window and optional Project scope.
// Workspace membership grants aggregate operational metrics; Agent detail
// remains subject to per-Agent access. Cost is derived from the server catalog.

type dashboardQueryScope struct {
	workspaceID pgtype.UUID
	projectID   pgtype.UUID
	timezone    string
	since       pgtype.Timestamptz
}

func (h *Handler) dashboardScope(w http.ResponseWriter, r *http.Request) (dashboardQueryScope, bool) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := requireWorkspaceMemberContext(w, r); !ok {
		return dashboardQueryScope{}, false
	}

	var projectID pgtype.UUID
	raw := r.URL.Query().Get("project_id")
	if raw != "" {
		parsed, err := util.ParseUUID(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid project_id")
			return dashboardQueryScope{}, false
		}
		projectID = parsed
	}

	timezone := h.resolveViewingTZ(r)
	return dashboardQueryScope{
		workspaceID: parseUUID(workspaceID),
		projectID:   projectID,
		timezone:    timezone,
		since:       parseSinceParamInTZ(r, 30, timezone),
	}, true
}

// dashboardUsageDailyResponse is one date/provider/model bucket.
type dashboardUsageDailyResponse struct {
	Date      string `json:"date"`
	TaskCount int32  `json:"task_count"`
	usageResponse
}

// GetDashboardUsageDaily returns token rows in the viewer's calendar days.
func (h *Handler) GetDashboardUsageDaily(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.dashboardScope(w, r)
	if !ok {
		return
	}

	resp, err := h.listDashboardUsageDaily(r.Context(), scope)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list usage")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) listDashboardUsageDaily(
	ctx context.Context,
	scope dashboardQueryScope,
) ([]dashboardUsageDailyResponse, error) {
	rows, err := h.Queries.ListDashboardUsageDaily(ctx, db.ListDashboardUsageDailyParams{
		WorkspaceID: scope.workspaceID,
		Tz:          scope.timezone,
		Since:       scope.since,
		ProjectID:   scope.projectID,
	})
	if err != nil {
		return nil, err
	}
	resp := make([]dashboardUsageDailyResponse, len(rows))
	for i, row := range rows {
		resp[i] = dashboardUsageDailyResponse{
			Date:      row.Date.Time.Format("2006-01-02"),
			TaskCount: row.TaskCount,
			usageResponse: newUsageResponse(
				row.Provider,
				row.Model,
				row.InputTokens,
				row.OutputTokens,
				row.CacheReadTokens,
				row.CacheWriteTokens,
			),
		}
	}
	return resp, nil
}

// GetDashboardUsageByAgent returns per-Agent/model token aggregates.
func (h *Handler) GetDashboardUsageByAgent(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.dashboardScope(w, r)
	if !ok {
		return
	}

	resp, err := h.listDashboardUsageByAgent(r.Context(), scope)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list usage by agent")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) listDashboardUsageByAgent(
	ctx context.Context,
	scope dashboardQueryScope,
) ([]usageByAgentResponse, error) {
	rows, err := h.Queries.ListDashboardUsageByAgent(ctx, db.ListDashboardUsageByAgentParams{
		WorkspaceID: scope.workspaceID,
		Since:       scope.since,
		ProjectID:   scope.projectID,
	})
	if err != nil {
		return nil, err
	}
	resp := make([]usageByAgentResponse, len(rows))
	for i, row := range rows {
		resp[i] = usageByAgentResponse{
			AgentID:   uuidToString(row.AgentID),
			TaskCount: row.TaskCount,
			usageResponse: newUsageResponse(
				row.Provider,
				row.Model,
				row.InputTokens,
				row.OutputTokens,
				row.CacheReadTokens,
				row.CacheWriteTokens,
			),
		}
	}
	return resp, nil
}

// dashboardAgentRunTimeResponse includes failed terminal-task runtime.
type dashboardAgentRunTimeResponse struct {
	AgentID      string `json:"agent_id"`
	TotalSeconds int64  `json:"total_seconds"`
	TaskCount    int32  `json:"task_count"`
	FailedCount  int32  `json:"failed_count"`
}

// GetDashboardAgentRunTime returns finite terminal-task runtime per Agent.
func (h *Handler) GetDashboardAgentRunTime(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.dashboardScope(w, r)
	if !ok {
		return
	}

	rows, err := h.Queries.ListDashboardAgentRunTime(r.Context(), db.ListDashboardAgentRunTimeParams{
		WorkspaceID: scope.workspaceID,
		Since:       scope.since,
		ProjectID:   scope.projectID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list agent runtime")
		return
	}

	resp := make([]dashboardAgentRunTimeResponse, len(rows))
	for i, row := range rows {
		resp[i] = dashboardAgentRunTimeResponse{
			AgentID:      uuidToString(row.AgentID),
			TotalSeconds: row.TotalSeconds,
			TaskCount:    row.TaskCount,
			FailedCount:  row.FailedCount,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// dashboardRunTimeDailyResponse is one day of terminal-task runtime.
type dashboardRunTimeDailyResponse struct {
	Date         string `json:"date"`
	TotalSeconds int64  `json:"total_seconds"`
	TaskCount    int32  `json:"task_count"`
	FailedCount  int32  `json:"failed_count"`
}

// GetDashboardRunTimeDaily groups terminal-task runtime by completion day.
func (h *Handler) GetDashboardRunTimeDaily(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.dashboardScope(w, r)
	if !ok {
		return
	}

	rows, err := h.Queries.ListDashboardRunTimeDaily(r.Context(), db.ListDashboardRunTimeDailyParams{
		WorkspaceID: scope.workspaceID,
		Tz:          scope.timezone,
		Since:       scope.since,
		ProjectID:   scope.projectID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list daily runtime")
		return
	}

	resp := make([]dashboardRunTimeDailyResponse, len(rows))
	for i, row := range rows {
		resp[i] = dashboardRunTimeDailyResponse{
			Date:         row.Date.Time.Format("2006-01-02"),
			TotalSeconds: row.TotalSeconds,
			TaskCount:    row.TaskCount,
			FailedCount:  row.FailedCount,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}
