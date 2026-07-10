package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// IssueResponse is the JSON response for an issue.
type IssueResponse struct {
	ID              string  `json:"id"`
	WorkspaceID     string  `json:"workspace_id"`
	Number          int32   `json:"number"`
	Identifier      string  `json:"identifier"`
	Title           string  `json:"title"`
	Description     *string `json:"description"`
	Status          string  `json:"status"`
	Priority        string  `json:"priority"`
	AssigneeType    *string `json:"assignee_type"`
	AssigneeID      *string `json:"assignee_id"`
	CreatorType     string  `json:"creator_type"`
	CreatorID       string  `json:"creator_id"`
	ParentIssueID   *string `json:"parent_issue_id"`
	ProjectID       *string `json:"project_id"`
	Position        float64 `json:"position"`
	StartDate       *string `json:"start_date"`
	DueDate         *string `json:"due_date"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
	WorkStartedAt   *string `json:"work_started_at,omitempty"`
	WorkCompletedAt *string `json:"work_completed_at,omitempty"`
	// Metadata is the per-issue KV map (see issue_metadata.go). Always emitted
	// (empty object when unset) so frontend code can `issue.metadata[key]`
	// without nil-guarding the parent field.
	Metadata    map[string]any          `json:"metadata"`
	Reactions   []IssueReactionResponse `json:"reactions,omitempty"`
	Attachments []AttachmentResponse    `json:"attachments,omitempty"`
	// Labels are bulk-attached by list/detail endpoints so the client can render
	// chips without an N+1 round-trip per row. Pointer + omitempty so paths that
	// don't load labels (e.g. UpdateIssue, batch UpdateIssues, the issue:updated
	// WS broadcast) emit no `labels` field at all — the client merge then
	// preserves whatever labels are already in cache. nil pointer = "field
	// absent, do not touch"; non-nil (incl. empty slice) = authoritative list.
	Labels *[]LabelResponse `json:"labels,omitempty"`
	// Summary fields let list/board cards render their first screen without
	// fetching full member/agent/squad/project directories. Detail hovers still
	// lazy-load the authoritative profile when opened.
	Assignee *IssueActorSummaryResponse   `json:"assignee,omitempty"`
	Project  *IssueProjectSummaryResponse `json:"project,omitempty"`
	// Present on list-style responses so issue cards can render sub-issue
	// progress without a workspace-wide child-progress request.
	ChildProgress *IssueChildProgressResponse `json:"child_progress,omitempty"`
	// Present on list-style responses so issue cards can render live agent
	// cues and the "working" filter without a workspace-wide task snapshot.
	AgentActivity *IssueAgentActivitySummaryResponse `json:"agent_activity,omitempty"`
}

type IssueActorSummaryResponse struct {
	Type      string  `json:"type"`
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	AvatarURL *string `json:"avatar_url"`
}

type IssueProjectSummaryResponse struct {
	ID    string  `json:"id"`
	Title string  `json:"title"`
	Icon  *string `json:"icon"`
}

type IssueChildProgressResponse struct {
	Done  int64 `json:"done"`
	Total int64 `json:"total"`
}

type IssueAgentActivitySummaryResponse struct {
	RunningCount int64    `json:"running_count"`
	QueuedCount  int64    `json:"queued_count"`
	AgentIDs     []string `json:"agent_ids"`
}

type IncompleteChildIssueResponse struct {
	ID           string  `json:"id"`
	Identifier   string  `json:"identifier"`
	Title        string  `json:"title"`
	Status       string  `json:"status"`
	AssigneeType *string `json:"assignee_type,omitempty"`
	AssigneeID   *string `json:"assignee_id,omitempty"`
	ProjectID    *string `json:"project_id,omitempty"`
}

// validIssueStatuses / validIssuePriorities mirror the CHECK constraints on
// the issue table. Write handlers pre-validate these so callers get a clean
// 400 with the allowed values instead of a database CHECK violation bubbling
// up as a 500.
var validIssueStatuses = []string{"backlog", "todo", "in_progress", "in_review", "done", "blocked", "cancelled"}
var validIssuePriorities = []string{"urgent", "high", "medium", "low", "none"}

var (
	tapdMarkdownWikiURLRE = regexp.MustCompile(`https?://www\.tapd\.cn/([0-9]+)/markdown_wikis/show/\s*#\s*([0-9]+)`)
	tapdProngStoryURLRE   = regexp.MustCompile(`https?://www\.tapd\.cn/([0-9]+)/prong/stories/view/([0-9]+)`)
	tapdStoryListURLRE    = regexp.MustCompile(`https?://www\.tapd\.cn/tapd_fe/([0-9]+)/story/list\?[^\s<>]*dialog_preview_id=story_([0-9]+)[^\s<>]*`)
	tapdStoryPreviewIDRE  = regexp.MustCompile(`^story_([0-9]+)$`)
)

type tapdWikiSourceRef struct {
	WorkspaceID string
	WikiID      string
	URL         string
}

type tapdSourceRef struct {
	WorkspaceID  string
	ResourceType string
	ResourceID   string
	URL          string
}



func (h *Handler) incompleteChildrenBlockingDone(ctx context.Context, issue db.Issue) ([]IncompleteChildIssueResponse, error) {
	children, err := h.Queries.ListChildIssues(ctx, issue.ID)
	if err != nil {
		return nil, err
	}
	return incompleteChildIssues(children, h.getIssuePrefix(ctx, issue.WorkspaceID)), nil
}

func (h *Handler) writeIssueDoneBlockedByChildren(w http.ResponseWriter, incomplete []IncompleteChildIssueResponse) {
	writeJSON(w, http.StatusConflict, map[string]any{
		"error":               "cannot mark issue done while child issues are not done",
		"code":                "child_issues_not_done",
		"incomplete_children": incomplete,
	})
}

func (h *Handler) issueDoneBlockedByMissingGongfengMR(ctx context.Context, issue db.Issue) (bool, error) {
	if !issue.ProjectID.Valid {
		return false, nil
	}
	resources, err := h.Queries.ListProjectResources(ctx, issue.ProjectID)
	if err != nil {
		return false, err
	}
	requiresMR := false
	for _, resource := range resources {
		if resource.ResourceType == "gongfeng_repo" {
			requiresMR = true
			break
		}
	}
	if !requiresMR {
		return false, nil
	}
	pullRequests, err := h.Queries.ListPullRequestsByIssue(ctx, issue.ID)
	if err != nil {
		return false, err
	}
	return len(pullRequests) == 0, nil
}

func (h *Handler) writeIssueDoneBlockedByMissingMR(w http.ResponseWriter) {
	writeJSON(w, http.StatusConflict, map[string]any{
		"error": "cannot mark Gongfeng-backed issue done without a linked MR",
		"code":  "missing_linked_mr",
	})
}

var (
	errIssueParentNotFound = errors.New("parent issue not found in this workspace")
	errIssueParentCycle    = errors.New("circular parent relationship detected")
)

func (h *Handler) validateIssueParentInWorkspace(ctx context.Context, issue db.Issue, parentID pgtype.UUID) error {
	if parentID == issue.ID {
		return errIssueParentCycle
	}
	seen := map[string]struct{}{}
	cursor := parentID
	for cursor.Valid {
		key := uuidToString(cursor)
		if _, exists := seen[key]; exists {
			return errIssueParentCycle
		}
		seen[key] = struct{}{}

		ancestor, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
			ID:          cursor,
			WorkspaceID: issue.WorkspaceID,
		})
		if err != nil || !ancestor.ID.Valid {
			return errIssueParentNotFound
		}
		if ancestor.ID == issue.ID {
			return errIssueParentCycle
		}
		cursor = ancestor.ParentIssueID
	}
	return nil
}

func (h *Handler) validateProjectInWorkspace(ctx context.Context, workspaceID, projectID pgtype.UUID) error {
	_, err := h.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{
		ID:          projectID,
		WorkspaceID: workspaceID,
	})
	return err
}


// issueListRowToResponse converts a list-query row (no description) to an IssueResponse.

// labelsByIssue bulk-loads labels for the given issue IDs and returns a map
// keyed by issue UUID string. On error or empty input, returns an empty map —
// label rendering is non-critical and we'd rather serve issues without labels
// than fail the whole list call.
func (h *Handler) labelsByIssue(ctx context.Context, wsUUID pgtype.UUID, issueIDs []pgtype.UUID) map[string][]LabelResponse {
	out := map[string][]LabelResponse{}
	if len(issueIDs) == 0 {
		return out
	}
	rows, err := h.Queries.ListLabelsForIssues(ctx, db.ListLabelsForIssuesParams{
		IssueIds:    issueIDs,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		slog.Warn("ListLabelsForIssues failed", "error", err)
		return out
	}
	for _, r := range rows {
		issueID := uuidToString(r.IssueID)
		out[issueID] = append(out[issueID], LabelResponse{
			ID:          uuidToString(r.ID),
			WorkspaceID: uuidToString(r.WorkspaceID),
			Name:        r.Name,
			Color:       r.Color,
			CreatedAt:   timestampToString(r.CreatedAt),
			UpdatedAt:   timestampToString(r.UpdatedAt),
		})
	}
	return out
}


type IssueAssigneeGroupResponse struct {
	ID           string          `json:"id"`
	AssigneeType *string         `json:"assignee_type"`
	AssigneeID   *string         `json:"assignee_id"`
	Issues       []IssueResponse `json:"issues"`
	Total        int64           `json:"total"`
}

type GroupedIssuesResponse struct {
	Groups []IssueAssigneeGroupResponse `json:"groups"`
}

type groupedIssueRow struct {
	db.ListIssuesRow
	GroupTotal int64
	Summary    issueListSummary
}

type IssueStatusBucketResponse struct {
	Issues []IssueResponse `json:"issues"`
	Total  int64           `json:"total"`
}

type ListIssueBucketsResponse struct {
	ByStatus map[string]IssueStatusBucketResponse `json:"by_status"`
}

type issueBucketRow struct {
	db.ListIssuesRow
	StatusTotal int64
	Summary     issueListSummary
}

type issueListRow struct {
	db.ListIssuesRow
	Summary issueListSummary
}

type issueListSummary struct {
	AssigneeName      pgtype.Text
	AssigneeAvatarURL pgtype.Text
	ProjectTitle      pgtype.Text
	ProjectIcon       pgtype.Text
	ChildDone         int64
	ChildTotal        int64
	AgentRunningCount int64
	AgentQueuedCount  int64
	AgentIDs          []pgtype.UUID
}

const issueListSelectSQL = `i.id, i.workspace_id, i.title, i.description, i.status, i.priority,
       i.assignee_type, i.assignee_id, i.creator_type, i.creator_id,
       i.parent_issue_id, i.position, i.start_date, i.due_date, i.created_at, i.updated_at, i.number, i.project_id, i.metadata,
       COALESCE(assignee_member.name, assignee_agent.name, assignee_squad.name) AS assignee_name,
       COALESCE(assignee_member.avatar_url, assignee_agent.avatar_url, assignee_squad.avatar_url) AS assignee_avatar_url,
       project.title AS project_title,
       project.icon AS project_icon,
       COALESCE(child_progress.child_done, 0)::bigint AS child_done,
       COALESCE(child_progress.child_total, 0)::bigint AS child_total,
       COALESCE(agent_activity.running_count, 0)::bigint AS agent_running_count,
       COALESCE(agent_activity.queued_count, 0)::bigint AS agent_queued_count,
       COALESCE(agent_activity.agent_ids, ARRAY[]::uuid[]) AS agent_ids`







func (h *Handler) visibleAgentUUIDsForIssueList(w http.ResponseWriter, r *http.Request, workspaceID string) ([]pgtype.UUID, bool) {
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return nil, false
	}
	actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
	allowed, err := h.accessibleAgentIDs(r.Context(), workspaceID, actorType, actorID, member.Role)
	if err != nil {
		if writeClientClosedIfCanceled(w, err) {
			return nil, false
		}
		writeError(w, http.StatusInternalServerError, "failed to resolve agent access")
		return nil, false
	}
	ids := make([]pgtype.UUID, 0, len(allowed))
	for id := range allowed {
		parsed, err := util.ParseUUID(id)
		if err == nil {
			ids = append(ids, parsed)
		}
	}
	return ids, true
}


// SearchIssueResponse extends IssueResponse with search metadata.
type SearchIssueResponse struct {
	IssueResponse
	MatchSource               string  `json:"match_source"`
	MatchedSnippet            *string `json:"matched_snippet,omitempty"`
	MatchedDescriptionSnippet *string `json:"matched_description_snippet,omitempty"`
	MatchedCommentSnippet     *string `json:"matched_comment_snippet,omitempty"`
}

// extractSnippet extracts a snippet of text around the first occurrence of query.
// Returns up to ~120 runes centered on the match. Uses rune-based slicing to
// avoid splitting multi-byte UTF-8 characters (important for CJK content).
// For multi-word queries, tries phrase match first; if not found, locates the
// earliest occurring individual term and centers the snippet around it.

// findRuneSubstring returns the index of needle in haystack, or -1 if not found.

// descriptionContains checks if the description text contains the search phrase or all terms.

// escapeLike escapes LIKE special characters (%, _, \) in user input.

// splitSearchTerms splits a query into individual search terms, filtering empty strings.

// identifierNumberRe matches patterns like "MUL-123" or "ABC-45".
var identifierNumberRe = regexp.MustCompile(`(?i)^[a-z]+-(\d+)$`)

// parseQueryNumber extracts an issue number from the query if it looks like
// an identifier (e.g. "MUL-123") or a bare number (e.g. "123").

// searchResult holds a raw row from the dynamic search query.
type searchResult struct {
	issue                 db.Issue
	totalCount            int64
	matchSource           string
	matchedCommentContent string
}

// buildSearchQuery builds a dynamic SQL query for issue search.
// It uses LOWER(column) LIKE for case-insensitive matching compatible with pg_bigm 1.2 GIN indexes.
// Search patterns are lowercased in Go to avoid redundant LOWER() on the pattern side in SQL.
// LIKE patterns are pre-built in Go (e.g. "%html%") so pg_bigm can extract bigrams from a single parameter value.

func (h *Handler) GetIssue(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, id)
	if !ok {
		return
	}
	prefix := h.getIssuePrefix(r.Context(), issue.WorkspaceID)
	resp := issueToResponse(issue, prefix)
	detailLabels := h.labelsByIssue(r.Context(), issue.WorkspaceID, []pgtype.UUID{issue.ID})[uuidToString(issue.ID)]
	if detailLabels == nil {
		detailLabels = []LabelResponse{}
	}
	resp.Labels = &detailLabels

	// Fetch issue reactions.
	reactions, err := h.Queries.ListIssueReactions(r.Context(), issue.ID)
	if err == nil && len(reactions) > 0 {
		resp.Reactions = make([]IssueReactionResponse, len(reactions))
		for i, rx := range reactions {
			resp.Reactions[i] = issueReactionToResponse(rx)
		}
	}

	// Fetch issue-level attachments.
	attachments, err := h.Queries.ListAttachmentsByIssue(r.Context(), db.ListAttachmentsByIssueParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err == nil && len(attachments) > 0 {
		resp.Attachments = make([]AttachmentResponse, len(attachments))
		for i, a := range attachments {
			resp.Attachments[i] = h.attachmentToResponse(a)
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) ListChildIssues(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, id)
	if !ok {
		return
	}
	children, err := h.Queries.ListChildIssues(r.Context(), issue.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list child issues")
		return
	}
	prefix := h.getIssuePrefix(r.Context(), issue.WorkspaceID)
	resp := make([]IssueResponse, len(children))
	for i, child := range children {
		resp[i] = issueToResponse(child, prefix)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"issues": resp,
	})
}

// Cap on the number of parents we'll fan-out children for in one request.
// Swimlane's visible-lane count is naturally bounded by what fits on screen
// (typically <= 50), but cap explicitly so a malicious caller can't ANY()
// across the whole workspace's issue set in a single round trip.
const listChildrenByParentsLimit = 200

// ListChildrenByParents returns the union of children for the
// provided parent ids. Replaces the N-call fan-out Swimlane would otherwise
// have to make on mount (one /issues/:id/children per visible parent lane).
//
// Workspace scope is enforced at the query level — any parent_id that doesn't
// belong to the caller's workspace simply yields zero children, so callers
// can't probe parents across workspace boundaries.
func (h *Handler) ListChildrenByParents(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	raw := r.URL.Query().Get("parent_ids")
	if raw == "" {
		// Empty input is a no-op response (not an error) — simplifies the
		// client which calls this unconditionally on Swimlane mount even
		// when there are zero visible parent lanes.
		writeJSON(w, http.StatusOK, map[string]any{"issues": []IssueResponse{}})
		return
	}

	parts := strings.Split(raw, ",")
	if len(parts) > listChildrenByParentsLimit {
		writeError(w, http.StatusBadRequest, "too many parent_ids")
		return
	}
	parentIDs := make([]pgtype.UUID, 0, len(parts))
	for _, s := range parts {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		id, ok := parseUUIDOrBadRequest(w, s, "parent_ids")
		if !ok {
			return
		}
		parentIDs = append(parentIDs, id)
	}
	if len(parentIDs) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"issues": []IssueResponse{}})
		return
	}

	children, err := h.Queries.ListChildrenByParents(r.Context(), db.ListChildrenByParentsParams{
		WorkspaceID: wsUUID,
		ParentIds:   parentIDs,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list child issues")
		return
	}
	prefix := h.getIssuePrefix(r.Context(), wsUUID)
	resp := make([]IssueResponse, len(children))
	for i, child := range children {
		resp[i] = issueToResponse(child, prefix)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"issues": resp,
	})
}

func (h *Handler) ChildIssueProgress(w http.ResponseWriter, r *http.Request) {
	wsID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, wsID, "workspace_id")
	if !ok {
		return
	}

	rows, err := h.Queries.ChildIssueProgress(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get child issue progress")
		return
	}

	type progressEntry struct {
		ParentIssueID string `json:"parent_issue_id"`
		Total         int64  `json:"total"`
		Done          int64  `json:"done"`
	}
	resp := make([]progressEntry, len(rows))
	for i, row := range rows {
		resp[i] = progressEntry{
			ParentIssueID: uuidToString(row.ParentIssueID),
			Total:         row.Total,
			Done:          row.Done,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"progress": resp,
	})
}

// QuickCreateIssueRequest is the body for POST /api/issues/quick-create. The
// user picks an actor (agent or squad) in the modal and types one line of
// natural language; the server validates the actor's reachability up front,
// queues a quick-create task, and returns 202 immediately. The agent
// translates the prompt into a `multica issue create` invocation in the
// background; success and failure both surface as inbox notifications to
// the requester.
//
// Exactly one of AgentID / SquadID is required. When SquadID is set, the
// task is enqueued against the squad's leader agent and the leader receives
// the same Operating Protocol briefing it would for an issue assigned to
// the squad, so it can choose to delegate to a squad member as usual.
//
// ProjectID is optional and lets the modal target a specific project so
// the agent's `multica issue create` invocation passes `--project <uuid>`
// instead of letting it default. The frontend remembers the user's last
// pick per workspace, so frequent users skip retyping "in project X".
//
// ParentIssueID is optional and is set by the "Add sub issue" entry point
// when the modal is opened from an existing issue. The agent passes it
// through as `--parent <uuid>` so the new issue is filed as a sub-issue,
// keeping the sub-issue intent of the entry point regardless of whether
// the user submits via manual or agent mode.
type QuickCreateIssueRequest struct {
	AgentID       string   `json:"agent_id,omitempty"`
	SquadID       string   `json:"squad_id,omitempty"`
	Prompt        string   `json:"prompt"`
	ProjectID     string   `json:"project_id,omitempty"`
	ParentIssueID string   `json:"parent_issue_id,omitempty"`
	Status        string   `json:"status,omitempty"`
	Priority      string   `json:"priority,omitempty"`
	AssigneeType  string   `json:"assignee_type,omitempty"`
	AssigneeID    string   `json:"assignee_id,omitempty"`
	StartDate     string   `json:"start_date,omitempty"`
	DueDate       string   `json:"due_date,omitempty"`
	AttachmentIDs []string `json:"attachment_ids,omitempty"`
}

// QuickCreateIssueResponse returns either a queued quick-create task or a
// directly-created source-backed issue when the server can materialize the
// source before dispatch.
type QuickCreateIssueResponse struct {
	TaskID            string `json:"task_id,omitempty"`
	IssueID           string `json:"issue_id,omitempty"`
	Identifier        string `json:"identifier,omitempty"`
	SourceFetchStatus string `json:"source_fetch_status,omitempty"`
}

func (h *Handler) CreateIssue(w http.ResponseWriter, r *http.Request) {
	var req CreateIssueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Title == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}

	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	// Get creator from context (set by auth middleware)
	creatorID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	status := req.Status
	if status == "" {
		status = "todo"
	}
	priority := req.Priority
	if priority == "" {
		priority = "none"
	}
	if !validateIssueEnum(w, "status", status, validIssueStatuses) {
		return
	}
	if !validateIssueEnum(w, "priority", priority, validIssuePriorities) {
		return
	}
	descriptionText := ""
	if req.Description != nil {
		descriptionText = *req.Description
	}
	req.Metadata = h.enrichIssueSourceMetadata(r.Context(), req.Metadata, creatorID, req.Title, descriptionText)
	metadata, err := validateIssueMetadataObject(req.Metadata)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	var assigneeType pgtype.Text
	var assigneeID pgtype.UUID
	if req.AssigneeType != nil {
		assigneeType = pgtype.Text{String: *req.AssigneeType, Valid: true}
	}
	if req.AssigneeID != nil {
		id, ok := parseUUIDOrBadRequest(w, *req.AssigneeID, "assignee_id")
		if !ok {
			return
		}
		assigneeID = id
	}

	if status, msg := h.validateAssigneePair(r.Context(), r, workspaceID, assigneeType, assigneeID); status != 0 {
		writeError(w, status, msg)
		return
	}

	var parentIssueID pgtype.UUID
	var projectID pgtype.UUID
	if req.ProjectID != nil {
		id, ok := parseUUIDOrBadRequest(w, *req.ProjectID, "project_id")
		if !ok {
			return
		}
		projectID = id
	}
	if req.ParentIssueID != nil {
		id, ok := parseUUIDOrBadRequest(w, *req.ParentIssueID, "parent_issue_id")
		if !ok {
			return
		}
		parentIssueID = id
	}
	// Cross-workspace parent / project existence is enforced inside
	// IssueService.Create (atomically with the create), so every entry
	// point — HTTP, Lark, future MCP — gets the same boundary check
	// without duplicating the lookup here.

	attachmentIDs, ok := parseUUIDSliceOrBadRequest(w, req.AttachmentIDs, "attachment_ids")
	if !ok {
		return
	}

	var startDate pgtype.Date
	if req.StartDate != nil && *req.StartDate != "" {
		d, err := util.ParseCalendarDate(*req.StartDate)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid start_date format, expected YYYY-MM-DD")
			return
		}
		startDate = d
	}

	var dueDate pgtype.Date
	if req.DueDate != nil && *req.DueDate != "" {
		d, err := util.ParseCalendarDate(*req.DueDate)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid due_date format, expected YYYY-MM-DD")
			return
		}
		dueDate = d
	}

	// Determine creator identity: agent (via X-Agent-ID header) or member.
	creatorType, actualCreatorID := h.resolveActor(r, creatorID, workspaceID)

	// Optional origin stamping (quick-create / autopilot). Only the
	// allowed origin types are accepted; anything else is rejected so a
	// rogue caller can't mint arbitrary origin labels. Both fields must
	// be provided together.
	var originType pgtype.Text
	var originID pgtype.UUID
	if req.OriginType != nil || req.OriginID != nil {
		if req.OriginType == nil || req.OriginID == nil {
			writeError(w, http.StatusBadRequest, "origin_type and origin_id must be provided together")
			return
		}
		switch *req.OriginType {
		case "quick_create":
			// Allowed — daemon CLI passes this through from a quick-create task.
		default:
			writeError(w, http.StatusBadRequest, "unsupported origin_type")
			return
		}
		oid, ok := parseUUIDOrBadRequest(w, *req.OriginID, "origin_id")
		if !ok {
			return
		}
		originType = pgtype.Text{String: *req.OriginType, Valid: true}
		originID = oid
	}

	// Prefix is workspace-level; pre-compute once so both the broadcast
	// payload builder and the HTTP response share the same value.
	prefix := h.getIssuePrefix(r.Context(), wsUUID)

	if originType.Valid && originID.Valid {
		existing, err := h.Queries.GetIssueByOrigin(r.Context(), db.GetIssueByOriginParams{
			WorkspaceID: wsUUID,
			OriginType:  originType,
			OriginID:    originID,
		})
		if err == nil {
			slog.Info("origin-stamped issue create reused existing issue",
				"issue_id", uuidToString(existing.ID),
				"origin_type", originType.String,
				"origin_id", uuidToString(originID),
				"workspace_id", workspaceID,
			)
			writeJSON(w, http.StatusOK, issueToResponse(existing, prefix))
			return
		}
		if !isNotFound(err) {
			slog.Warn("origin-stamped issue lookup failed", append(logger.RequestAttrs(r), "error", err, "workspace_id", workspaceID)...)
			writeError(w, http.StatusInternalServerError, "failed to check issue origin")
			return
		}
	}

	// Analytics agent ID: assignee agent when the issue is being assigned
	// to an agent, otherwise the creator agent for agent-authored issues.
	// Resolved here (not in the service) because creator identity is HTTP-side.
	analyticsAgentID := ""
	if assigneeType.Valid && assigneeType.String == "agent" {
		analyticsAgentID = uuidToString(assigneeID)
	}
	if creatorType == "agent" && analyticsAgentID == "" {
		analyticsAgentID = actualCreatorID
	}

	buildAttachmentResponses := func(atts []db.Attachment) []AttachmentResponse {
		if len(atts) == 0 {
			return nil
		}
		out := make([]AttachmentResponse, len(atts))
		for i, a := range atts {
			out[i] = h.attachmentToResponse(a)
		}
		return out
	}

	res, err := h.IssueService.Create(r.Context(), service.IssueCreateParams{
		WorkspaceID:   wsUUID,
		Title:         req.Title,
		Description:   ptrToText(req.Description),
		Status:        status,
		Priority:      priority,
		AssigneeType:  assigneeType,
		AssigneeID:    assigneeID,
		CreatorType:   creatorType,
		CreatorID:     parseUUID(actualCreatorID),
		ParentIssueID: parentIssueID,
		ProjectID:     projectID,
		StartDate:     startDate,
		DueDate:       dueDate,
		OriginType:    originType,
		OriginID:      originID,
		AttachmentIDs: attachmentIDs,
		Metadata:      metadata,
	}, service.IssueCreateOpts{
		ActorID:          actualCreatorID,
		AnalyticsAgentID: analyticsAgentID,
		Platform:         func() string { p, _, _ := middleware.ClientMetadataFromContext(r.Context()); return p }(),
		BroadcastPayload: func(issue db.Issue, atts []db.Attachment) map[string]any {
			payload := issueToResponse(issue, prefix)
			payload.Attachments = buildAttachmentResponses(atts)
			return map[string]any{"issue": payload}
		},
	})

	if errors.Is(err, service.ErrParentIssueNotFound) {
		writeError(w, http.StatusBadRequest, "parent issue not found in this workspace")
		return
	}
	if errors.Is(err, service.ErrProjectNotFound) {
		writeError(w, http.StatusBadRequest, "project not found in this workspace")
		return
	}
	if isCheckViolation(err) {
		writeError(w, http.StatusBadRequest, "metadata exceeds the 8KB size limit")
		return
	}
	if err != nil {
		slog.Warn("create issue failed", append(logger.RequestAttrs(r), "error", err, "workspace_id", workspaceID)...)
		writeError(w, http.StatusInternalServerError, "failed to create issue: "+err.Error())
		return
	}

	issue := res.Issue
	slog.Info("issue created", append(logger.RequestAttrs(r), "issue_id", uuidToString(issue.ID), "title", issue.Title, "status", issue.Status, "workspace_id", workspaceID)...)

	resp := issueToResponse(issue, prefix)
	resp.Attachments = buildAttachmentResponses(res.Attachments)
	writeJSON(w, http.StatusCreated, resp)
}

func (h *Handler) enrichIssueSourceMetadata(ctx context.Context, metadata map[string]json.RawMessage, creatorUserID string, texts ...string) map[string]json.RawMessage {
	metadata = enrichTAPDSourceMetadataFromText(metadata, texts...)
	return h.enrichSourceCredentialMetadata(ctx, metadata, creatorUserID)
}








func (h *Handler) enrichSourceCredentialMetadata(ctx context.Context, metadata map[string]json.RawMessage, creatorUserID string) map[string]json.RawMessage {
	provider, ok := metadataString(metadata, "source_provider")
	if !ok || provider != externalCredentialProviderTAPD {
		return metadata
	}
	out := make(map[string]json.RawMessage, len(metadata)+8)
	for key, value := range metadata {
		out[key] = value
	}
	set := func(key, value string) {
		raw, _ := json.Marshal(value)
		out[key] = raw
	}
	set("source_fetch_provider", "tapd_mcp")
	set("source_credential_scope", "account")
	set("source_credential_inheritance", "task_creator_or_trigger_user")
	profile, err := h.Queries.GetDefaultExternalCredentialProfileForUser(ctx, db.GetDefaultExternalCredentialProfileForUserParams{
		UserID:   parseUUID(creatorUserID),
		Provider: externalCredentialProviderTAPD,
	})
	if err != nil {
		set("source_fetch_status", "blocked_missing_profile")
		set("source_fetch_error", "no account-level TAPD credential profile for task creator")
		return out
	}
	set("source_credential_profile_id", uuidToString(profile.ID))
	set("source_credential_profile_name", profile.Name)
	set("source_credential_profile_status", profile.Status)
	set("source_fetch_status", "pending_mcp_fetch")
	return out
}


type UpdateIssueRequest struct {
	Title         *string  `json:"title"`
	Description   *string  `json:"description"`
	Status        *string  `json:"status"`
	Priority      *string  `json:"priority"`
	AssigneeType  *string  `json:"assignee_type"`
	AssigneeID    *string  `json:"assignee_id"`
	Position      *float64 `json:"position"`
	StartDate     *string  `json:"start_date"`
	DueDate       *string  `json:"due_date"`
	ParentIssueID *string  `json:"parent_issue_id"`
	ProjectID     *string  `json:"project_id"`
	// AttachmentIDs lets the description editor bind newly uploaded files to
	// this issue so they surface in `GET /api/issues/:id/attachments` and the
	// editor's preview Eye keeps working past a refresh. Existing bindings
	// are idempotent — re-sending the same id is a no-op.
	AttachmentIDs []string `json:"attachment_ids"`
}

func (h *Handler) UpdateIssue(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	prevIssue, ok := h.loadIssueForUser(w, r, id)
	if !ok {
		return
	}
	userID := requestUserID(r)
	workspaceID := uuidToString(prevIssue.WorkspaceID)

	// Read body as raw bytes so we can detect which fields were explicitly sent.
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	var req UpdateIssueRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Track which fields were explicitly present in JSON (even if null)
	var rawFields map[string]json.RawMessage
	json.Unmarshal(bodyBytes, &rawFields)

	// Pre-fill nullable fields (bare sqlc.narg) with current values
	params := db.UpdateIssueParams{
		ID:            prevIssue.ID,
		AssigneeType:  prevIssue.AssigneeType,
		AssigneeID:    prevIssue.AssigneeID,
		StartDate:     prevIssue.StartDate,
		DueDate:       prevIssue.DueDate,
		ParentIssueID: prevIssue.ParentIssueID,
		ProjectID:     prevIssue.ProjectID,
	}

	// COALESCE fields — only set when explicitly provided
	if req.Title != nil {
		params.Title = pgtype.Text{String: *req.Title, Valid: true}
	}
	if req.Description != nil {
		params.Description = pgtype.Text{String: *req.Description, Valid: true}
	}
	if req.Status != nil {
		if !validateIssueEnum(w, "status", *req.Status, validIssueStatuses) {
			return
		}
		params.Status = pgtype.Text{String: *req.Status, Valid: true}
	}
	if req.Priority != nil {
		if !validateIssueEnum(w, "priority", *req.Priority, validIssuePriorities) {
			return
		}
		params.Priority = pgtype.Text{String: *req.Priority, Valid: true}
	}
	if req.Position != nil {
		params.Position = pgtype.Float8{Float64: *req.Position, Valid: true}
	}
	// Nullable fields — only override when explicitly present in JSON
	if _, ok := rawFields["assignee_type"]; ok {
		if req.AssigneeType != nil {
			params.AssigneeType = pgtype.Text{String: *req.AssigneeType, Valid: true}
		} else {
			params.AssigneeType = pgtype.Text{Valid: false} // explicit null = unassign
		}
	}
	if _, ok := rawFields["assignee_id"]; ok {
		if req.AssigneeID != nil {
			id, ok := parseUUIDOrBadRequest(w, *req.AssigneeID, "assignee_id")
			if !ok {
				return
			}
			params.AssigneeID = id
		} else {
			params.AssigneeID = pgtype.UUID{Valid: false} // explicit null = unassign
		}
	}
	if _, ok := rawFields["start_date"]; ok {
		if req.StartDate != nil && *req.StartDate != "" {
			d, err := util.ParseCalendarDate(*req.StartDate)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid start_date format, expected YYYY-MM-DD")
				return
			}
			params.StartDate = d
		} else {
			params.StartDate = pgtype.Date{Valid: false} // explicit null = clear date
		}
	}
	if _, ok := rawFields["due_date"]; ok {
		if req.DueDate != nil && *req.DueDate != "" {
			d, err := util.ParseCalendarDate(*req.DueDate)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid due_date format, expected YYYY-MM-DD")
				return
			}
			params.DueDate = d
		} else {
			params.DueDate = pgtype.Date{Valid: false} // explicit null = clear date
		}
	}
	if _, ok := rawFields["parent_issue_id"]; ok {
		if req.ParentIssueID != nil {
			newParentID, ok := parseUUIDOrBadRequest(w, *req.ParentIssueID, "parent_issue_id")
			if !ok {
				return
			}
			if err := h.validateIssueParentInWorkspace(r.Context(), prevIssue, newParentID); errors.Is(err, errIssueParentNotFound) {
				writeError(w, http.StatusBadRequest, "parent issue not found in this workspace")
				return
			} else if err != nil {
				writeError(w, http.StatusBadRequest, "circular parent relationship detected")
				return
			}
			params.ParentIssueID = newParentID
		} else {
			params.ParentIssueID = pgtype.UUID{Valid: false} // explicit null = remove parent
		}
	}
	if _, ok := rawFields["project_id"]; ok {
		if req.ProjectID != nil {
			projectUUID, ok := parseUUIDOrBadRequest(w, *req.ProjectID, "project_id")
			if !ok {
				return
			}
			if err := h.validateProjectInWorkspace(r.Context(), prevIssue.WorkspaceID, projectUUID); err != nil {
				writeError(w, http.StatusBadRequest, "project not found in this workspace")
				return
			}
			params.ProjectID = projectUUID
		} else {
			params.ProjectID = pgtype.UUID{Valid: false}
		}
	}

	// Validate the resulting (assignee_type, assignee_id) pair when the caller
	// touches either field. Existing data on the issue is left alone if the
	// caller is not changing it.
	_, touchedType := rawFields["assignee_type"]
	_, touchedID := rawFields["assignee_id"]
	if touchedType || touchedID {
		if status, msg := h.validateAssigneePair(r.Context(), r, workspaceID, params.AssigneeType, params.AssigneeID); status != 0 {
			writeError(w, status, msg)
			return
		}
	}

	attachmentIDs, ok := parseUUIDSliceOrBadRequest(w, req.AttachmentIDs, "attachment_ids")
	if !ok {
		return
	}

	if params.Status.Valid && params.Status.String == "done" {
		incomplete, err := h.incompleteChildrenBlockingDone(r.Context(), prevIssue)
		if err != nil {
			slog.Warn("check child issue done gate failed", append(logger.RequestAttrs(r), "error", err, "issue_id", id, "workspace_id", workspaceID)...)
			writeError(w, http.StatusInternalServerError, "failed to check child issues")
			return
		}
		if len(incomplete) > 0 {
			h.writeIssueDoneBlockedByChildren(w, incomplete)
			return
		}
		actorType, _ := h.resolveActor(r, userID, workspaceID)
		if actorType == "agent" {
			blocked, err := h.issueDoneBlockedByMissingGongfengMR(r.Context(), prevIssue)
			if err != nil {
				slog.Warn("check issue linked MR gate failed", append(logger.RequestAttrs(r), "error", err, "issue_id", id, "workspace_id", workspaceID)...)
				writeError(w, http.StatusInternalServerError, "failed to check linked MRs")
				return
			}
			if blocked {
				h.writeIssueDoneBlockedByMissingMR(w)
				return
			}
		}
	}

	issue, err := h.Queries.UpdateIssue(r.Context(), params)
	if err != nil {
		slog.Warn("update issue failed", append(logger.RequestAttrs(r), "error", err, "issue_id", id, "workspace_id", workspaceID)...)
		writeError(w, http.StatusInternalServerError, "failed to update issue: "+err.Error())
		return
	}

	if len(attachmentIDs) > 0 {
		h.linkAttachmentsByIssueIDs(r.Context(), issue.ID, issue.WorkspaceID, attachmentIDs)
	}

	// Determine actor identity: agent (via X-Agent-ID header) or member.
	actorType, actorID := h.resolveActor(r, userID, workspaceID)

	if req.Title != nil || req.Description != nil {
		nextDescription := ""
		if issue.Description.Valid {
			nextDescription = issue.Description.String
		}
		metadataBefore := decodeIssueMetadataRaw(issue.Metadata)
		metadataAfter := h.enrichIssueSourceMetadata(r.Context(), metadataBefore, userID, issue.Title, nextDescription)
		if _, err := validateIssueMetadataObject(metadataAfter); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		metadataChanges := changedIssueMetadataKeys(metadataBefore, metadataAfter)
		if len(metadataChanges) > 0 {
			for key, value := range metadataChanges {
				issue, err = h.Queries.SetIssueMetadataKey(r.Context(), db.SetIssueMetadataKeyParams{
					ID:          issue.ID,
					WorkspaceID: issue.WorkspaceID,
					Key:         key,
					Value:       value,
				})
				if err != nil {
					writeError(w, http.StatusInternalServerError, "failed to update issue source metadata")
					return
				}
			}
			h.publish(protocol.EventIssueMetadataChanged, workspaceID, actorType, actorID, map[string]any{
				"issue_id": uuidToString(issue.ID),
				"metadata": parseIssueMetadata(issue.Metadata),
			})
		}
	}

	prefix := h.getIssuePrefix(r.Context(), issue.WorkspaceID)
	resp := issueToResponse(issue, prefix)
	slog.Info("issue updated", append(logger.RequestAttrs(r), "issue_id", id, "workspace_id", workspaceID)...)

	assigneeChanged := (req.AssigneeType != nil || req.AssigneeID != nil) &&
		(prevIssue.AssigneeType.String != issue.AssigneeType.String || uuidToString(prevIssue.AssigneeID) != uuidToString(issue.AssigneeID))
	statusChanged := req.Status != nil && prevIssue.Status != issue.Status
	projectChanged := req.ProjectID != nil && prevIssue.ProjectID != issue.ProjectID
	priorityChanged := req.Priority != nil && prevIssue.Priority != issue.Priority
	descriptionChanged := req.Description != nil && textToPtr(prevIssue.Description) != resp.Description
	titleChanged := req.Title != nil && prevIssue.Title != issue.Title
	prevStartDate := dateToPtr(prevIssue.StartDate)
	startDateChanged := prevStartDate != resp.StartDate && (prevStartDate == nil) != (resp.StartDate == nil) ||
		(prevStartDate != nil && resp.StartDate != nil && *prevStartDate != *resp.StartDate)
	prevDueDate := dateToPtr(prevIssue.DueDate)
	dueDateChanged := prevDueDate != resp.DueDate && (prevDueDate == nil) != (resp.DueDate == nil) ||
		(prevDueDate != nil && resp.DueDate != nil && *prevDueDate != *resp.DueDate)

	h.publish(protocol.EventIssueUpdated, workspaceID, actorType, actorID, map[string]any{
		"issue":               resp,
		"assignee_changed":    assigneeChanged,
		"status_changed":      statusChanged,
		"priority_changed":    priorityChanged,
		"start_date_changed":  startDateChanged,
		"due_date_changed":    dueDateChanged,
		"description_changed": descriptionChanged,
		"title_changed":       titleChanged,
		"prev_title":          prevIssue.Title,
		"prev_assignee_type":  textToPtr(prevIssue.AssigneeType),
		"prev_assignee_id":    uuidToPtr(prevIssue.AssigneeID),
		"prev_status":         prevIssue.Status,
		"prev_priority":       prevIssue.Priority,
		"prev_start_date":     prevStartDate,
		"prev_due_date":       prevDueDate,
		"prev_description":    textToPtr(prevIssue.Description),
		"creator_type":        prevIssue.CreatorType,
		"creator_id":          uuidToString(prevIssue.CreatorID),
	})

	h.reconcileIssueUpdateSideEffects(r.Context(), r, prevIssue, issue, assigneeChanged, statusChanged, actorType, actorID)
	if issue.Status == "backlog" && (statusChanged || projectChanged || assigneeChanged) {
		h.IssueService.EnsureProjectOwnerApprovalForBacklog(r.Context(), issue, actorType, actorID)
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) reconcileIssueUpdateSideEffects(ctx context.Context, r *http.Request, prevIssue db.Issue, issue db.Issue, assigneeChanged bool, statusChanged bool, actorType string, actorID string) {
	// Reconcile task queue when assignee changes.
	if assigneeChanged {
		h.TaskService.CancelTasksForIssue(ctx, issue.ID)
		if h.shouldEnqueueAgentTask(ctx, issue) {
			h.TaskService.EnqueueTaskForIssue(ctx, issue)
		}
		if h.shouldEnqueueSquadLeaderOnAssign(ctx, issue) {
			h.enqueueSquadLeaderTask(ctx, issue, pgtype.UUID{}, actorType, actorID)
		}
	}

	// Trigger the assigned agent when an issue moves out of backlog. Backlog
	// acts as a parking lot; the self-loop guard prevents an agent from
	// re-triggering the same issue its current task is running on.
	if statusChanged && !assigneeChanged &&
		prevIssue.Status == "backlog" && issue.Status != "done" && issue.Status != "cancelled" &&
		!h.isAssignedAgentRunningOnIssue(ctx, r, actorType, actorID, issue) {
		if h.isAgentAssigneeReady(ctx, issue) {
			h.TaskService.EnqueueTaskForIssue(ctx, issue)
		}
		if h.isSquadLeaderReady(ctx, issue) {
			h.enqueueSquadLeaderTask(ctx, issue, pgtype.UUID{}, actorType, actorID)
		}
	}

	// Cancellation is a user-initiated terminal action that should stop execution.
	if statusChanged && issue.Status == "cancelled" {
		h.TaskService.CancelTasksForIssue(ctx, issue.ID)
	}

	// Best-effort parent notification for child done transitions.
	if statusChanged {
		h.notifyParentOfChildDone(ctx, prevIssue, issue, actorType, actorID)
	}
}

// validateAssigneePair verifies the (assignee_type, assignee_id) pair refers
// to an existing entity in the workspace. For agent assignees it also rejects
// archived agents and runs the personal-agent gate via canAccessPersonalAgent
// — assigning an issue is a task-producing surface, so it must use the same
// predicate as chat / @-mention / history. Agent callers (X-Agent-ID) bypass
// the gate so A2A flows can still hand work off to personal agents.
//
// Returns (statusCode, errorMessage). statusCode == 0 means the pair is valid;
// callers should treat any non-zero status as a rejection and surface it back
// to the client.
func (h *Handler) validateAssigneePair(ctx context.Context, r *http.Request, workspaceID string, assigneeType pgtype.Text, assigneeID pgtype.UUID) (int, string) {
	// Both unset → unassigned issue, valid.
	if !assigneeType.Valid && !assigneeID.Valid {
		return 0, ""
	}
	// Exactly one of type/id provided → callers must always pair them.
	if assigneeType.Valid != assigneeID.Valid {
		return http.StatusBadRequest, "assignee_type and assignee_id must be provided together"
	}
	wsUUID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return http.StatusBadRequest, "invalid workspace_id"
	}
	switch assigneeType.String {
	case "member":
		if _, err := h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
			UserID:      assigneeID,
			WorkspaceID: wsUUID,
		}); err != nil {
			return http.StatusBadRequest, "assignee_id does not refer to a member of this workspace"
		}
		return 0, ""
	case "agent":
		agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
			ID:          assigneeID,
			WorkspaceID: wsUUID,
		})
		if err != nil {
			return http.StatusBadRequest, "assignee_id does not refer to an agent of this workspace"
		}
		if agent.ArchivedAt.Valid {
			return http.StatusBadRequest, "cannot assign to archived agent"
		}
		actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
		if !h.canAccessPersonalAgent(ctx, agent, actorType, actorID, workspaceID) {
			return http.StatusForbidden, "cannot assign to personal agent"
		}
		return 0, ""
	case "squad":
		squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
			ID:          assigneeID,
			WorkspaceID: wsUUID,
		})
		if err != nil {
			return http.StatusBadRequest, "assignee_id does not refer to a squad in this workspace"
		}
		if squad.ArchivedAt.Valid {
			return http.StatusBadRequest, "cannot assign to an archived squad"
		}
		actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
		if !h.canUseSquad(ctx, squad, actorType, actorID, workspaceID) {
			return http.StatusForbidden, "cannot assign to personal squad"
		}
		leader, err := h.Queries.GetAgent(ctx, squad.LeaderID)
		if err != nil || leader.ArchivedAt.Valid {
			return http.StatusBadRequest, "squad leader is archived; cannot assign to this squad"
		}
		if !h.canAccessPersonalAgent(ctx, leader, actorType, actorID, workspaceID) {
			return http.StatusForbidden, "cannot assign to squad with personal leader"
		}
		return 0, ""
	default:
		return http.StatusBadRequest, "assignee_type must be 'member', 'agent', or 'squad'"
	}
}

// shouldEnqueueAgentTask returns true when an issue creation or assignment
// should trigger the assigned agent. Backlog issues are skipped — backlog
// acts as a parking lot where issues can be pre-assigned without immediately
// triggering execution. Moving out of backlog is handled separately in
// UpdateIssue.
func (h *Handler) shouldEnqueueAgentTask(ctx context.Context, issue db.Issue) bool {
	if issue.Status == "backlog" {
		return false
	}
	return h.isAgentAssigneeReady(ctx, issue)
}

// shouldEnqueueOnComment returns true if a member comment on this issue should
// trigger the assigned agent. Fires for any status — comments are
// conversational and can happen at any stage, including after completion
// (e.g. follow-up questions on a done issue).
//
// Mirrors the personal-agent gate that computeMentionedAgentCommentTriggers applies on the
// @mention path: once an owner/admin assigns a personal agent to an issue, the
// agent's UUID is "welded" onto the issue and remains visible to every member
// who can view it. Without this check any of those members could dispatch a new
// task to the personal agent simply by commenting (#3300).
func (h *Handler) shouldEnqueueOnComment(ctx context.Context, issue db.Issue, actorType, actorID string, opts commentTriggerComputeOptions) bool {
	if !issue.AssigneeType.Valid || issue.AssigneeType.String != "agent" || !issue.AssigneeID.Valid {
		return false
	}
	agent, err := h.Queries.GetAgent(ctx, issue.AssigneeID)
	if err != nil || !agent.RuntimeID.Valid || agent.ArchivedAt.Valid {
		return false
	}
	if !h.canAccessPersonalAgent(ctx, agent, actorType, actorID, uuidToString(issue.WorkspaceID)) {
		return false
	}
	// Coalescing queue: allow enqueue when a task is running (so the agent
	// picks up new comments on the next cycle) but skip if this agent already
	// has a pending task (natural dedup for rapid-fire comments).
	hasPending, err := h.hasPendingTaskForIssueAndAgent(ctx, issue.ID, issue.AssigneeID, opts)
	if err != nil || hasPending {
		return false
	}
	return true
}

// isAssignedAgentRunningOnIssue reports whether the calling agent's current
// task is running for the exact issue being promoted AND that agent is the
// issue's current executor (direct assignee or squad leader). That is the true
// self-loop on backlog→active: the executor flipping its own issue would
// immediately re-enqueue itself.
//
// Project-owner review tasks can also run on the same issue, but the reviewer
// is not the issue executor; approving backlog→todo must wake the assigned
// agent or squad leader. Same-agent cross-issue handoff remains allowed too.
//
// X-Task-ID is guaranteed to be present and consistent when actorType is
// "agent": resolveActor demotes the actor to "member" otherwise (handler.go
// resolveActor). We still recheck defensively — a future caller could pass
// agent identity through a different path.
func (h *Handler) isAssignedAgentRunningOnIssue(ctx context.Context, r *http.Request, actorType, actorID string, issue db.Issue) bool {
	if actorType != "agent" {
		return false
	}
	if !h.actorIsIssueExecutor(ctx, actorID, issue) {
		return false
	}
	taskIDStr := r.Header.Get("X-Task-ID")
	if taskIDStr == "" {
		return false
	}
	taskUUID, err := util.ParseUUID(taskIDStr)
	if err != nil {
		return false
	}
	task, err := h.Queries.GetAgentTask(ctx, taskUUID)
	if err != nil {
		return false
	}
	if !task.IssueID.Valid {
		return false
	}
	return uuidToString(task.IssueID) == uuidToString(issue.ID)
}

func (h *Handler) actorIsIssueExecutor(ctx context.Context, actorID string, issue db.Issue) bool {
	if actorID == "" || !issue.AssigneeType.Valid || !issue.AssigneeID.Valid {
		return false
	}
	switch issue.AssigneeType.String {
	case "agent":
		return actorID == uuidToString(issue.AssigneeID)
	case "squad":
		squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
			ID:          issue.AssigneeID,
			WorkspaceID: issue.WorkspaceID,
		})
		if err != nil {
			return false
		}
		return actorID == uuidToString(squad.LeaderID)
	default:
		return false
	}
}

// isAgentAssigneeReady checks if an issue is assigned to an active agent
// with a valid runtime.
func (h *Handler) isAgentAssigneeReady(ctx context.Context, issue db.Issue) bool {
	if !issue.AssigneeType.Valid || issue.AssigneeType.String != "agent" || !issue.AssigneeID.Valid {
		return false
	}

	agent, err := h.Queries.GetAgent(ctx, issue.AssigneeID)
	if err != nil || !agent.RuntimeID.Valid || agent.ArchivedAt.Valid {
		return false
	}

	return true
}

func (h *Handler) DeleteIssue(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, id)
	if !ok {
		return
	}

	h.TaskService.CancelTasksForIssue(r.Context(), issue.ID)
	// Fail any linked autopilot runs before delete (ON DELETE SET NULL clears issue_id).
	h.Queries.FailAutopilotRunsByIssue(r.Context(), issue.ID)

	// Collect all attachment URLs (issue-level + comment-level) before CASCADE delete.
	attachmentURLs, _ := h.Queries.ListAttachmentURLsByIssueOrComments(r.Context(), issue.ID)

	err := h.Queries.DeleteIssue(r.Context(), db.DeleteIssueParams{
		ID:          issue.ID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete issue")
		return
	}

	h.deleteS3Objects(r.Context(), attachmentURLs)
	userID := requestUserID(r)
	actorType, actorID := h.resolveActor(r, userID, uuidToString(issue.WorkspaceID))
	// Always emit the resolved UUID — frontend caches key by UUID, so an
	// identifier-style payload ("MUL-123") would leave stale entries on
	// other clients after an identifier-path delete.
	resolvedID := uuidToString(issue.ID)
	h.publish(protocol.EventIssueDeleted, uuidToString(issue.WorkspaceID), actorType, actorID, map[string]any{"issue_id": resolvedID})
	slog.Info("issue deleted", append(logger.RequestAttrs(r), "issue_id", resolvedID, "workspace_id", uuidToString(issue.WorkspaceID))...)
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Batch operations
// ---------------------------------------------------------------------------

type BatchUpdateIssuesRequest struct {
	IssueIDs []string           `json:"issue_ids"`
	Updates  UpdateIssueRequest `json:"updates"`
}

