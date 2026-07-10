package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func validateIssueEnum(w http.ResponseWriter, field, value string, allowed []string) bool {
	for _, a := range allowed {
		if value == a {
			return true
		}
	}
	writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid %s %q; valid values: %s", field, value, strings.Join(allowed, ", ")))
	return false
}
func incompleteChildIssues(children []db.Issue, issuePrefix string) []IncompleteChildIssueResponse {
	incomplete := make([]IncompleteChildIssueResponse, 0)
	for _, child := range children {
		if child.Status == "done" {
			continue
		}
		incomplete = append(incomplete, IncompleteChildIssueResponse{
			ID:           uuidToString(child.ID),
			Identifier:   issuePrefix + "-" + strconv.Itoa(int(child.Number)),
			Title:        child.Title,
			Status:       child.Status,
			AssigneeType: textToPtr(child.AssigneeType),
			AssigneeID:   uuidToPtr(child.AssigneeID),
			ProjectID:    uuidToPtr(child.ProjectID),
		})
	}
	return incomplete
}
func issueToResponse(i db.Issue, issuePrefix string) IssueResponse {
	identifier := issuePrefix + "-" + strconv.Itoa(int(i.Number))
	return IssueResponse{
		ID:              uuidToString(i.ID),
		WorkspaceID:     uuidToString(i.WorkspaceID),
		Number:          i.Number,
		Identifier:      identifier,
		Title:           i.Title,
		Description:     textToPtr(i.Description),
		Status:          i.Status,
		Priority:        i.Priority,
		AssigneeType:    textToPtr(i.AssigneeType),
		AssigneeID:      uuidToPtr(i.AssigneeID),
		CreatorType:     i.CreatorType,
		CreatorID:       uuidToString(i.CreatorID),
		ParentIssueID:   uuidToPtr(i.ParentIssueID),
		ProjectID:       uuidToPtr(i.ProjectID),
		Position:        i.Position,
		StartDate:       dateToPtr(i.StartDate),
		DueDate:         dateToPtr(i.DueDate),
		CreatedAt:       timestampToString(i.CreatedAt),
		UpdatedAt:       timestampToString(i.UpdatedAt),
		WorkStartedAt:   timestampToPtr(i.WorkStartedAt),
		WorkCompletedAt: timestampToPtr(i.WorkCompletedAt),
		Metadata:        parseIssueMetadata(i.Metadata),
	}
}
func issueListRowToResponse(i db.ListIssuesRow, issuePrefix string) IssueResponse {
	identifier := issuePrefix + "-" + strconv.Itoa(int(i.Number))
	return IssueResponse{
		ID:            uuidToString(i.ID),
		WorkspaceID:   uuidToString(i.WorkspaceID),
		Number:        i.Number,
		Identifier:    identifier,
		Title:         i.Title,
		Description:   textToPtr(i.Description),
		Status:        i.Status,
		Priority:      i.Priority,
		AssigneeType:  textToPtr(i.AssigneeType),
		AssigneeID:    uuidToPtr(i.AssigneeID),
		CreatorType:   i.CreatorType,
		CreatorID:     uuidToString(i.CreatorID),
		ParentIssueID: uuidToPtr(i.ParentIssueID),
		ProjectID:     uuidToPtr(i.ProjectID),
		Position:      i.Position,
		StartDate:     dateToPtr(i.StartDate),
		DueDate:       dateToPtr(i.DueDate),
		CreatedAt:     timestampToString(i.CreatedAt),
		UpdatedAt:     timestampToString(i.UpdatedAt),
		Metadata:      parseIssueMetadata(i.Metadata),
	}
}
func openIssueRowToResponse(i db.ListOpenIssuesRow, issuePrefix string) IssueResponse {
	identifier := issuePrefix + "-" + strconv.Itoa(int(i.Number))
	return IssueResponse{
		ID:            uuidToString(i.ID),
		WorkspaceID:   uuidToString(i.WorkspaceID),
		Number:        i.Number,
		Identifier:    identifier,
		Title:         i.Title,
		Description:   textToPtr(i.Description),
		Status:        i.Status,
		Priority:      i.Priority,
		AssigneeType:  textToPtr(i.AssigneeType),
		AssigneeID:    uuidToPtr(i.AssigneeID),
		CreatorType:   i.CreatorType,
		CreatorID:     uuidToString(i.CreatorID),
		ParentIssueID: uuidToPtr(i.ParentIssueID),
		ProjectID:     uuidToPtr(i.ProjectID),
		Position:      i.Position,
		StartDate:     dateToPtr(i.StartDate),
		DueDate:       dateToPtr(i.DueDate),
		CreatedAt:     timestampToString(i.CreatedAt),
		UpdatedAt:     timestampToString(i.UpdatedAt),
		Metadata:      parseIssueMetadata(i.Metadata),
	}
}
func issueListJoinSQL(visibleAgentIDsRef string) string {
	return fmt.Sprintf(`LEFT JOIN "user" assignee_member
       ON i.assignee_type = 'member'
      AND assignee_member.id = i.assignee_id
LEFT JOIN agent assignee_agent
       ON i.assignee_type = 'agent'
      AND assignee_agent.id = i.assignee_id
      AND assignee_agent.workspace_id = i.workspace_id
LEFT JOIN squad assignee_squad
       ON i.assignee_type = 'squad'
      AND assignee_squad.id = i.assignee_id
      AND assignee_squad.workspace_id = i.workspace_id
LEFT JOIN project
       ON project.id = i.project_id
      AND project.workspace_id = i.workspace_id
LEFT JOIN (
       SELECT parent_issue_id,
              COUNT(*) FILTER (WHERE status IN ('done', 'cancelled'))::bigint AS child_done,
              COUNT(*)::bigint AS child_total
         FROM issue
        WHERE workspace_id = $1
          AND parent_issue_id IS NOT NULL
        GROUP BY parent_issue_id
) child_progress
       ON child_progress.parent_issue_id = i.id
LEFT JOIN (
       SELECT issue_id,
              COUNT(*) FILTER (WHERE status = 'running')::bigint AS running_count,
              COUNT(*) FILTER (WHERE status IN ('queued', 'dispatched', 'waiting_local_directory'))::bigint AS queued_count,
              ARRAY_AGG(DISTINCT agent_id) FILTER (
                  WHERE status IN ('queued', 'dispatched', 'waiting_local_directory', 'running')
              ) AS agent_ids
         FROM agent_task_queue
        WHERE issue_id IS NOT NULL
          AND agent_id = ANY(%s::uuid[])
          AND status IN ('queued', 'dispatched', 'waiting_local_directory', 'running')
        GROUP BY issue_id
) agent_activity
       ON agent_activity.issue_id = i.id`, visibleAgentIDsRef)
}
func scanIssueListRow(rows interface{ Scan(dest ...any) error }, row *issueListRow) error {
	return rows.Scan(issueListScanDest(row)...)
}
func issueListScanDest(row *issueListRow) []any {
	return []any{
		&row.ID,
		&row.WorkspaceID,
		&row.Title,
		&row.Description,
		&row.Status,
		&row.Priority,
		&row.AssigneeType,
		&row.AssigneeID,
		&row.CreatorType,
		&row.CreatorID,
		&row.ParentIssueID,
		&row.Position,
		&row.StartDate,
		&row.DueDate,
		&row.CreatedAt,
		&row.UpdatedAt,
		&row.Number,
		&row.ProjectID,
		&row.Metadata,
		&row.Summary.AssigneeName,
		&row.Summary.AssigneeAvatarURL,
		&row.Summary.ProjectTitle,
		&row.Summary.ProjectIcon,
		&row.Summary.ChildDone,
		&row.Summary.ChildTotal,
		&row.Summary.AgentRunningCount,
		&row.Summary.AgentQueuedCount,
		&row.Summary.AgentIDs,
	}
}
func issueListRowWithSummaryToResponse(row issueListRow, issuePrefix string) IssueResponse {
	resp := issueListRowToResponse(row.ListIssuesRow, issuePrefix)
	attachIssueListSummary(&resp, row.Summary)
	return resp
}
func attachIssueListSummary(resp *IssueResponse, summary issueListSummary) {
	if resp.AssigneeType != nil && resp.AssigneeID != nil {
		name := unknownIssueAssigneeName(*resp.AssigneeType)
		if summary.AssigneeName.Valid {
			name = summary.AssigneeName.String
		}
		resp.Assignee = &IssueActorSummaryResponse{
			Type:      *resp.AssigneeType,
			ID:        *resp.AssigneeID,
			Name:      name,
			AvatarURL: textToPtr(summary.AssigneeAvatarURL),
		}
	}
	if resp.ProjectID != nil && summary.ProjectTitle.Valid {
		resp.Project = &IssueProjectSummaryResponse{
			ID:    *resp.ProjectID,
			Title: summary.ProjectTitle.String,
			Icon:  textToPtr(summary.ProjectIcon),
		}
	}
	resp.ChildProgress = &IssueChildProgressResponse{
		Done:  summary.ChildDone,
		Total: summary.ChildTotal,
	}
	agentIDs := make([]string, 0, len(summary.AgentIDs))
	for _, id := range summary.AgentIDs {
		if id.Valid {
			agentIDs = append(agentIDs, uuidToString(id))
		}
	}
	resp.AgentActivity = &IssueAgentActivitySummaryResponse{
		RunningCount: summary.AgentRunningCount,
		QueuedCount:  summary.AgentQueuedCount,
		AgentIDs:     agentIDs,
	}
}
func unknownIssueAssigneeName(assigneeType string) string {
	switch assigneeType {
	case "member":
		return "未知成员"
	case "agent":
		return "未知智能体"
	case "squad":
		return "未知小队"
	default:
		return "未知负责人"
	}
}
func assigneeGroupID(assigneeType pgtype.Text, assigneeID pgtype.UUID) string {
	if assigneeType.Valid && assigneeID.Valid {
		return "assignee:" + assigneeType.String + ":" + uuidToString(assigneeID)
	}
	return "assignee:unassigned"
}
func extractSnippet(content, query string) string {
	runes := []rune(content)
	lowerRunes := []rune(strings.ToLower(content))
	queryRunes := []rune(strings.ToLower(query))

	idx := findRuneSubstring(lowerRunes, queryRunes)

	// If phrase not found, try individual terms for multi-word queries.
	matchLen := len(queryRunes)
	if idx < 0 {
		terms := strings.Fields(strings.ToLower(query))
		if len(terms) > 1 {
			earliest := -1
			earliestLen := 0
			for _, term := range terms {
				termRunes := []rune(term)
				pos := findRuneSubstring(lowerRunes, termRunes)
				if pos >= 0 && (earliest < 0 || pos < earliest) {
					earliest = pos
					earliestLen = len(termRunes)
				}
			}
			if earliest >= 0 {
				idx = earliest
				matchLen = earliestLen
			}
		}
	}

	if idx < 0 {
		if len(runes) > 120 {
			return string(runes[:120]) + "..."
		}
		return content
	}
	start := idx - 40
	if start < 0 {
		start = 0
	}
	end := idx + matchLen + 80
	if end > len(runes) {
		end = len(runes)
	}
	snippet := string(runes[start:end])
	if start > 0 {
		snippet = "..." + snippet
	}
	if end < len(runes) {
		snippet = snippet + "..."
	}
	return snippet
}
func findRuneSubstring(haystack, needle []rune) int {
	if len(needle) == 0 || len(haystack) < len(needle) {
		return -1
	}
	for i := 0; i <= len(haystack)-len(needle); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
func descriptionContains(desc pgtype.Text, phrase string, terms []string) bool {
	if !desc.Valid || desc.String == "" {
		return false
	}
	lower := strings.ToLower(desc.String)
	if strings.Contains(lower, strings.ToLower(phrase)) {
		return true
	}
	if len(terms) > 1 {
		for _, t := range terms {
			if !strings.Contains(lower, strings.ToLower(t)) {
				return false
			}
		}
		return true
	}
	return false
}
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}
func splitSearchTerms(q string) []string {
	fields := strings.FieldsFunc(q, func(r rune) bool {
		return unicode.IsSpace(r)
	})
	terms := make([]string, 0, len(fields))
	for _, f := range fields {
		if f != "" {
			terms = append(terms, f)
		}
	}
	return terms
}
func parseQueryNumber(q string) (int, bool) {
	const maxIssueNumber = 2147483647
	q = strings.TrimSpace(q)
	// Check for identifier pattern like "MUL-123"
	if m := identifierNumberRe.FindStringSubmatch(q); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 && n <= maxIssueNumber {
			return n, true
		}
	}
	// Check for bare number
	if n, err := strconv.Atoi(q); err == nil && n > 0 && n <= maxIssueNumber {
		return n, true
	}
	return 0, false
}
func buildSearchQuery(phrase string, terms []string, queryNum int, hasNum bool, includeClosed bool) (string, []any) {
	// Lowercase in Go so SQL only needs LOWER() on the column side.
	phrase = strings.ToLower(phrase)
	for i, t := range terms {
		terms[i] = strings.ToLower(t)
	}

	// Parameter index tracker
	argIdx := 1
	args := []any{}
	nextArg := func(val any) string {
		args = append(args, val)
		s := fmt.Sprintf("$%d", argIdx)
		argIdx++
		return s
	}

	escapedPhrase := escapeLike(phrase)
	// $1: exact phrase (for exact title match)
	phraseParam := nextArg(escapedPhrase)
	// $2: "%phrase%" (contains pattern — pre-built for pg_bigm index usage)
	phraseContainsParam := nextArg("%" + escapedPhrase + "%")
	// $3: "phrase%" (starts-with pattern)
	phraseStartsWithParam := nextArg(escapedPhrase + "%")

	wsParam := nextArg(nil) // $4 — workspace_id, will be filled by caller position

	// Build per-term LIKE conditions only for multi-word search.
	var termContainsParams []string
	if len(terms) > 1 {
		for _, t := range terms {
			et := escapeLike(t)
			termContainsParams = append(termContainsParams, nextArg("%"+et+"%"))
		}
	}

	// --- WHERE clause ---
	var whereParts []string

	// Full phrase match: title, description, or comment
	phraseMatch := fmt.Sprintf(
		"(LOWER(i.title) LIKE %s OR LOWER(COALESCE(i.description, '')) LIKE %s OR EXISTS (SELECT 1 FROM comment c WHERE c.issue_id = i.id AND LOWER(c.content) LIKE %s))",
		phraseContainsParam, phraseContainsParam, phraseContainsParam,
	)
	whereParts = append(whereParts, phraseMatch)

	// Multi-word AND match (each term must appear somewhere)
	if len(termContainsParams) > 1 {
		var termConditions []string
		for _, tp := range termContainsParams {
			termConditions = append(termConditions, fmt.Sprintf(
				"(LOWER(i.title) LIKE %s OR LOWER(COALESCE(i.description, '')) LIKE %s OR EXISTS (SELECT 1 FROM comment c WHERE c.issue_id = i.id AND LOWER(c.content) LIKE %s))",
				tp, tp, tp,
			))
		}
		whereParts = append(whereParts, "("+strings.Join(termConditions, " AND ")+")")
	}

	// Number match
	numParam := ""
	if hasNum {
		numParam = nextArg(queryNum)
		whereParts = append(whereParts, fmt.Sprintf("i.number = %s", numParam))
	}

	whereClause := "(" + strings.Join(whereParts, " OR ") + ")"

	if !includeClosed {
		whereClause += " AND i.status NOT IN ('done', 'cancelled')"
	}

	// --- ORDER BY clause ---
	// Build ranking CASE with fine-grained tiers.
	var rankCases []string

	// Tier 0: Identifier exact match
	if hasNum {
		rankCases = append(rankCases, fmt.Sprintf("WHEN i.number = %s THEN 0", numParam))
	}

	// Tier 1: Exact title match
	rankCases = append(rankCases, fmt.Sprintf("WHEN LOWER(i.title) = %s THEN 1", phraseParam))

	// Tier 2: Title starts with phrase
	rankCases = append(rankCases, fmt.Sprintf("WHEN LOWER(i.title) LIKE %s THEN 2", phraseStartsWithParam))

	// Tier 3: Title contains phrase
	rankCases = append(rankCases, fmt.Sprintf("WHEN LOWER(i.title) LIKE %s THEN 3", phraseContainsParam))

	// Tier 4: Title matches all words (multi-word only)
	if len(termContainsParams) > 1 {
		var titleTerms []string
		for _, tp := range termContainsParams {
			titleTerms = append(titleTerms, fmt.Sprintf("LOWER(i.title) LIKE %s", tp))
		}
		rankCases = append(rankCases, fmt.Sprintf("WHEN (%s) THEN 4", strings.Join(titleTerms, " AND ")))
	}

	// Tier 5: Description contains phrase
	rankCases = append(rankCases, fmt.Sprintf("WHEN LOWER(COALESCE(i.description, '')) LIKE %s THEN 5", phraseContainsParam))

	// Tier 6: Description matches all words (multi-word only)
	if len(termContainsParams) > 1 {
		var descTerms []string
		for _, tp := range termContainsParams {
			descTerms = append(descTerms, fmt.Sprintf("LOWER(COALESCE(i.description, '')) LIKE %s", tp))
		}
		rankCases = append(rankCases, fmt.Sprintf("WHEN (%s) THEN 6", strings.Join(descTerms, " AND ")))
	}

	// Tier 7: Comment contains phrase
	rankCases = append(rankCases, fmt.Sprintf("WHEN EXISTS (SELECT 1 FROM comment c WHERE c.issue_id = i.id AND LOWER(c.content) LIKE %s) THEN 7", phraseContainsParam))

	// Tier 8: Comment matches all words (multi-word only)
	if len(termContainsParams) > 1 {
		var commentTerms []string
		for _, tp := range termContainsParams {
			commentTerms = append(commentTerms, fmt.Sprintf("LOWER(c.content) LIKE %s", tp))
		}
		rankCases = append(rankCases, fmt.Sprintf("WHEN EXISTS (SELECT 1 FROM comment c WHERE c.issue_id = i.id AND (%s)) THEN 8", strings.Join(commentTerms, " AND ")))
	}

	rankExpr := "CASE " + strings.Join(rankCases, " ") + " ELSE 9 END"

	// Status priority: active issues first
	statusRank := `CASE i.status
		WHEN 'in_progress' THEN 0
		WHEN 'in_review' THEN 1
		WHEN 'todo' THEN 2
		WHEN 'blocked' THEN 3
		WHEN 'backlog' THEN 4
		WHEN 'done' THEN 5
		WHEN 'cancelled' THEN 6
		ELSE 7
	END`

	// --- match_source expression ---
	matchSourceExpr := fmt.Sprintf(`CASE
		WHEN LOWER(i.title) LIKE %s THEN 'title'
		WHEN LOWER(COALESCE(i.description, '')) LIKE %s THEN 'description'
		ELSE 'comment'
	END`, phraseContainsParam, phraseContainsParam)

	// For multi-word: also check if all terms match in title/description
	if len(termContainsParams) > 1 {
		var titleTerms []string
		var descTerms []string
		for _, tp := range termContainsParams {
			titleTerms = append(titleTerms, fmt.Sprintf("LOWER(i.title) LIKE %s", tp))
			descTerms = append(descTerms, fmt.Sprintf("LOWER(COALESCE(i.description, '')) LIKE %s", tp))
		}
		matchSourceExpr = fmt.Sprintf(`CASE
			WHEN LOWER(i.title) LIKE %s THEN 'title'
			WHEN (%s) THEN 'title'
			WHEN LOWER(COALESCE(i.description, '')) LIKE %s THEN 'description'
			WHEN (%s) THEN 'description'
			ELSE 'comment'
		END`,
			phraseContainsParam, strings.Join(titleTerms, " AND "),
			phraseContainsParam, strings.Join(descTerms, " AND "),
		)
	}

	// --- matched_comment_content subquery ---
	// Always return matching comment content regardless of match_source,
	// so frontend can display comment snippet alongside title/description matches.
	commentSubquery := fmt.Sprintf(`COALESCE(
		(SELECT c.content FROM comment c
		 WHERE c.issue_id = i.id AND LOWER(c.content) LIKE %s
		 ORDER BY c.created_at DESC LIMIT 1),
		''
	)`, phraseContainsParam)

	if len(termContainsParams) > 1 {
		var commentTerms []string
		for _, tp := range termContainsParams {
			commentTerms = append(commentTerms, fmt.Sprintf("LOWER(c.content) LIKE %s", tp))
		}
		commentSubquery = fmt.Sprintf(`COALESCE(
			(SELECT c.content FROM comment c
			 WHERE c.issue_id = i.id AND (LOWER(c.content) LIKE %s OR (%s))
			 ORDER BY c.created_at DESC LIMIT 1),
			''
		)`, phraseContainsParam, strings.Join(commentTerms, " AND "))
	}

	limitParam := nextArg(nil)  // placeholder
	offsetParam := nextArg(nil) // placeholder

	query := fmt.Sprintf(`SELECT i.id, i.workspace_id, i.title, i.description, i.status, i.priority,
		i.assignee_type, i.assignee_id, i.creator_type, i.creator_id,
		i.parent_issue_id, i.acceptance_criteria, i.context_refs, i.position,
		i.start_date, i.due_date, i.created_at, i.updated_at, i.number, i.project_id,
		COUNT(*) OVER() AS total_count,
		%s AS match_source,
		%s AS matched_comment_content
	FROM issue i
	WHERE i.workspace_id = %s AND %s
	ORDER BY %s, %s, i.updated_at DESC
	LIMIT %s OFFSET %s`,
		matchSourceExpr,
		commentSubquery,
		wsParam,
		whereClause,
		rankExpr,
		statusRank,
		limitParam,
		offsetParam,
	)

	return query, args
}
func enrichTAPDSourceMetadataFromText(metadata map[string]json.RawMessage, texts ...string) map[string]json.RawMessage {
	sourceURL := metadataStringPreserve(metadata, "source_url")
	ref, ok := parseTAPDSourceURL(sourceURL)
	if !ok && sourceURL != "" {
		return metadata
	}
	if !ok {
		for _, text := range texts {
			ref, ok = parseTAPDSourceURL(text)
			if ok {
				break
			}
		}
	}
	if !ok {
		return metadata
	}
	provider, hasProvider := metadataStringPreserve(metadata, "source_provider"), false
	if provider != "" {
		hasProvider = true
	}
	if hasProvider && !strings.EqualFold(provider, externalCredentialProviderTAPD) {
		return metadata
	}
	out := make(map[string]json.RawMessage, len(metadata)+6)
	for key, value := range metadata {
		out[key] = value
	}
	setIfMissing := func(key, value string) {
		if strings.TrimSpace(value) == "" || metadataStringPreserve(out, key) != "" {
			return
		}
		raw, _ := json.Marshal(value)
		out[key] = raw
	}
	setIfMissing("source_provider", externalCredentialProviderTAPD)
	setIfMissing("source_url", ref.URL)
	setIfMissing("tapd_workspace_id", ref.WorkspaceID)
	setIfMissing("tapd_resource_type", ref.ResourceType)
	setIfMissing("tapd_resource_id", ref.ResourceID)
	if ref.ResourceType == "markdown_wiki" {
		setIfMissing("tapd_wiki_id", ref.ResourceID)
	}
	return out
}
func parseTAPDSourceURL(value string) (tapdSourceRef, bool) {
	if wiki, ok := parseTAPDMarkdownWikiURL(value); ok {
		return tapdSourceRef{
			WorkspaceID:  wiki.WorkspaceID,
			ResourceType: "markdown_wiki",
			ResourceID:   wiki.WikiID,
			URL:          wiki.URL,
		}, true
	}
	if ref, ok := parseTAPDStoryURL(value); ok {
		return ref, true
	}
	return tapdSourceRef{}, false
}
func parseTAPDStoryURL(value string) (tapdSourceRef, bool) {
	match := tapdProngStoryURLRE.FindStringSubmatch(value)
	if len(match) == 3 {
		workspaceID := strings.TrimSpace(match[1])
		storyID := strings.TrimSpace(match[2])
		if workspaceID != "" && storyID != "" {
			return tapdSourceRef{
				WorkspaceID:  workspaceID,
				ResourceType: "story",
				ResourceID:   storyID,
				URL:          fmt.Sprintf("https://www.tapd.cn/%s/prong/stories/view/%s", workspaceID, storyID),
			}, true
		}
	}
	match = tapdStoryListURLRE.FindStringSubmatch(strings.ReplaceAll(value, "&amp;", "&"))
	if len(match) == 3 {
		workspaceID := strings.TrimSpace(match[1])
		storyID := strings.TrimSpace(match[2])
		if workspaceID != "" && storyID != "" {
			return tapdSourceRef{
				WorkspaceID:  workspaceID,
				ResourceType: "story",
				ResourceID:   storyID,
				URL:          fmt.Sprintf("https://www.tapd.cn/%s/prong/stories/view/%s", workspaceID, storyID),
			}, true
		}
	}
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || !strings.HasSuffix(strings.ToLower(parsed.Host), "tapd.cn") {
		return tapdSourceRef{}, false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "tapd_fe" || parts[2] != "story" {
		return tapdSourceRef{}, false
	}
	workspaceID := strings.TrimSpace(parts[1])
	previewID := strings.TrimSpace(parsed.Query().Get("dialog_preview_id"))
	previewMatch := tapdStoryPreviewIDRE.FindStringSubmatch(previewID)
	if workspaceID == "" || len(previewMatch) != 2 || strings.TrimSpace(previewMatch[1]) == "" {
		return tapdSourceRef{}, false
	}
	storyID := strings.TrimSpace(previewMatch[1])
	return tapdSourceRef{
		WorkspaceID:  workspaceID,
		ResourceType: "story",
		ResourceID:   storyID,
		URL:          fmt.Sprintf("https://www.tapd.cn/%s/prong/stories/view/%s", workspaceID, storyID),
	}, true
}
func parseTAPDMarkdownWikiURL(value string) (tapdWikiSourceRef, bool) {
	match := tapdMarkdownWikiURLRE.FindStringSubmatch(value)
	if len(match) != 3 {
		return tapdWikiSourceRef{}, false
	}
	workspaceID := strings.TrimSpace(match[1])
	wikiID := strings.TrimSpace(match[2])
	if workspaceID == "" || wikiID == "" {
		return tapdWikiSourceRef{}, false
	}
	return tapdWikiSourceRef{
		WorkspaceID: workspaceID,
		WikiID:      wikiID,
		URL:         fmt.Sprintf("https://www.tapd.cn/%s/markdown_wikis/show/#%s", workspaceID, wikiID),
	}, true
}
func metadataStringPreserve(metadata map[string]json.RawMessage, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	raw, ok := metadata[key]
	if !ok {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}
func decodeIssueMetadataRaw(raw []byte) map[string]json.RawMessage {
	if len(raw) == 0 {
		return map[string]json.RawMessage{}
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(raw, &metadata); err != nil || metadata == nil {
		return map[string]json.RawMessage{}
	}
	return metadata
}
func changedIssueMetadataKeys(before, after map[string]json.RawMessage) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for key, next := range after {
		prev, ok := before[key]
		if ok && string(prev) == string(next) {
			continue
		}
		out[key] = next
	}
	return out
}
func metadataString(metadata map[string]json.RawMessage, key string) (string, bool) {
	if len(metadata) == 0 {
		return "", false
	}
	raw, ok := metadata[key]
	if !ok {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return strings.TrimSpace(strings.ToLower(value)), true
}
