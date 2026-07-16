package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type projectResponse struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	Title       string  `json:"title"`
	Description *string `json:"description"`
	Icon        *string `json:"icon"`
	Status      string  `json:"status"`
	Priority    string  `json:"priority"`
	LeadType    *string `json:"lead_type"`
	LeadID      *string `json:"lead_id"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	IssueCount  int64   `json:"issue_count"`
	DoneCount   int64   `json:"done_count"`
	// ResourceCount is a breadcrumb pointing at the sub-collection at
	// /api/projects/{id}/resources. Resources themselves stay out of this
	// payload to keep parent metadata and child collections separate; clients
	// that need the list call ListProjectResources directly.
	ResourceCount int64 `json:"resource_count"`
}

func projectToResponse(p db.Project) projectResponse {
	return projectResponse{
		ID:          uuidToString(p.ID),
		WorkspaceID: uuidToString(p.WorkspaceID),
		Title:       p.Title,
		Description: textToPtr(p.Description),
		Icon:        textToPtr(p.Icon),
		Status:      p.Status,
		Priority:    p.Priority,
		LeadType:    textToPtr(p.LeadType),
		LeadID:      uuidToPtr(p.LeadID),
		CreatedAt:   timestampToString(p.CreatedAt),
		UpdatedAt:   timestampToString(p.UpdatedAt),
	}
}

func (h *Handler) loadProjectCounts(ctx context.Context, projectID pgtype.UUID) (issueCount, doneCount, resourceCount int64, err error) {
	stats, err := h.Queries.GetProjectIssueStats(ctx, []pgtype.UUID{projectID})
	if err != nil {
		return 0, 0, 0, err
	}
	if len(stats) > 0 {
		issueCount = stats[0].TotalCount
		doneCount = stats[0].DoneCount
	}

	rows, err := h.Queries.GetProjectResourceCounts(ctx, []pgtype.UUID{projectID})
	if err != nil {
		return 0, 0, 0, err
	}
	if len(rows) > 0 {
		resourceCount = rows[0].ResourceCount
	}
	return issueCount, doneCount, resourceCount, nil
}

func writeProjectSummaryError(w http.ResponseWriter, r *http.Request, err error) {
	if writeClientClosedIfCanceled(w, err) {
		return
	}
	slog.Error("load project summary failed", append(logger.RequestAttrs(r), "error", err)...)
	writeError(w, http.StatusInternalServerError, "failed to load project summary")
}

type CreateProjectRequest struct {
	Title       string                         `json:"title"`
	Description *string                        `json:"description"`
	Icon        *string                        `json:"icon"`
	Status      string                         `json:"status"`
	Priority    string                         `json:"priority"`
	LeadType    *string                        `json:"lead_type"`
	LeadID      *string                        `json:"lead_id"`
	Resources   []CreateProjectResourceRequest `json:"resources,omitempty"`
}

type createProjectResponse struct {
	projectResponse
	Resources []ProjectResourceResponse `json:"resources,omitempty"`
}

type UpdateProjectRequest struct {
	Title       *string `json:"title"`
	Description *string `json:"description"`
	Icon        *string `json:"icon"`
	Status      *string `json:"status"`
	Priority    *string `json:"priority"`
	LeadType    *string `json:"lead_type"`
	LeadID      *string `json:"lead_id"`
}

func (h *Handler) ListProjects(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	var statusFilter pgtype.Text
	if s := r.URL.Query().Get("status"); s != "" {
		statusFilter = pgtype.Text{String: s, Valid: true}
	}
	var priorityFilter pgtype.Text
	if p := r.URL.Query().Get("priority"); p != "" {
		priorityFilter = pgtype.Text{String: p, Valid: true}
	}
	projects, err := h.Queries.ListProjects(r.Context(), db.ListProjectsParams{
		WorkspaceID: wsUUID,
		Status:      statusFilter,
		Priority:    priorityFilter,
	})
	if err != nil {
		if writeClientClosedIfCanceled(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to list projects")
		return
	}

	// Batch-fetch issue stats and resource counts for all projects
	statsMap := make(map[string]db.GetProjectIssueStatsRow)
	resourceCountMap := make(map[string]int64)
	if len(projects) > 0 {
		projectIDs := make([]pgtype.UUID, len(projects))
		for i, p := range projects {
			projectIDs[i] = p.ID
		}
		stats, err := h.Queries.GetProjectIssueStats(r.Context(), projectIDs)
		if err != nil {
			writeProjectSummaryError(w, r, err)
			return
		}
		for _, s := range stats {
			statsMap[uuidToString(s.ProjectID)] = s
		}
		counts, err := h.Queries.GetProjectResourceCounts(r.Context(), projectIDs)
		if err != nil {
			writeProjectSummaryError(w, r, err)
			return
		}
		for _, c := range counts {
			resourceCountMap[uuidToString(c.ProjectID)] = c.ResourceCount
		}
	}

	resp := make([]projectResponse, len(projects))
	for i, p := range projects {
		resp[i] = projectToResponse(p)
		if s, ok := statsMap[resp[i].ID]; ok {
			resp[i].IssueCount = s.TotalCount
			resp[i].DoneCount = s.DoneCount
		}
		resp[i].ResourceCount = resourceCountMap[resp[i].ID]
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": resp, "total": len(resp)})
}

func (h *Handler) GetProject(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)
	project, ok := h.loadProjectForRequest(w, r, id, workspaceID)
	if !ok {
		return
	}
	issueCount, doneCount, resourceCount, err := h.loadProjectCounts(r.Context(), project.ID)
	if err != nil {
		writeProjectSummaryError(w, r, err)
		return
	}
	resp := projectToResponse(project)
	resp.IssueCount = issueCount
	resp.DoneCount = doneCount
	resp.ResourceCount = resourceCount
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) loadProjectForRequest(w http.ResponseWriter, r *http.Request, id, workspaceID string) (db.Project, bool) {
	idUUID, ok := parseUUIDOrBadRequest(w, id, "project id")
	if !ok {
		return db.Project{}, false
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return db.Project{}, false
	}
	project, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{
		ID: idUUID, WorkspaceID: wsUUID,
	})
	if err != nil {
		writeEntityLoadError(w, err, "project", "project_id", id)
		return db.Project{}, false
	}
	return project, true
}

// validProjectStatuses / validProjectPriorities mirror the CHECK constraints on
// the project table. CreateProject / UpdateProject
// pre-validate against these so an unknown enum value returns a clean 400 with
// the allowed list instead of surfacing the DB CHECK violation as a 500 — the
// exact mismatch reported in #3925 (`--status active`).
var validProjectStatuses = []string{"planned", "in_progress", "paused", "completed", "cancelled"}
var validProjectPriorities = []string{"urgent", "high", "medium", "low", "none"}

// validateProjectEnum writes a 400 and returns false when value is not in
// allowed; the caller returns immediately on false.
func validateProjectEnum(w http.ResponseWriter, field, value string, allowed []string) bool {
	for _, a := range allowed {
		if value == a {
			return true
		}
	}
	writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid %s %q; valid values: %s", field, value, strings.Join(allowed, ", ")))
	return false
}

// writeProjectWriteError maps a failed project INSERT/UPDATE to an HTTP
// response. A CHECK constraint violation is a client error (400) — pre-validation
// already covers status/priority, so this backstops any other constrained column
// (e.g. lead_type). Anything else is a genuine server fault: log the underlying
// error so transient DB failures are diagnosable (#3925 had no server-side
// signal) and return 500.
func (h *Handler) writeProjectWriteError(w http.ResponseWriter, r *http.Request, err error, action string) {
	if isCheckViolation(err) {
		writeError(w, http.StatusBadRequest, "project "+action+" rejected: a field value failed a database constraint")
		return
	}
	slog.Error("project "+action+" failed", append(logger.RequestAttrs(r), "error", err)...)
	writeError(w, http.StatusInternalServerError, "failed to "+action+" project")
}

func (h *Handler) CreateProject(w http.ResponseWriter, r *http.Request) {
	var req CreateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Title == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	status := req.Status
	if status == "" {
		status = "planned"
	}
	if !validateProjectEnum(w, "status", status, validProjectStatuses) {
		return
	}
	priority := req.Priority
	if priority == "" {
		priority = "none"
	}
	if !validateProjectEnum(w, "priority", priority, validProjectPriorities) {
		return
	}
	var leadType pgtype.Text
	var leadID pgtype.UUID
	if req.LeadType != nil {
		leadType = pgtype.Text{String: *req.LeadType, Valid: true}
	}
	if req.LeadID != nil {
		id, ok := parseUUIDOrBadRequest(w, *req.LeadID, "lead_id")
		if !ok {
			return
		}
		leadID = id
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	// Pre-validate every resource payload before opening a transaction so an
	// invalid ref produces a clean 400 with no DB work. For local_directory we
	// also enforce one row per daemon_id within the batch — the daemon-side
	// resolver picks the first match by daemon_id, so two rows on the same
	// daemon would silently route the agent into whichever sorts first.
	// The standalone POST/PUT paths run the same check via
	// findLocalDirectoryConflict; this loop just covers the bundled-create
	// surface, where there is no existing row to compare against yet.
	normalizedRefs := make([]json.RawMessage, len(req.Resources))
	localDirSeen := map[string]int{}
	for i, res := range req.Resources {
		res.ResourceType = strings.TrimSpace(res.ResourceType)
		if res.ResourceType == "" {
			writeError(w, http.StatusBadRequest, "resources[].resource_type is required")
			return
		}
		ref, err := validateAndNormalizeResourceRef(res.ResourceType, res.ResourceRef)
		if err != nil {
			writeError(w, http.StatusBadRequest, "resources["+strconv.Itoa(i)+"]: "+err.Error())
			return
		}
		normalizedRefs[i] = ref
		req.Resources[i].ResourceType = res.ResourceType
		req.Resources[i].ResourceRef = ref
		if res.ResourceType == "local_directory" {
			var ld localDirectoryRef
			if err := json.Unmarshal(ref, &ld); err != nil {
				writeError(w, http.StatusBadRequest, "resources["+strconv.Itoa(i)+"]: "+err.Error())
				return
			}
			if prev, ok := localDirSeen[ld.DaemonID]; ok {
				writeError(w, http.StatusBadRequest, "resources["+strconv.Itoa(i)+"]: duplicate local_directory for daemon (already at index "+strconv.Itoa(prev)+"); each daemon may attach at most one local_directory per project")
				return
			}
			localDirSeen[ld.DaemonID] = i
		}
	}
	req.Status = status
	req.Priority = priority
	requestHash, err := hashRequestFingerprint(req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create project")
		return
	}
	idempotencyKey, ok := requireIdempotencyKey(w, r)
	if !ok {
		return
	}
	actorID := parseUUID(userID)
	writeReplayError := resourceCreateReplayErrorWriter(
		"Idempotency-Key was already used with a different request",
		"failed to load project request",
	)
	loadReplay := func() (createProjectResponse, bool, error) {
		return loadResourceCreateReplay(
			r.Context(), h.Queries, wsUUID, actorID, resourceTypeProject,
			idempotencyKey, requestHash,
			func(response createProjectResponse) bool { return response.ID != "" },
		)
	}
	if handleResourceCreateReplay(w, http.StatusCreated, loadReplay, writeReplayError) {
		return
	}
	for i, res := range req.Resources {
		if err := h.ensureGongfengProjectPathRegistered(
			r.Context(), wsUUID, strings.TrimSpace(res.ResourceType), normalizedRefs[i],
		); err != nil {
			writeError(w, http.StatusBadRequest, "resources["+strconv.Itoa(i)+"]: "+err.Error())
			return
		}
	}

	createParams := db.CreateProjectParams{
		WorkspaceID: wsUUID,
		Title:       req.Title,
		Description: ptrToText(req.Description),
		Icon:        ptrToText(req.Icon),
		Status:      status,
		LeadType:    leadType,
		LeadID:      leadID,
		Priority:    priority,
	}

	// Project, optional resources, and the exact replay response share one
	// transaction. This is deliberately the only create path: a response lost
	// after commit can be retried without creating a duplicate Project.
	tx, qtx, ok := h.beginResourceCreateTransaction(w, r.Context(), "failed to start transaction")
	if !ok {
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	err = reserveResourceCreateRequest(r.Context(), qtx, wsUUID, actorID, resourceTypeProject, idempotencyKey, requestHash)
	if !handleResourceCreateReservation(
		w, r.Context(), tx, err, loadReplay,
		writeReplayError,
		"failed to reserve project request",
		http.StatusCreated,
	) {
		return
	}
	project, err := qtx.CreateProject(r.Context(), createParams)
	if err != nil {
		h.writeProjectWriteError(w, r, err, "create")
		return
	}

	creator, _ := h.parseUserUUIDOrZero(userID)
	resourceRows := make([]db.ProjectResource, 0, len(req.Resources))
	for i, res := range req.Resources {
		var label pgtype.Text
		if res.Label != nil && strings.TrimSpace(*res.Label) != "" {
			label = pgtype.Text{String: strings.TrimSpace(*res.Label), Valid: true}
		}
		position := int32(i)
		if res.Position != nil {
			position = *res.Position
		}
		row, err := qtx.CreateProjectResource(r.Context(), db.CreateProjectResourceParams{
			ProjectID:    project.ID,
			WorkspaceID:  project.WorkspaceID,
			ResourceType: res.ResourceType,
			ResourceRef:  normalizedRefs[i],
			Label:        label,
			Position:     position,
			CreatedBy:    creator,
		})
		if err != nil {
			if isUniqueViolation(err) {
				writeError(w, http.StatusConflict, "resources["+strconv.Itoa(i)+"]: this resource is already attached")
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to attach resource at index "+strconv.Itoa(i))
			return
		}
		resourceRows = append(resourceRows, row)
	}
	resourceResp := make([]ProjectResourceResponse, len(resourceRows))
	for i, row := range resourceRows {
		resourceResp[i] = projectResourceToResponse(row)
	}
	resp := projectToResponse(project)
	resp.ResourceCount = int64(len(resourceResp))
	createResp := createProjectResponse{projectResponse: resp}
	if len(resourceResp) > 0 {
		createResp.Resources = resourceResp
	}
	if err := completeResourceCreateRequest(
		r.Context(), qtx, wsUUID, actorID, resourceTypeProject,
		idempotencyKey, requestHash, project.ID, createResp,
	); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to complete project request")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit project create")
		return
	}

	h.publish(protocol.EventProjectCreated, workspaceID, "member", userID, map[string]any{"project": resp})
	for _, rr := range resourceResp {
		h.publish(protocol.EventProjectResourceCreated, workspaceID, "member", userID, map[string]any{
			"resource":   rr,
			"project_id": resp.ID,
		})
	}
	writeJSON(w, http.StatusCreated, createResp)
}

func (h *Handler) UpdateProject(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)
	prevProject, ok := h.loadProjectForRequest(w, r, id, workspaceID)
	if !ok {
		return
	}
	issueCount, doneCount, resourceCount, err := h.loadProjectCounts(r.Context(), prevProject.ID)
	if err != nil {
		writeProjectSummaryError(w, r, err)
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	var req UpdateProjectRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal(bodyBytes, &rawFields); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	params := db.UpdateProjectParams{
		ID:          prevProject.ID,
		Description: prevProject.Description,
		Icon:        prevProject.Icon,
		LeadType:    prevProject.LeadType,
		LeadID:      prevProject.LeadID,
	}
	if req.Title != nil {
		params.Title = pgtype.Text{String: *req.Title, Valid: true}
	}
	if req.Status != nil {
		if !validateProjectEnum(w, "status", *req.Status, validProjectStatuses) {
			return
		}
		params.Status = pgtype.Text{String: *req.Status, Valid: true}
	}
	if req.Priority != nil {
		if !validateProjectEnum(w, "priority", *req.Priority, validProjectPriorities) {
			return
		}
		params.Priority = pgtype.Text{String: *req.Priority, Valid: true}
	}
	if _, ok := rawFields["description"]; ok {
		if req.Description != nil {
			params.Description = pgtype.Text{String: *req.Description, Valid: true}
		} else {
			params.Description = pgtype.Text{Valid: false}
		}
	}
	if _, ok := rawFields["icon"]; ok {
		if req.Icon != nil {
			params.Icon = pgtype.Text{String: *req.Icon, Valid: true}
		} else {
			params.Icon = pgtype.Text{Valid: false}
		}
	}
	if _, ok := rawFields["lead_type"]; ok {
		if req.LeadType != nil {
			params.LeadType = pgtype.Text{String: *req.LeadType, Valid: true}
		} else {
			params.LeadType = pgtype.Text{Valid: false}
		}
	}
	if _, ok := rawFields["lead_id"]; ok {
		if req.LeadID != nil {
			leadUUID, ok := parseUUIDOrBadRequest(w, *req.LeadID, "lead_id")
			if !ok {
				return
			}
			params.LeadID = leadUUID
		} else {
			params.LeadID = pgtype.UUID{Valid: false}
		}
	}
	project, err := h.Queries.UpdateProject(r.Context(), params)
	if err != nil {
		h.writeProjectWriteError(w, r, err, "update")
		return
	}
	resp := projectToResponse(project)
	resp.IssueCount = issueCount
	resp.DoneCount = doneCount
	resp.ResourceCount = resourceCount
	h.publish(protocol.EventProjectUpdated, workspaceID, "member", userID, map[string]any{"project": resp})
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) DeleteProject(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)
	project, ok := h.loadProjectForRequest(w, r, id, workspaceID)
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	if err := h.Queries.DeleteProject(r.Context(), db.DeleteProjectParams{
		ID:          project.ID,
		WorkspaceID: project.WorkspaceID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete project")
		return
	}
	h.publish(protocol.EventProjectDeleted, workspaceID, "member", userID, map[string]any{"project_id": uuidToString(project.ID)})
	w.WriteHeader(http.StatusNoContent)
}

// SearchProjectResponse extends projectResponse with search metadata.
type SearchProjectResponse struct {
	projectResponse
	MatchSource    string  `json:"match_source"`
	MatchedSnippet *string `json:"matched_snippet,omitempty"`
}

// buildProjectSearchQuery builds a dynamic SQL query for project search.
func buildProjectSearchQuery(phrase string, terms []string, includeClosed bool) (string, []any) {
	var args dynamicQueryArgs
	patterns := addSearchQueryPatterns(&args, phrase, terms)
	phraseParam := patterns.exact
	phraseContains := patterns.contains
	phraseStartsWith := patterns.starts
	wsParam := patterns.workspace
	termParams := patterns.terms

	// --- WHERE clause ---
	var whereParts []string

	// Full phrase match: title or description
	phraseMatch := fmt.Sprintf(
		"(LOWER(p.title) LIKE %s OR LOWER(COALESCE(p.description, '')) LIKE %s)",
		phraseContains, phraseContains,
	)
	whereParts = append(whereParts, phraseMatch)

	// Multi-word AND match
	if len(termParams) > 1 {
		var termConditions []string
		for _, tp := range termParams {
			termConditions = append(termConditions, fmt.Sprintf(
				"(LOWER(p.title) LIKE %s OR LOWER(COALESCE(p.description, '')) LIKE %s)",
				tp, tp,
			))
		}
		whereParts = append(whereParts, "("+strings.Join(termConditions, " AND ")+")")
	}

	whereClause := "(" + strings.Join(whereParts, " OR ") + ")"

	if !includeClosed {
		whereClause += " AND p.status NOT IN ('completed', 'cancelled')"
	}

	// --- ORDER BY ranking ---
	var rankCases []string

	// Tier 0: Exact title match
	rankCases = append(rankCases, fmt.Sprintf("WHEN LOWER(p.title) = %s THEN 0", phraseParam))

	// Tier 1: Title starts with phrase
	rankCases = append(rankCases, fmt.Sprintf("WHEN LOWER(p.title) LIKE %s THEN 1", phraseStartsWith))

	// Tier 2: Title contains phrase
	rankCases = append(rankCases, fmt.Sprintf("WHEN LOWER(p.title) LIKE %s THEN 2", phraseContains))

	// Tier 3: Title matches all words (multi-word only)
	if len(termParams) > 1 {
		var titleTerms []string
		for _, tp := range termParams {
			titleTerms = append(titleTerms, fmt.Sprintf("LOWER(p.title) LIKE %s", tp))
		}
		rankCases = append(rankCases, fmt.Sprintf("WHEN (%s) THEN 3", strings.Join(titleTerms, " AND ")))
	}

	// Tier 4: Description contains phrase
	rankCases = append(rankCases, fmt.Sprintf("WHEN LOWER(COALESCE(p.description, '')) LIKE %s THEN 4", phraseContains))

	rankExpr := "CASE " + strings.Join(rankCases, " ") + " ELSE 5 END"

	// --- match_source expression ---
	matchSourceExpr := fmt.Sprintf(`CASE
		WHEN LOWER(p.title) LIKE %s THEN 'title'
		ELSE 'description'
	END`, phraseContains)

	if len(termParams) > 1 {
		var titleTerms []string
		for _, tp := range termParams {
			titleTerms = append(titleTerms, fmt.Sprintf("LOWER(p.title) LIKE %s", tp))
		}
		matchSourceExpr = fmt.Sprintf(`CASE
			WHEN LOWER(p.title) LIKE %s THEN 'title'
			WHEN (%s) THEN 'title'
			ELSE 'description'
		END`,
			phraseContains, strings.Join(titleTerms, " AND "),
		)
	}

	limitParam := args.add(nil)
	offsetParam := args.add(nil)

	query := fmt.Sprintf(`SELECT p.id, p.workspace_id, p.title, p.description, p.icon,
		p.status, p.priority, p.lead_type, p.lead_id,
		p.created_at, p.updated_at,
		COUNT(*) OVER() AS total_count,
		%s AS match_source
	FROM project p
	WHERE p.workspace_id = %s AND %s
	ORDER BY %s, p.updated_at DESC
	LIMIT %s OFFSET %s`,
		matchSourceExpr,
		wsParam,
		whereClause,
		rankExpr,
		limitParam,
		offsetParam,
	)

	return query, args
}

func (h *Handler) SearchProjects(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	query, workspaceUUID, options, ok := h.parseSearchRequest(w, r)
	if !ok {
		return
	}
	terms := splitSearchTerms(query)

	sqlQuery, args := buildProjectSearchQuery(query, terms, options.includeClosed)
	args[1] = workspaceUUID
	args[len(args)-2] = options.limit
	args[len(args)-1] = options.offset

	rows, err := h.DB.Query(ctx, sqlQuery, args...)
	if err != nil {
		slog.Warn("search projects failed", "error", err, "workspace_id", uuidToString(workspaceUUID), "query", query)
		writeError(w, http.StatusInternalServerError, "failed to search projects")
		return
	}
	defer rows.Close()

	type projectSearchRow struct {
		project     db.Project
		totalCount  int64
		matchSource string
	}

	var results []projectSearchRow
	for rows.Next() {
		var row projectSearchRow
		if err := rows.Scan(
			&row.project.ID,
			&row.project.WorkspaceID,
			&row.project.Title,
			&row.project.Description,
			&row.project.Icon,
			&row.project.Status,
			&row.project.Priority,
			&row.project.LeadType,
			&row.project.LeadID,
			&row.project.CreatedAt,
			&row.project.UpdatedAt,
			&row.totalCount,
			&row.matchSource,
		); err != nil {
			slog.Warn("search projects scan failed", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to search projects")
			return
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("search projects rows error", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to search projects")
		return
	}

	var total int64
	if len(results) > 0 {
		total = results[0].totalCount
	}

	// Batch-fetch issue stats and resource counts
	statsMap := make(map[string]db.GetProjectIssueStatsRow)
	resourceCountMap := make(map[string]int64)
	if len(results) > 0 {
		projectIDs := make([]pgtype.UUID, len(results))
		for i, r := range results {
			projectIDs[i] = r.project.ID
		}
		stats, err := h.Queries.GetProjectIssueStats(ctx, projectIDs)
		if err == nil {
			for _, s := range stats {
				statsMap[uuidToString(s.ProjectID)] = s
			}
		}
		counts, err := h.Queries.GetProjectResourceCounts(ctx, projectIDs)
		if err == nil {
			for _, c := range counts {
				resourceCountMap[uuidToString(c.ProjectID)] = c.ResourceCount
			}
		}
	}

	resp := make([]SearchProjectResponse, len(results))
	for i, row := range results {
		pr := projectToResponse(row.project)
		if s, ok := statsMap[pr.ID]; ok {
			pr.IssueCount = s.TotalCount
			pr.DoneCount = s.DoneCount
		}
		pr.ResourceCount = resourceCountMap[pr.ID]
		spr := SearchProjectResponse{
			projectResponse: pr,
			MatchSource:     row.matchSource,
		}
		if row.matchSource == "description" {
			desc := ""
			if row.project.Description.Valid {
				desc = row.project.Description.String
			}
			if desc != "" {
				snippet := extractSnippet(desc, query)
				spr.MatchedSnippet = &snippet
			}
		}
		resp[i] = spr
	}

	w.Header().Set("X-Total-Count", strconv.FormatInt(total, 10))
	writeJSON(w, http.StatusOK, map[string]any{
		"projects": resp,
		"total":    total,
	})
}
