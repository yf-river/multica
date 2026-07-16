package handler

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ---------------------------------------------------------------------------
// Workspace / Project dashboard
//
// Four read endpoints power the workspace dashboard:
//
//   GET /api/dashboard/usage/daily       per-(date, model) token rows
//   GET /api/dashboard/usage/by-agent    per-(agent, model) token rows
//   GET /api/dashboard/agent-runtime     per-agent run-time + task counts
//   GET /api/dashboard/runtime/daily     per-date run-time + task counts
//
// All four accept ?days=N (defaults to 30, capped at 365) and an optional
// ?project_id=<uuid> to scope the rollup to a single project. With no
// project_id the data spans the whole workspace.
//
// Cost is computed server-side from the maintained pricing catalog. The model
// dimension remains on the wire so clients can show which rows were unpriced.
//
// Access control: workspace membership only — we don't filter by per-agent
// visibility on the dashboard because token spend / run time are workspace-
// level operational metrics. Agent-detail pages still gate on per-agent
// access (see GetWorkspaceAgentRunCounts).
// ---------------------------------------------------------------------------

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

// dashboardUsageDailyResponse is one (date, provider, model) bucket.
type dashboardUsageDailyResponse struct {
	Date      string `json:"date"`
	TaskCount int32  `json:"task_count"`
	usageResponse
}

// GetDashboardUsageDaily returns per-(date, model) token rows for the
// workspace, optionally scoped to a project. Backed by task_usage_hourly,
// sliced into calendar days under the viewer's tz.
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

// GetDashboardUsageByAgent returns per-(agent, model) token aggregates
// for the workspace, optionally scoped to a project. Backed by
// task_usage_hourly with the viewer's tz applied to the `?days=` cutoff.
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

// dashboardAgentRunTimeResponse is one agent's total terminal-task run time
// over the window. Includes failed tasks so the dashboard can surface how
// much execution time was spent on runs that didn't succeed.
type dashboardAgentRunTimeResponse struct {
	AgentID      string `json:"agent_id"`
	TotalSeconds int64  `json:"total_seconds"`
	TaskCount    int32  `json:"task_count"`
	FailedCount  int32  `json:"failed_count"`
}

// GetDashboardAgentRunTime returns per-agent total task run time (seconds)
// and task counts for the workspace, optionally scoped to a project. Only
// terminal tasks (completed or failed) with both started_at and
// completed_at populated contribute, since queued/running tasks have no
// finite duration.
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

// dashboardRunTimeDailyResponse is one (date) bucket of terminal-task run
// time and counts. Powers the workspace dashboard's daily Time and Tasks
// charts — same toggle as Tokens / Cost, different metric.
type dashboardRunTimeDailyResponse struct {
	Date         string `json:"date"`
	TotalSeconds int64  `json:"total_seconds"`
	TaskCount    int32  `json:"task_count"`
	FailedCount  int32  `json:"failed_count"`
}

// GetDashboardRunTimeDaily returns per-date total task run time and task
// counts for the workspace, optionally scoped to a project. Only terminal
// tasks (completed or failed) with both started_at and completed_at
// populated contribute. Bucketed by completed_at so the day boundaries
// line up with the per-agent run-time card.
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
