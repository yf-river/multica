package handler

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) SearchIssues(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	workspaceID := h.resolveWorkspaceID(r)

	q := r.URL.Query().Get("q")
	if q == "" {
		writeError(w, http.StatusBadRequest, "q parameter is required")
		return
	}

	limit := 20
	offset := 0
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = v
		}
	}
	if limit > 50 {
		limit = 50
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if v, err := strconv.Atoi(o); err == nil && v >= 0 {
			offset = v
		}
	}

	includeClosed := r.URL.Query().Get("include_closed") == "true"

	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	terms := splitSearchTerms(q)
	queryNum, hasNum := parseQueryNumber(q)

	sqlQuery, args := buildSearchQuery(q, terms, queryNum, hasNum, includeClosed)
	// Fill placeholder args: $4 = workspace_id, last two = limit, offset
	args[3] = wsUUID
	args[len(args)-2] = limit
	args[len(args)-1] = offset

	rows, err := h.DB.Query(ctx, sqlQuery, args...)
	if err != nil {
		slog.Warn("search issues failed", "error", err, "workspace_id", workspaceID, "query", q)
		writeError(w, http.StatusInternalServerError, "failed to search issues")
		return
	}
	defer rows.Close()

	var results []searchResult
	for rows.Next() {
		var sr searchResult
		if err := rows.Scan(
			&sr.issue.ID,
			&sr.issue.WorkspaceID,
			&sr.issue.Title,
			&sr.issue.Description,
			&sr.issue.Status,
			&sr.issue.Priority,
			&sr.issue.AssigneeType,
			&sr.issue.AssigneeID,
			&sr.issue.CreatorType,
			&sr.issue.CreatorID,
			&sr.issue.ParentIssueID,
			&sr.issue.AcceptanceCriteria,
			&sr.issue.ContextRefs,
			&sr.issue.Position,
			&sr.issue.StartDate,
			&sr.issue.DueDate,
			&sr.issue.CreatedAt,
			&sr.issue.UpdatedAt,
			&sr.issue.Number,
			&sr.issue.ProjectID,
			&sr.totalCount,
			&sr.matchSource,
			&sr.matchedCommentContent,
		); err != nil {
			slog.Warn("search issues scan failed", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to search issues")
			return
		}
		results = append(results, sr)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("search issues rows error", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to search issues")
		return
	}

	var total int64
	if len(results) > 0 {
		total = results[0].totalCount
	}

	prefix := h.getIssuePrefix(ctx, wsUUID)
	resp := make([]SearchIssueResponse, len(results))
	for i, sr := range results {
		sir := SearchIssueResponse{
			IssueResponse: issueToResponse(sr.issue, prefix),
			MatchSource:   sr.matchSource,
		}
		// Always populate comment snippet when a matching comment exists
		if sr.matchedCommentContent != "" {
			snippet := extractSnippet(sr.matchedCommentContent, q)
			sir.MatchedCommentSnippet = &snippet
			// Keep backward compat: also set MatchedSnippet for comment-source matches
			if sr.matchSource == "comment" {
				sir.MatchedSnippet = &snippet
			}
		}
		// Populate description snippet when description matches
		if sr.matchSource == "description" || descriptionContains(sr.issue.Description, q, terms) {
			if sr.issue.Description.Valid && sr.issue.Description.String != "" {
				snippet := extractSnippet(sr.issue.Description.String, q)
				sir.MatchedDescriptionSnippet = &snippet
			}
		}
		resp[i] = sir
	}

	w.Header().Set("X-Total-Count", strconv.FormatInt(total, 10))
	writeJSON(w, http.StatusOK, map[string]any{
		"issues": resp,
		"total":  total,
	})
}

func (h *Handler) ListIssues(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	// Parse optional filter params. Malformed UUIDs in filters return 400 —
	// silently coercing them to a zero UUID would mask a client bug and let
	// the query return an empty result set (or worse, match a NULL row).
	var priorityFilter pgtype.Text
	if p := r.URL.Query().Get("priority"); p != "" {
		priorityFilter = pgtype.Text{String: p, Valid: true}
	}
	var assigneeFilter pgtype.UUID
	if a := r.URL.Query().Get("assignee_id"); a != "" {
		id, ok := parseUUIDOrBadRequest(w, a, "assignee_id")
		if !ok {
			return
		}
		assigneeFilter = id
	}
	var assigneeIdsFilter []pgtype.UUID
	if ids := r.URL.Query().Get("assignee_ids"); ids != "" {
		for _, raw := range strings.Split(ids, ",") {
			if s := strings.TrimSpace(raw); s != "" {
				id, ok := parseUUIDOrBadRequest(w, s, "assignee_ids")
				if !ok {
					return
				}
				assigneeIdsFilter = append(assigneeIdsFilter, id)
			}
		}
	}
	var creatorFilter pgtype.UUID
	if c := r.URL.Query().Get("creator_id"); c != "" {
		id, ok := parseUUIDOrBadRequest(w, c, "creator_id")
		if !ok {
			return
		}
		creatorFilter = id
	}
	var projectFilter pgtype.UUID
	if p := r.URL.Query().Get("project_id"); p != "" {
		id, ok := parseUUIDOrBadRequest(w, p, "project_id")
		if !ok {
			return
		}
		projectFilter = id
	}
	// involves_user_id widens the assignee filter to surface issues where the
	// user is the indirect assignee (their owned agent, or a squad they belong
	// to / lead / have an agent inside). Direct member-assignment is excluded
	// by design — that is the meaning of `assignee_id` (tab 1), and tab 3 must
	// be disjoint from tab 1.
	var involvesUserFilter pgtype.UUID
	if u := r.URL.Query().Get("involves_user_id"); u != "" {
		id, ok := parseUUIDOrBadRequest(w, u, "involves_user_id")
		if !ok {
			return
		}
		involvesUserFilter = id
	}

	metadataFilter, ok := parseMetadataFilterParam(w, r.URL.Query().Get("metadata"))
	if !ok {
		return
	}
	dateFilter, ok := parseIssueDateFilter(w, r.URL.Query())
	if !ok {
		return
	}

	// open_only=true returns all non-done/cancelled issues (no limit).
	if r.URL.Query().Get("open_only") == "true" {
		issues, err := h.Queries.ListOpenIssues(ctx, db.ListOpenIssuesParams{
			WorkspaceID:    wsUUID,
			Priority:       priorityFilter,
			AssigneeID:     assigneeFilter,
			AssigneeIds:    assigneeIdsFilter,
			CreatorID:      creatorFilter,
			ProjectID:      projectFilter,
			InvolvesUserID: involvesUserFilter,
			MetadataFilter: metadataFilter,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list issues")
			return
		}

		prefix := h.getIssuePrefix(ctx, wsUUID)
		ids := make([]pgtype.UUID, len(issues))
		for i, issue := range issues {
			ids[i] = issue.ID
		}
		labelsMap := h.labelsByIssue(ctx, wsUUID, ids)
		resp := make([]IssueResponse, len(issues))
		for i, issue := range issues {
			resp[i] = openIssueRowToResponse(issue, prefix)
			labels := labelsMap[resp[i].ID]
			if labels == nil {
				labels = []LabelResponse{}
			}
			resp[i].Labels = &labels
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"issues": resp,
			"total":  len(resp),
		})
		return
	}

	limit := 100
	offset := 0
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = v
		}
	}
	if limit > 100 {
		limit = 100
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if v, err := strconv.Atoi(o); err == nil && v >= 0 {
			offset = v
		}
	}

	var statusFilter pgtype.Text
	if s := r.URL.Query().Get("status"); s != "" {
		statusFilter = pgtype.Text{String: s, Valid: true}
	}

	// scheduled=true restricts the result to issues that have at least one of
	// start_date / due_date set. Used by the Project Gantt view, which only
	// renders schedulable rows and shouldn't pay for the full project list.
	var scheduledFilter pgtype.Bool
	if r.URL.Query().Get("scheduled") == "true" {
		scheduledFilter = pgtype.Bool{Bool: true, Valid: true}
	}

	// Parse sort and direction params for dynamic ORDER BY.
	// Manual sort (position) is always ASC — direction is ignored because
	// the user defines order through drag-and-drop, reversing it has no
	// product meaning.
	sortCol := "position"
	if s := r.URL.Query().Get("sort"); s != "" {
		switch s {
		case "position", "title", "created_at", "start_date", "due_date":
			sortCol = s
		case "priority":
			sortCol = "CASE i.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 ELSE 4 END"
		default:
			writeError(w, http.StatusBadRequest, "invalid sort value")
			return
		}
	}
	sortDir := "ASC"
	if sortCol != "position" {
		if d := r.URL.Query().Get("direction"); d != "" {
			switch strings.ToLower(d) {
			case "asc":
				sortDir = "ASC"
			case "desc":
				sortDir = "DESC"
			default:
				writeError(w, http.StatusBadRequest, "invalid direction value")
				return
			}
		}
	}

	// Build dynamic SQL — same approach as ListGroupedIssues.
	where := []string{"i.workspace_id = $1"}
	args := []any{wsUUID}
	addArg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}

	if statusFilter.Valid {
		where = append(where, fmt.Sprintf("i.status = %s", addArg(statusFilter.String)))
	}
	if priorityFilter.Valid {
		where = append(where, fmt.Sprintf("i.priority = %s", addArg(priorityFilter.String)))
	}
	if assigneeFilter.Valid {
		where = append(where, fmt.Sprintf("i.assignee_id = %s::uuid", addArg(assigneeFilter)))
	}
	if len(assigneeIdsFilter) > 0 {
		where = append(where, fmt.Sprintf("i.assignee_id = ANY(%s::uuid[])", addArg(assigneeIdsFilter)))
	}
	if creatorFilter.Valid {
		where = append(where, fmt.Sprintf("i.creator_id = %s::uuid", addArg(creatorFilter)))
	}
	if projectFilter.Valid {
		where = append(where, fmt.Sprintf("i.project_id = %s::uuid", addArg(projectFilter)))
	}
	if scheduledFilter.Valid {
		where = append(where, "(i.start_date IS NOT NULL OR i.due_date IS NOT NULL)")
	}
	if metadataFilter != nil {
		where = append(where, fmt.Sprintf("i.metadata @> %s::jsonb", addArg(string(metadataFilter))))
	}
	where = appendIssueDateFilter(where, addArg, dateFilter)
	if involvesUserFilter.Valid {
		ref := addArg(involvesUserFilter)
		where = append(where, fmt.Sprintf(`(
    (i.assignee_type = 'agent' AND i.assignee_id IN (
       SELECT a.id FROM agent a
        WHERE a.workspace_id = $1
          AND a.owner_id     = %[1]s::uuid
    ))
    OR (i.assignee_type = 'squad' AND i.assignee_id IN (
       SELECT sm.squad_id
         FROM squad_member sm
         JOIN squad s ON s.id = sm.squad_id
        WHERE s.workspace_id = $1
          AND sm.member_type = 'member'
          AND sm.member_id   = %[1]s::uuid
       UNION
       SELECT s.id
         FROM squad s
         JOIN agent a ON a.id = s.leader_id
        WHERE s.workspace_id = $1
          AND a.workspace_id = $1
          AND a.owner_id     = %[1]s::uuid
       UNION
       SELECT sm.squad_id
         FROM squad_member sm
         JOIN squad s ON s.id = sm.squad_id
         JOIN agent a ON a.id = sm.member_id
        WHERE s.workspace_id = $1
          AND sm.member_type = 'agent'
          AND a.workspace_id = $1
          AND a.owner_id     = %[1]s::uuid
    ))
)`, ref))
	}

	whereSql := strings.Join(where, " AND ")
	countArgs := append([]any(nil), args...)

	// Build ORDER BY clause.
	orderBy := sortCol
	if !strings.HasPrefix(sortCol, "CASE") {
		orderBy = "i." + sortCol
	}
	orderBy += " " + sortDir
	if sortCol == "start_date" || sortCol == "due_date" {
		orderBy += " NULLS LAST"
	}
	orderBy += ", i.created_at DESC"

	visibleAgentIDs, ok := h.visibleAgentUUIDsForIssueList(w, r, workspaceID)
	if !ok {
		return
	}
	visibleAgentIDsRef := addArg(visibleAgentIDs)
	offsetRef := addArg(int64(offset))
	limitRef := addArg(int64(limit))

	query := fmt.Sprintf(`SELECT %s
FROM issue i
%s
WHERE %s
ORDER BY %s
LIMIT %s OFFSET %s`, issueListSelectSQL, issueListJoinSQL(visibleAgentIDsRef), whereSql, orderBy, limitRef, offsetRef)

	rows, err := h.DB.Query(ctx, query, args...)
	if err != nil {
		slog.Warn("ListIssues query failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list issues")
		return
	}
	defer rows.Close()

	var issues []issueListRow
	for rows.Next() {
		var row issueListRow
		if err := scanIssueListRow(rows, &row); err != nil {
			slog.Warn("ListIssues scan failed", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to list issues")
			return
		}
		issues = append(issues, row)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("ListIssues rows failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list issues")
		return
	}

	// Get the true total count for pagination awareness.
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM issue i WHERE %s`, whereSql)
	var total int64
	if err := h.DB.QueryRow(ctx, countQuery, countArgs...).Scan(&total); err != nil {
		total = int64(len(issues))
	}

	prefix := h.getIssuePrefix(ctx, wsUUID)
	ids := make([]pgtype.UUID, len(issues))
	for i, issue := range issues {
		ids[i] = issue.ID
	}
	labelsMap := h.labelsByIssue(ctx, wsUUID, ids)
	resp := make([]IssueResponse, len(issues))
	for i, issue := range issues {
		resp[i] = issueListRowWithSummaryToResponse(issue, prefix)
		labels := labelsMap[resp[i].ID]
		if labels == nil {
			labels = []LabelResponse{}
		}
		resp[i].Labels = &labels
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"issues": resp,
		"total":  total,
	})
}

func (h *Handler) ListIssueBuckets(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if h.DB == nil {
		writeError(w, http.StatusInternalServerError, "database is unavailable")
		return
	}

	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	statuses := splitCommaParam(r.URL.Query().Get("statuses"))
	if len(statuses) == 0 {
		statuses = validIssueStatuses
	}
	for _, status := range statuses {
		if !slices.Contains(validIssueStatuses, status) {
			writeError(w, http.StatusBadRequest, "invalid statuses")
			return
		}
	}

	limit := 50
	offset := 0
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = v
		}
	}
	if limit > 100 {
		limit = 100
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if v, err := strconv.Atoi(o); err == nil && v >= 0 {
			offset = v
		}
	}

	where := []string{"i.workspace_id = $1", "i.status = ANY($2::text[])"}
	args := []any{wsUUID, statuses}
	addArg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}

	if p := r.URL.Query().Get("priority"); p != "" {
		where = append(where, fmt.Sprintf("i.priority = %s", addArg(p)))
	}
	if raw := r.URL.Query().Get("assignee_id"); raw != "" {
		id, ok := parseUUIDOrBadRequest(w, raw, "assignee_id")
		if !ok {
			return
		}
		where = append(where, fmt.Sprintf("i.assignee_id = %s::uuid", addArg(id)))
	}
	if raw := r.URL.Query().Get("assignee_ids"); raw != "" {
		ids, ok := parseUUIDParamList(w, raw, "assignee_ids")
		if !ok {
			return
		}
		if len(ids) > 0 {
			where = append(where, fmt.Sprintf("i.assignee_id = ANY(%s::uuid[])", addArg(ids)))
		}
	}
	if raw := r.URL.Query().Get("creator_id"); raw != "" {
		id, ok := parseUUIDOrBadRequest(w, raw, "creator_id")
		if !ok {
			return
		}
		where = append(where, fmt.Sprintf("i.creator_id = %s::uuid", addArg(id)))
	}
	if raw := r.URL.Query().Get("project_id"); raw != "" {
		id, ok := parseUUIDOrBadRequest(w, raw, "project_id")
		if !ok {
			return
		}
		where = append(where, fmt.Sprintf("i.project_id = %s::uuid", addArg(id)))
	}
	if raw := r.URL.Query().Get("involves_user_id"); raw != "" {
		id, ok := parseUUIDOrBadRequest(w, raw, "involves_user_id")
		if !ok {
			return
		}
		ref := addArg(id)
		where = append(where, fmt.Sprintf(`(
    (i.assignee_type = 'agent' AND i.assignee_id IN (
       SELECT a.id FROM agent a
        WHERE a.workspace_id = $1
          AND a.owner_id     = %[1]s::uuid
    ))
    OR (i.assignee_type = 'squad' AND i.assignee_id IN (
       SELECT sm.squad_id
         FROM squad_member sm
         JOIN squad s ON s.id = sm.squad_id
        WHERE s.workspace_id = $1
          AND sm.member_type = 'member'
          AND sm.member_id   = %[1]s::uuid
       UNION
       SELECT s.id
         FROM squad s
         JOIN agent a ON a.id = s.leader_id
        WHERE s.workspace_id = $1
          AND a.workspace_id = $1
          AND a.owner_id     = %[1]s::uuid
       UNION
       SELECT sm.squad_id
         FROM squad_member sm
         JOIN squad s ON s.id = sm.squad_id
         JOIN agent a ON a.id = sm.member_id
        WHERE s.workspace_id = $1
          AND sm.member_type = 'agent'
          AND a.workspace_id = $1
          AND a.owner_id     = %[1]s::uuid
    ))
)`, ref))
	}
	metadataFilter, ok := parseMetadataFilterParam(w, r.URL.Query().Get("metadata"))
	if !ok {
		return
	}
	if metadataFilter != nil {
		where = append(where, fmt.Sprintf("i.metadata @> %s::jsonb", addArg(string(metadataFilter))))
	}
	dateFilter, ok := parseIssueDateFilter(w, r.URL.Query())
	if !ok {
		return
	}
	where = appendIssueDateFilter(where, addArg, dateFilter)

	sortCol := "position"
	if s := r.URL.Query().Get("sort"); s != "" {
		switch s {
		case "position", "title", "created_at", "start_date", "due_date":
			sortCol = s
		case "priority":
			sortCol = "CASE i.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 ELSE 4 END"
		default:
			writeError(w, http.StatusBadRequest, "invalid sort value")
			return
		}
	}
	sortDir := "ASC"
	if sortCol != "position" {
		if d := r.URL.Query().Get("direction"); d != "" {
			switch strings.ToLower(d) {
			case "asc":
				sortDir = "ASC"
			case "desc":
				sortDir = "DESC"
			default:
				writeError(w, http.StatusBadRequest, "invalid direction value")
				return
			}
		}
	}
	orderBy := sortCol
	if !strings.HasPrefix(sortCol, "CASE") {
		orderBy = "i." + sortCol
	}
	orderBy += " " + sortDir
	if sortCol == "start_date" || sortCol == "due_date" {
		orderBy += " NULLS LAST"
	}
	orderBy += ", i.created_at DESC"

	whereSQL := strings.Join(where, " AND ")
	visibleAgentIDs, ok := h.visibleAgentUUIDsForIssueList(w, r, workspaceID)
	if !ok {
		return
	}
	visibleAgentIDsRef := addArg(visibleAgentIDs)
	offsetRef := addArg(int64(offset))
	limitRef := addArg(int64(limit))
	query := fmt.Sprintf(`WITH ranked AS (
  SELECT %s,
         COUNT(*) OVER (PARTITION BY i.status) AS status_total,
         ROW_NUMBER() OVER (PARTITION BY i.status ORDER BY %s) AS status_row_number
    FROM issue i
    %s
   WHERE %s
)
SELECT id, workspace_id, title, description, status, priority,
       assignee_type, assignee_id, creator_type, creator_id,
       parent_issue_id, position, start_date, due_date, created_at, updated_at, number, project_id, metadata,
       assignee_name, assignee_avatar_url, project_title, project_icon, child_done, child_total,
       agent_running_count, agent_queued_count, agent_ids, status_total
  FROM ranked
 WHERE status_row_number > %s
   AND status_row_number <= (%s + %s)
 ORDER BY status, status_row_number`, issueListSelectSQL, orderBy, issueListJoinSQL(visibleAgentIDsRef), whereSQL, offsetRef, offsetRef, limitRef)

	rows, err := h.DB.Query(ctx, query, args...)
	if err != nil {
		slog.Warn("ListIssueBuckets query failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list issue buckets")
		return
	}
	defer rows.Close()

	bucketRows := []issueBucketRow{}
	for rows.Next() {
		var row issueBucketRow
		base := issueListRow{ListIssuesRow: row.ListIssuesRow, Summary: row.Summary}
		dest := append(issueListScanDest(&base), &row.StatusTotal)
		if err := rows.Scan(dest...); err != nil {
			slog.Warn("ListIssueBuckets scan failed", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to list issue buckets")
			return
		}
		row.ListIssuesRow = base.ListIssuesRow
		row.Summary = base.Summary
		bucketRows = append(bucketRows, row)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("ListIssueBuckets rows failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list issue buckets")
		return
	}

	ids := make([]pgtype.UUID, len(bucketRows))
	for i, row := range bucketRows {
		ids[i] = row.ID
	}
	prefix := h.getIssuePrefix(ctx, wsUUID)
	labelsMap := h.labelsByIssue(ctx, wsUUID, ids)
	byStatus := make(map[string]IssueStatusBucketResponse, len(statuses))
	for _, status := range statuses {
		byStatus[status] = IssueStatusBucketResponse{Issues: []IssueResponse{}, Total: 0}
	}
	for _, row := range bucketRows {
		status := row.Status
		issue := issueListRowWithSummaryToResponse(issueListRow{
			ListIssuesRow: row.ListIssuesRow,
			Summary:       row.Summary,
		}, prefix)
		labels := labelsMap[issue.ID]
		if labels == nil {
			labels = []LabelResponse{}
		}
		issue.Labels = &labels
		bucket := byStatus[status]
		bucket.Issues = append(bucket.Issues, issue)
		bucket.Total = row.StatusTotal
		byStatus[status] = bucket
	}

	writeJSON(w, http.StatusOK, ListIssueBucketsResponse{ByStatus: byStatus})
}

type issueActorFilter struct {
	actorType string
	actorID   pgtype.UUID
}

type issueDateFilter struct {
	column string
	start  time.Time
	end    time.Time
}

func parseIssueDateFilter(w http.ResponseWriter, values url.Values) (*issueDateFilter, bool) {
	field := strings.TrimSpace(values.Get("date_field"))
	startRaw := strings.TrimSpace(values.Get("date_start"))
	endRaw := strings.TrimSpace(values.Get("date_end"))
	if field == "" && startRaw == "" && endRaw == "" {
		return nil, true
	}
	if field == "" || startRaw == "" || endRaw == "" {
		writeError(w, http.StatusBadRequest, "date_field, date_start, and date_end are required together")
		return nil, false
	}

	column := ""
	switch field {
	case "created_at":
		column = "created_at"
	case "updated_at":
		column = "updated_at"
	default:
		writeError(w, http.StatusBadRequest, "invalid date_field")
		return nil, false
	}

	start, err := time.Parse(time.RFC3339Nano, startRaw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid date_start")
		return nil, false
	}
	end, err := time.Parse(time.RFC3339Nano, endRaw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid date_end")
		return nil, false
	}
	if !start.Before(end) {
		writeError(w, http.StatusBadRequest, "date_start must be before date_end")
		return nil, false
	}

	return &issueDateFilter{column: column, start: start, end: end}, true
}

func appendIssueDateFilter(where []string, addArg func(any) string, filter *issueDateFilter) []string {
	if filter == nil {
		return where
	}
	startRef := addArg(filter.start)
	endRef := addArg(filter.end)
	return append(where, fmt.Sprintf(
		"i.%s >= %s AND i.%s < %s",
		filter.column,
		startRef,
		filter.column,
		endRef,
	))
}

func splitCommaParam(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func isIssueActorType(s string) bool {
	return s == "member" || s == "agent" || s == "squad"
}

func parseUUIDParamList(w http.ResponseWriter, raw, fieldName string) ([]pgtype.UUID, bool) {
	parts := splitCommaParam(raw)
	if len(parts) == 0 {
		return nil, true
	}
	ids := make([]pgtype.UUID, 0, len(parts))
	for _, part := range parts {
		id, ok := parseUUIDOrBadRequest(w, part, fieldName)
		if !ok {
			return nil, false
		}
		ids = append(ids, id)
	}
	return ids, true
}

func parseActorFilterList(w http.ResponseWriter, raw, fieldName string) ([]issueActorFilter, bool) {
	parts := splitCommaParam(raw)
	if len(parts) == 0 {
		return nil, true
	}
	filters := make([]issueActorFilter, 0, len(parts))
	for _, part := range parts {
		pieces := strings.SplitN(part, ":", 2)
		if len(pieces) != 2 || !isIssueActorType(pieces[0]) || strings.TrimSpace(pieces[1]) == "" {
			writeError(w, http.StatusBadRequest, "invalid "+fieldName)
			return nil, false
		}
		id, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(pieces[1]), fieldName)
		if !ok {
			return nil, false
		}
		filters = append(filters, issueActorFilter{
			actorType: pieces[0],
			actorID:   id,
		})
	}
	return filters, true
}

func (h *Handler) ListGroupedIssues(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if h.DB == nil {
		writeError(w, http.StatusInternalServerError, "database is unavailable")
		return
	}

	groupBy := r.URL.Query().Get("group_by")
	if groupBy == "" {
		groupBy = "assignee"
	}
	if groupBy != "assignee" {
		writeError(w, http.StatusBadRequest, "unsupported group_by")
		return
	}

	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	limit := 50
	offset := 0
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = v
		}
	}
	if limit > 100 {
		limit = 100
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if v, err := strconv.Atoi(o); err == nil && v > 0 {
			offset = v
		}
	}

	where := []string{"i.workspace_id = $1"}
	args := []any{wsUUID}
	addArg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}

	statuses := splitCommaParam(r.URL.Query().Get("statuses"))
	if len(statuses) == 0 {
		statuses = splitCommaParam(r.URL.Query().Get("status"))
	}
	if len(statuses) > 0 {
		where = append(where, fmt.Sprintf("i.status = ANY(%s::text[])", addArg(statuses)))
	}

	priorities := splitCommaParam(r.URL.Query().Get("priorities"))
	if len(priorities) == 0 {
		priorities = splitCommaParam(r.URL.Query().Get("priority"))
	}
	if len(priorities) > 0 {
		where = append(where, fmt.Sprintf("i.priority = ANY(%s::text[])", addArg(priorities)))
	}

	assigneeTypes := splitCommaParam(r.URL.Query().Get("assignee_types"))
	if len(assigneeTypes) > 0 {
		for _, assigneeType := range assigneeTypes {
			if !isIssueActorType(assigneeType) {
				writeError(w, http.StatusBadRequest, "invalid assignee_types")
				return
			}
		}
		where = append(where, fmt.Sprintf("i.assignee_type = ANY(%s::text[])", addArg(assigneeTypes)))
	}

	if raw := r.URL.Query().Get("assignee_id"); raw != "" {
		id, ok := parseUUIDOrBadRequest(w, raw, "assignee_id")
		if !ok {
			return
		}
		where = append(where, fmt.Sprintf("i.assignee_id = %s::uuid", addArg(id)))
	}
	if raw := r.URL.Query().Get("assignee_ids"); raw != "" {
		ids, ok := parseUUIDParamList(w, raw, "assignee_ids")
		if !ok {
			return
		}
		if len(ids) > 0 {
			where = append(where, fmt.Sprintf("i.assignee_id = ANY(%s::uuid[])", addArg(ids)))
		}
	}
	if raw := r.URL.Query().Get("creator_id"); raw != "" {
		id, ok := parseUUIDOrBadRequest(w, raw, "creator_id")
		if !ok {
			return
		}
		where = append(where, fmt.Sprintf("i.creator_id = %s::uuid", addArg(id)))
	}
	if raw := r.URL.Query().Get("project_id"); raw != "" {
		id, ok := parseUUIDOrBadRequest(w, raw, "project_id")
		if !ok {
			return
		}
		where = append(where, fmt.Sprintf("i.project_id = %s::uuid", addArg(id)))
	}
	if filter, ok := parseMetadataFilterParam(w, r.URL.Query().Get("metadata")); !ok {
		return
	} else if filter != nil {
		where = append(where, fmt.Sprintf("i.metadata @> %s::jsonb", addArg(string(filter))))
	}
	// Mirror the involves_user_id 4-branch UNION from sqlc's ListIssues /
	// ListOpenIssues / CountIssues. ListGroupedIssues is a hand-written dynamic
	// SQL builder that does not share parameters with sqlc, so the fragment is
	// re-implemented here in lock-step. Member-direct assignment is excluded by
	// design: that semantics belongs to tab 1 (`assignee_id`), and tab 3 must
	// stay disjoint from tab 1.
	if raw := r.URL.Query().Get("involves_user_id"); raw != "" {
		id, ok := parseUUIDOrBadRequest(w, raw, "involves_user_id")
		if !ok {
			return
		}
		ref := addArg(id)
		where = append(where, fmt.Sprintf(`(
    (i.assignee_type = 'agent' AND i.assignee_id IN (
       SELECT a.id FROM agent a
        WHERE a.workspace_id = $1
          AND a.owner_id     = %[1]s::uuid
    ))
    OR (i.assignee_type = 'squad' AND i.assignee_id IN (
       SELECT sm.squad_id
         FROM squad_member sm
         JOIN squad s ON s.id = sm.squad_id
        WHERE s.workspace_id = $1
          AND sm.member_type = 'member'
          AND sm.member_id   = %[1]s::uuid
       UNION
       SELECT s.id
         FROM squad s
         JOIN agent a ON a.id = s.leader_id
        WHERE s.workspace_id = $1
          AND a.workspace_id = $1
          AND a.owner_id     = %[1]s::uuid
       UNION
       SELECT sm.squad_id
         FROM squad_member sm
         JOIN squad s ON s.id = sm.squad_id
         JOIN agent a ON a.id = sm.member_id
        WHERE s.workspace_id = $1
          AND sm.member_type = 'agent'
          AND a.workspace_id = $1
          AND a.owner_id     = %[1]s::uuid
    ))
)`, ref))
	}

	assigneeFilters, ok := parseActorFilterList(w, r.URL.Query().Get("assignee_filters"), "assignee_filters")
	if !ok {
		return
	}
	includeNoAssignee := r.URL.Query().Get("include_no_assignee") == "true"
	if len(assigneeFilters) > 0 || includeNoAssignee {
		ors := make([]string, 0, len(assigneeFilters)+1)
		for _, filter := range assigneeFilters {
			ors = append(ors, fmt.Sprintf(
				"(i.assignee_type = %s::text AND i.assignee_id = %s::uuid)",
				addArg(filter.actorType),
				addArg(filter.actorID),
			))
		}
		if includeNoAssignee {
			ors = append(ors, "(i.assignee_type IS NULL AND i.assignee_id IS NULL)")
		}
		where = append(where, "("+strings.Join(ors, " OR ")+")")
	}

	creatorFilters, ok := parseActorFilterList(w, r.URL.Query().Get("creator_filters"), "creator_filters")
	if !ok {
		return
	}
	if len(creatorFilters) > 0 {
		ors := make([]string, 0, len(creatorFilters))
		for _, filter := range creatorFilters {
			ors = append(ors, fmt.Sprintf(
				"(i.creator_type = %s::text AND i.creator_id = %s::uuid)",
				addArg(filter.actorType),
				addArg(filter.actorID),
			))
		}
		where = append(where, "("+strings.Join(ors, " OR ")+")")
	}

	projectIDs, ok := parseUUIDParamList(w, r.URL.Query().Get("project_ids"), "project_ids")
	if !ok {
		return
	}
	includeNoProject := r.URL.Query().Get("include_no_project") == "true"
	if len(projectIDs) > 0 || includeNoProject {
		ors := make([]string, 0, 2)
		if len(projectIDs) > 0 {
			ors = append(ors, fmt.Sprintf("i.project_id = ANY(%s::uuid[])", addArg(projectIDs)))
		}
		if includeNoProject {
			ors = append(ors, "i.project_id IS NULL")
		}
		where = append(where, "("+strings.Join(ors, " OR ")+")")
	}

	labelIDs, ok := parseUUIDParamList(w, r.URL.Query().Get("label_ids"), "label_ids")
	if !ok {
		return
	}
	if len(labelIDs) > 0 {
		where = append(where, fmt.Sprintf(
			"EXISTS (SELECT 1 FROM issue_to_label itl WHERE itl.issue_id = i.id AND itl.label_id = ANY(%s::uuid[]))",
			addArg(labelIDs),
		))
	}

	dateFilter, ok := parseIssueDateFilter(w, r.URL.Query())
	if !ok {
		return
	}
	where = appendIssueDateFilter(where, addArg, dateFilter)

	if groupAssigneeType := r.URL.Query().Get("group_assignee_type"); groupAssigneeType != "" {
		if groupAssigneeType == "none" {
			where = append(where, "(i.assignee_type IS NULL AND i.assignee_id IS NULL)")
		} else {
			if !isIssueActorType(groupAssigneeType) {
				writeError(w, http.StatusBadRequest, "invalid group_assignee_type")
				return
			}
			rawID := r.URL.Query().Get("group_assignee_id")
			if rawID == "" {
				writeError(w, http.StatusBadRequest, "invalid group_assignee_id")
				return
			}
			assigneeID, ok := parseUUIDOrBadRequest(w, rawID, "group_assignee_id")
			if !ok {
				return
			}
			where = append(where, fmt.Sprintf(
				"(i.assignee_type = %s::text AND i.assignee_id = %s::uuid)",
				addArg(groupAssigneeType),
				addArg(assigneeID),
			))
		}
	}

	sortCol := "position"
	if s := r.URL.Query().Get("sort"); s != "" {
		switch s {
		case "position", "title", "created_at", "start_date", "due_date":
			sortCol = s
		case "priority":
			sortCol = "CASE i.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 ELSE 4 END"
		default:
			writeError(w, http.StatusBadRequest, "invalid sort value")
			return
		}
	}
	sortDir := "ASC"
	if sortCol != "position" {
		if d := r.URL.Query().Get("direction"); d != "" {
			switch strings.ToLower(d) {
			case "asc":
				sortDir = "ASC"
			case "desc":
				sortDir = "DESC"
			default:
				writeError(w, http.StatusBadRequest, "invalid direction value")
				return
			}
		}
	}

	intraGroupOrder := sortCol
	if !strings.HasPrefix(sortCol, "CASE") {
		intraGroupOrder = "i." + sortCol
	}
	intraGroupOrder += " " + sortDir
	if sortCol == "start_date" || sortCol == "due_date" {
		intraGroupOrder += " NULLS LAST"
	}
	intraGroupOrder += ", i.created_at DESC"

	offsetRef := addArg(int64(offset))
	limitRef := addArg(int64(limit))
	visibleAgentIDs, ok := h.visibleAgentUUIDsForIssueList(w, r, workspaceID)
	if !ok {
		return
	}
	visibleAgentIDsRef := addArg(visibleAgentIDs)
	query := fmt.Sprintf(`
WITH ranked AS (
	SELECT
		%s,
		COUNT(*) OVER (PARTITION BY i.assignee_type, i.assignee_id) AS group_total,
		ROW_NUMBER() OVER (
			PARTITION BY i.assignee_type, i.assignee_id
			ORDER BY %s
		) AS rn
	FROM issue i
	%s
	WHERE %s
)
SELECT
	id, workspace_id, title, description, status, priority,
	assignee_type, assignee_id, creator_type, creator_id,
	parent_issue_id, position, start_date, due_date, created_at, updated_at,
	number, project_id, metadata,
	assignee_name, assignee_avatar_url, project_title, project_icon, child_done, child_total,
	agent_running_count, agent_queued_count, agent_ids, group_total
FROM ranked
WHERE rn > %s AND rn <= %s + %s
ORDER BY
	CASE assignee_type
		WHEN 'member' THEN 0
		WHEN 'agent' THEN 1
		WHEN 'squad' THEN 2
		ELSE 3
	END,
	assignee_type NULLS LAST,
	assignee_id NULLS LAST,
	rn`, issueListSelectSQL, intraGroupOrder, issueListJoinSQL(visibleAgentIDsRef), strings.Join(where, " AND "), offsetRef, offsetRef, limitRef)

	rows, err := h.DB.Query(ctx, query, args...)
	if err != nil {
		slog.Warn("ListGroupedIssues query failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list grouped issues")
		return
	}
	defer rows.Close()

	groupedRows := []groupedIssueRow{}
	for rows.Next() {
		var row groupedIssueRow
		base := issueListRow{ListIssuesRow: row.ListIssuesRow, Summary: row.Summary}
		dest := append(issueListScanDest(&base), &row.GroupTotal)
		if err := rows.Scan(dest...); err != nil {
			slog.Warn("ListGroupedIssues scan failed", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to list grouped issues")
			return
		}
		row.ListIssuesRow = base.ListIssuesRow
		row.Summary = base.Summary
		groupedRows = append(groupedRows, row)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("ListGroupedIssues rows failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list grouped issues")
		return
	}

	ids := make([]pgtype.UUID, len(groupedRows))
	for i, row := range groupedRows {
		ids[i] = row.ID
	}
	labelsMap := h.labelsByIssue(ctx, wsUUID, ids)
	prefix := h.getIssuePrefix(ctx, wsUUID)

	groups := []IssueAssigneeGroupResponse{}
	groupIndex := map[string]int{}
	for _, row := range groupedRows {
		groupID := assigneeGroupID(row.AssigneeType, row.AssigneeID)
		idx, exists := groupIndex[groupID]
		if !exists {
			idx = len(groups)
			groupIndex[groupID] = idx
			groups = append(groups, IssueAssigneeGroupResponse{
				ID:           groupID,
				AssigneeType: textToPtr(row.AssigneeType),
				AssigneeID:   uuidToPtr(row.AssigneeID),
				Issues:       []IssueResponse{},
				Total:        row.GroupTotal,
			})
		}

		issue := issueListRowWithSummaryToResponse(issueListRow{
			ListIssuesRow: row.ListIssuesRow,
			Summary:       row.Summary,
		}, prefix)
		labels := labelsMap[issue.ID]
		if labels == nil {
			labels = []LabelResponse{}
		}
		issue.Labels = &labels
		groups[idx].Issues = append(groups[idx].Issues, issue)
	}

	writeJSON(w, http.StatusOK, GroupedIssuesResponse{Groups: groups})
}

