package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/agent"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func (h *Handler) QuickCreateIssue(w http.ResponseWriter, r *http.Request) {
	var req QuickCreateIssueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		writeError(w, http.StatusBadRequest, "prompt is required")
		return
	}

	hasAgent := strings.TrimSpace(req.AgentID) != ""
	hasSquad := strings.TrimSpace(req.SquadID) != ""
	if hasAgent == hasSquad {
		writeError(w, http.StatusBadRequest, "exactly one of agent_id or squad_id is required")
		return
	}

	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	requesterID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	requesterUUID, ok := parseUUIDOrBadRequest(w, requesterID, "requester_id")
	if !ok {
		return
	}

	// Resolve the actor to the agent that will actually run the task. For
	// agent picks that's the agent itself; for squad picks it's the squad's
	// leader agent. The leader receives a squad-leader briefing on dispatch
	// (see daemon.go), matching the behavior of an issue assigned to the
	// squad — picking a squad here is functionally "ask the squad leader to
	// create this issue, on behalf of the squad".
	var agentUUID pgtype.UUID
	var squadUUID pgtype.UUID
	if hasSquad {
		var ok bool
		squadUUID, ok = parseUUIDOrBadRequest(w, req.SquadID, "squad_id")
		if !ok {
			return
		}
		squad, err := h.Queries.GetSquadInWorkspace(r.Context(), db.GetSquadInWorkspaceParams{
			ID:          squadUUID,
			WorkspaceID: wsUUID,
		})
		if err != nil {
			writeError(w, http.StatusNotFound, "squad not found")
			return
		}
		if squad.ArchivedAt.Valid {
			writeError(w, http.StatusBadRequest, "squad is archived")
			return
		}
		agentUUID = squad.LeaderID
	} else {
		var ok bool
		agentUUID, ok = parseUUIDOrBadRequest(w, req.AgentID, "agent_id")
		if !ok {
			return
		}
	}

	// Reuse the same workspace-membership / archived / personal-agent
	// ownership rules as `validateAssigneePair` so a user can't POST a
	// personal agent_id they shouldn't be able to dispatch (the frontend
	// filters them out, but the handler is the trust boundary). Squad
	// picks reach this with the resolved leader agent; the same rules
	// apply — a personal leader behind a squad the user can't reach
	// should still be rejected.
	if status, msg := h.validateAssigneePair(
		r.Context(), r, workspaceID,
		pgtype.Text{String: "agent", Valid: true},
		agentUUID,
	); status != 0 {
		writeError(w, status, msg)
		return
	}

	// Re-load the agent for the runtime liveness check below. Safe by
	// construction: validateAssigneePair just confirmed it exists in this
	// workspace and the caller has visibility.
	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          agentUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	if !agent.RuntimeID.Valid {
		writeAgentUnavailable(w, "agent has no runtime")
		return
	}
	if !h.isRuntimeOnline(r.Context(), agent.RuntimeID) {
		writeAgentUnavailable(w, "agent's runtime is offline")
		return
	}

	// Daemon CLI version gate. The agent-side prompt + create-flow rely on
	// behaviors introduced in MinQuickCreateCLIVersion (URL attachment
	// handling, quick-create attachment binding, no-retry on partial failure).
	// Older daemons either double-create issues on partial CLI failures, drop
	// attachment bindings, or mishandle pasted screenshot URLs; fail closed
	// before enqueuing rather than surface the breakage as an inbox failure
	// twenty seconds later. Dev-built
	// daemons (git-describe shape) are exempted inside CheckMinCLIVersion
	// so `make daemon` works without weakening staging or production.
	if status, payload := h.checkQuickCreateDaemonVersion(r.Context(), agent.RuntimeID); status != 0 {
		writeJSON(w, status, payload)
		return
	}

	attachmentIDs, ok := parseUUIDSliceOrBadRequest(w, req.AttachmentIDs, "attachment_ids")
	if !ok {
		return
	}

	// Optional project_id — validate it belongs to the same workspace before
	// pinning the task to it. The handler is the trust boundary; the frontend
	// already only shows projects from the active workspace, but we re-check
	// here so a forged request can't smuggle a foreign project ID through.
	var projectUUID pgtype.UUID
	if strings.TrimSpace(req.ProjectID) != "" {
		pid, ok := parseUUIDOrBadRequest(w, req.ProjectID, "project_id")
		if !ok {
			return
		}
		if _, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{
			ID:          pid,
			WorkspaceID: wsUUID,
		}); err != nil {
			writeError(w, http.StatusBadRequest, "project not found")
			return
		}
		projectUUID = pid
	}

	// Optional parent_issue_id — validate same-workspace membership just like
	// the regular CreateIssue path. Frontend seeds this from the "Add sub
	// issue" entry, but the handler re-checks so a forged request can't
	// smuggle a foreign parent UUID through.
	var parentIssueUUID pgtype.UUID
	if strings.TrimSpace(req.ParentIssueID) != "" {
		pid, ok := parseUUIDOrBadRequest(w, req.ParentIssueID, "parent_issue_id")
		if !ok {
			return
		}
		parent, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
			ID:          pid,
			WorkspaceID: wsUUID,
		})
		if err != nil || !parent.ID.Valid {
			writeError(w, http.StatusBadRequest, "parent issue not found in this workspace")
			return
		}
		parentIssueUUID = pid
	}

	status := strings.TrimSpace(req.Status)
	if status != "" && !slices.Contains(validIssueStatuses, status) {
		writeError(w, http.StatusBadRequest, "invalid status")
		return
	}
	priority := strings.TrimSpace(req.Priority)
	if priority != "" && !slices.Contains(validIssuePriorities, priority) {
		writeError(w, http.StatusBadRequest, "invalid priority")
		return
	}
	assigneeType := strings.TrimSpace(req.AssigneeType)
	assigneeID := strings.TrimSpace(req.AssigneeID)
	var assigneeUUID pgtype.UUID
	if assigneeType != "" || assigneeID != "" {
		if assigneeType == "" || assigneeID == "" {
			writeError(w, http.StatusBadRequest, "assignee_type and assignee_id must be provided together")
			return
		}
		if !slices.Contains([]string{"member", "agent", "squad"}, assigneeType) {
			writeError(w, http.StatusBadRequest, "invalid assignee_type")
			return
		}
		parsed, ok := parseUUIDOrBadRequest(w, assigneeID, "assignee_id")
		if !ok {
			return
		}
		if statusCode, msg := h.validateAssigneePair(r.Context(), r, workspaceID, pgtype.Text{String: assigneeType, Valid: true}, parsed); statusCode != 0 {
			writeError(w, statusCode, msg)
			return
		}
		assigneeUUID = parsed
	}
	startDate := strings.TrimSpace(req.StartDate)
	if startDate != "" && !isDateOnly(startDate) {
		writeError(w, http.StatusBadRequest, "invalid start_date")
		return
	}
	dueDate := strings.TrimSpace(req.DueDate)
	if dueDate != "" && !isDateOnly(dueDate) {
		writeError(w, http.StatusBadRequest, "invalid due_date")
		return
	}

	if ref, ok := parseTAPDSourceURL(prompt); ok {
		resp, handled := h.quickCreateTAPDSourceIssue(r.Context(), w, quickCreateTAPDSourceIssueParams{
			Prompt:         prompt,
			Ref:            ref,
			WorkspaceID:    wsUUID,
			RequesterID:    requesterUUID,
			RequesterIDRaw: requesterID,
			HasSquad:       hasSquad,
			AgentID:        agentUUID,
			SquadID:        squadUUID,
			ProjectID:      projectUUID,
			ParentIssueID:  parentIssueUUID,
			AttachmentIDs:  attachmentIDs,
			Status:         status,
			Priority:       priority,
			AssigneeType:   assigneeType,
			AssigneeID:     assigneeUUID,
			StartDate:      startDate,
			DueDate:        dueDate,
		})
		if !handled {
			return
		}
		writeJSON(w, http.StatusCreated, resp)
		return
	}

	task, err := h.TaskService.EnqueueQuickCreateTask(r.Context(), service.EnqueueQuickCreateTaskParams{
		WorkspaceID:   wsUUID,
		RequesterID:   requesterUUID,
		AgentID:       agentUUID,
		SquadID:       squadUUID,
		Prompt:        prompt,
		ProjectID:     projectUUID,
		ParentIssueID: parentIssueUUID,
		AttachmentIDs: attachmentIDs,
		Status:        status,
		Priority:      priority,
		AssigneeType:  assigneeType,
		AssigneeID:    assigneeUUID,
		StartDate:     startDate,
		DueDate:       dueDate,
	})
	if err != nil {
		slog.Warn("quick-create enqueue failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to enqueue quick-create task")
		return
	}

	writeJSON(w, http.StatusAccepted, QuickCreateIssueResponse{TaskID: uuidToString(task.ID)})
}

type quickCreateTAPDSourceIssueParams struct {
	Prompt         string
	Ref            tapdSourceRef
	WorkspaceID    pgtype.UUID
	RequesterID    pgtype.UUID
	RequesterIDRaw string
	HasSquad       bool
	AgentID        pgtype.UUID
	SquadID        pgtype.UUID
	ProjectID      pgtype.UUID
	ParentIssueID  pgtype.UUID
	AttachmentIDs  []pgtype.UUID
	Status         string
	Priority       string
	AssigneeType   string
	AssigneeID     pgtype.UUID
	StartDate      string
	DueDate        string
}

func (h *Handler) quickCreateTAPDSourceIssue(ctx context.Context, w http.ResponseWriter, p quickCreateTAPDSourceIssueParams) (QuickCreateIssueResponse, bool) {
	fetchReq := RecordIssueSourceFetchRequest{
		Provider:     externalCredentialProviderTAPD,
		URL:          p.Ref.URL,
		WorkspaceID:  p.Ref.WorkspaceID,
		ResourceType: p.Ref.ResourceType,
		ResourceID:   p.Ref.ResourceID,
	}
	fetched, fetchErr := h.autoFetchTAPDSource(ctx, p.RequesterIDRaw, fetchReq, nil)
	if fetchErr != nil {
		fetched = fetchReq
		fetched.Provider = externalCredentialProviderTAPD
		fetched.FetchProvider = "tapd_mcp"
		fetched.Status = "fetch_failed"
		fetched.Error = fetchErr.Error()
	}

	metadata := map[string]json.RawMessage{}
	setRawMetadataString(metadata, "source_provider", externalCredentialProviderTAPD)
	setRawMetadataString(metadata, "source_url", p.Ref.URL)
	setRawMetadataString(metadata, "tapd_workspace_id", p.Ref.WorkspaceID)
	setRawMetadataString(metadata, "tapd_resource_type", p.Ref.ResourceType)
	setRawMetadataString(metadata, "tapd_resource_id", p.Ref.ResourceID)
	if p.Ref.ResourceType == "markdown_wiki" {
		setRawMetadataString(metadata, "tapd_wiki_id", p.Ref.ResourceID)
	}
	metadata = h.enrichSourceCredentialMetadata(ctx, metadata, p.RequesterIDRaw)
	for key, value := range sourceFetchMetadata(fetched) {
		raw, _ := json.Marshal(value)
		metadata[key] = raw
	}
	if fetchErr == nil {
		setRawMetadataString(metadata, "source_summary_status", "pending")
		setRawMetadataString(metadata, "source_summary_mode", "agent_task")
	}
	validatedMetadata, err := validateIssueMetadataObject(metadata)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return QuickCreateIssueResponse{}, false
	}

	issueStatus := firstNonEmpty(p.Status, "todo")
	if fetchErr != nil {
		issueStatus = "blocked"
	}
	priority := firstNonEmpty(p.Priority, "none")
	title := firstNonEmpty(fetched.Title, tapdSourceReadFailureTitle(p.Ref))
	description := buildQuickCreateTAPDDescription(fetched, fetchErr)

	assigneeType := p.AssigneeType
	assigneeID := p.AssigneeID
	if assigneeType == "" || !assigneeID.Valid {
		if p.HasSquad {
			assigneeType = "squad"
			assigneeID = p.SquadID
		} else {
			assigneeType = "agent"
			assigneeID = p.AgentID
		}
	}

	startDate := pgtype.Date{}
	if p.StartDate != "" {
		parsed, err := util.ParseCalendarDate(p.StartDate)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid start_date format, expected YYYY-MM-DD")
			return QuickCreateIssueResponse{}, false
		}
		startDate = parsed
	}
	dueDate := pgtype.Date{}
	if p.DueDate != "" {
		parsed, err := util.ParseCalendarDate(p.DueDate)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid due_date format, expected YYYY-MM-DD")
			return QuickCreateIssueResponse{}, false
		}
		dueDate = parsed
	}

	prefix := h.getIssuePrefix(ctx, p.WorkspaceID)
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
	res, err := h.IssueService.Create(ctx, service.IssueCreateParams{
		WorkspaceID:   p.WorkspaceID,
		Title:         title,
		Description:   pgtype.Text{String: description, Valid: true},
		Status:        issueStatus,
		Priority:      priority,
		AssigneeType:  pgtype.Text{String: assigneeType, Valid: assigneeType != ""},
		AssigneeID:    assigneeID,
		CreatorType:   "member",
		CreatorID:     p.RequesterID,
		ParentIssueID: p.ParentIssueID,
		ProjectID:     p.ProjectID,
		StartDate:     startDate,
		DueDate:       dueDate,
		AttachmentIDs: p.AttachmentIDs,
		Metadata:      validatedMetadata,
	}, service.IssueCreateOpts{
		ActorID:             p.RequesterIDRaw,
		Platform:            "web",
		SuppressAutoEnqueue: true,
		BroadcastPayload: func(issue db.Issue, atts []db.Attachment) map[string]any {
			payload := issueToResponse(issue, prefix)
			payload.Attachments = buildAttachmentResponses(atts)
			return map[string]any{"issue": payload}
		},
	})
	if errors.Is(err, service.ErrParentIssueNotFound) {
		writeError(w, http.StatusBadRequest, "parent issue not found in this workspace")
		return QuickCreateIssueResponse{}, false
	}
	if errors.Is(err, service.ErrProjectNotFound) {
		writeError(w, http.StatusBadRequest, "project not found in this workspace")
		return QuickCreateIssueResponse{}, false
	}
	if err != nil {
		slog.Warn("quick-create TAPD issue create failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create TAPD issue: "+err.Error())
		return QuickCreateIssueResponse{}, false
	}
	if fetchErr == nil {
		if h.TaskService == nil {
			h.applyQuickCreateTAPDSourceSummaryFallback(ctx, res.Issue, fetched, errors.New("task service unavailable"))
			h.IssueService.EnqueueOnAssignForIssue(ctx, res.Issue, "member", p.RequesterIDRaw)
			return QuickCreateIssueResponse{
				IssueID:           uuidToString(res.Issue.ID),
				Identifier:        issueToResponse(res.Issue, prefix).Identifier,
				SourceFetchStatus: fetched.Status,
			}, true
		}
		task, err := h.TaskService.EnqueueIssueSourceSummaryTask(ctx, res.Issue, p.AgentID)
		if err != nil {
			slog.Warn("quick-create TAPD source summary task enqueue failed",
				"issue_id", uuidToString(res.Issue.ID),
				"agent_id", uuidToString(p.AgentID),
				"error", err,
			)
			h.applyQuickCreateTAPDSourceSummaryFallback(ctx, res.Issue, fetched, err)
			h.IssueService.EnqueueOnAssignForIssue(ctx, res.Issue, "member", p.RequesterIDRaw)
		} else if _, err := h.Queries.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{
			ID:          res.Issue.ID,
			WorkspaceID: res.Issue.WorkspaceID,
			Key:         "source_summary_task_id",
			Value:       jsonStringBytes(uuidToString(task.ID)),
		}); err != nil {
			slog.Warn("quick-create TAPD source summary task metadata write failed",
				"issue_id", uuidToString(res.Issue.ID),
				"task_id", uuidToString(task.ID),
				"error", err,
			)
		}
	}

	return QuickCreateIssueResponse{
		IssueID:           uuidToString(res.Issue.ID),
		Identifier:        issueToResponse(res.Issue, prefix).Identifier,
		SourceFetchStatus: fetched.Status,
	}, true
}

func buildQuickCreateTAPDDescription(fetched RecordIssueSourceFetchRequest, fetchErr error) string {
	if fetchErr != nil {
		var b strings.Builder
		b.WriteString("## 来源抓取失败\n")
		b.WriteString(fetched.Error)
		b.WriteString("\n")
		return b.String()
	}
	return buildQuickCreateTAPDSummaryPendingDescription()
}

func buildQuickCreateTAPDSummaryPendingDescription() string {
	var b strings.Builder
	b.WriteString("## 需求摘要\n")
	b.WriteString("摘要生成中，系统正在基于 TAPD 来源生成可执行的需求摘要。\n")
	return strings.TrimSpace(b.String())
}

func buildQuickCreateTAPDLocalSummaryDescription(fetched RecordIssueSourceFetchRequest) string {
	body := strings.TrimSpace(firstNonEmpty(fetched.BodyExcerpt, fetched.Summary))
	if len([]rune(body)) > 900 {
		body = string([]rune(body)[:900]) + "..."
	}
	var b strings.Builder
	b.WriteString("## 需求摘要\n")
	if fetched.Title != "" {
		b.WriteString(fetched.Title)
		if body != "" && body != fetched.Title {
			b.WriteString("\n\n")
			b.WriteString(body)
		}
	} else if body != "" {
		b.WriteString(body)
	} else {
		b.WriteString("TAPD 来源未返回可用于摘要的正文。")
	}
	return strings.TrimSpace(b.String())
}

func (h *Handler) applyQuickCreateTAPDSourceSummaryFallback(ctx context.Context, issue db.Issue, fetched RecordIssueSourceFetchRequest, cause error) {
	description := buildQuickCreateTAPDLocalSummaryDescription(fetched)
	updated, err := h.Queries.UpdateIssue(ctx, db.UpdateIssueParams{
		ID:            issue.ID,
		Description:   pgtype.Text{String: description, Valid: true},
		AssigneeType:  issue.AssigneeType,
		AssigneeID:    issue.AssigneeID,
		StartDate:     issue.StartDate,
		DueDate:       issue.DueDate,
		ParentIssueID: issue.ParentIssueID,
		ProjectID:     issue.ProjectID,
	})
	if err != nil {
		slog.Warn("quick-create TAPD source summary fallback update failed",
			"issue_id", uuidToString(issue.ID),
			"error", err,
		)
		return
	}
	if _, err := h.Queries.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{
		ID:          issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Key:         "source_summary_status",
		Value:       jsonStringBytes("failed"),
	}); err != nil {
		slog.Warn("quick-create TAPD source summary fallback metadata failed",
			"issue_id", uuidToString(issue.ID),
			"key", "source_summary_status",
			"error", err,
		)
	}
	if cause != nil {
		if _, err := h.Queries.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{
			ID:          issue.ID,
			WorkspaceID: issue.WorkspaceID,
			Key:         "source_summary_error",
			Value:       jsonStringBytes(cause.Error()),
		}); err != nil {
			slog.Warn("quick-create TAPD source summary fallback metadata failed",
				"issue_id", uuidToString(issue.ID),
				"key", "source_summary_error",
				"error", err,
			)
		}
	}
	prefix := h.getIssuePrefix(ctx, issue.WorkspaceID)
	h.publish(protocol.EventIssueUpdated, uuidToString(issue.WorkspaceID), "system", "", map[string]any{
		"issue":               issueToResponse(updated, prefix),
		"description_changed": true,
	})
}

func tapdSourceReadFailureTitle(ref tapdSourceRef) string {
	switch ref.ResourceType {
	case "story":
		return "TAPD Story 读取失败：" + ref.ResourceID
	case "task":
		return "TAPD Task 读取失败：" + ref.ResourceID
	default:
		return "TAPD Wiki 读取失败：" + ref.ResourceID
	}
}

func setRawMetadataString(metadata map[string]json.RawMessage, key, value string) {
	raw, _ := json.Marshal(value)
	metadata[key] = raw
}

func jsonStringBytes(value string) []byte {
	raw, _ := json.Marshal(value)
	return raw
}

// writeAgentUnavailable returns 422 with a stable error code so the modal
// can show a "switch agent" hint without parsing the human-readable reason.
func writeAgentUnavailable(w http.ResponseWriter, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnprocessableEntity)
	json.NewEncoder(w).Encode(map[string]any{
		"code":   "agent_unavailable",
		"reason": reason,
	})
}

func isDateOnly(value string) bool {
	_, err := time.Parse("2006-01-02", value)
	return err == nil
}

// isRuntimeOnline returns true when the given runtime is currently
// reachable (status == "online"). Quick-create rejects submissions whose
// agent's runtime is offline so the user gets immediate feedback in the
// modal instead of an inbox failure twenty seconds later.
func (h *Handler) isRuntimeOnline(ctx context.Context, runtimeID pgtype.UUID) bool {
	rt, err := h.Queries.GetAgentRuntime(ctx, runtimeID)
	if err != nil {
		return false
	}
	return rt.Status == "online"
}

// checkQuickCreateDaemonVersion enforces MinQuickCreateCLIVersion against the
// CLI version the daemon reported at registration time (stored on the runtime
// row's metadata.cli_version). Returns (0, nil) when the version is
// acceptable, otherwise (status, payload) ready to hand to writeJSON.
//
// Failure shape is stable so the modal can branch on the `code` field and
// surface a "needs upgrade" hint that points at the specific runtime:
//
//	422 {
//	  "code": "daemon_version_unsupported",
//	  "current_version": "0.2.18" | "",
//	  "min_version":     "0.2.21",
//	  "runtime_id":      "<uuid>"
//	}
func (h *Handler) checkQuickCreateDaemonVersion(ctx context.Context, runtimeID pgtype.UUID) (int, map[string]any) {
	rt, err := h.Queries.GetAgentRuntime(ctx, runtimeID)
	if err != nil {
		// Runtime row vanished between the online check and here — treat
		// as unavailable rather than wedging the request on a 500.
		return http.StatusUnprocessableEntity, map[string]any{
			"code":   "agent_unavailable",
			"reason": "agent's runtime is no longer registered",
		}
	}
	current := readRuntimeCLIVersion(rt.Metadata)
	switch err := agent.CheckMinCLIVersion(current); {
	case err == nil:
		return 0, nil
	case errors.Is(err, agent.ErrCLIVersionMissing), errors.Is(err, agent.ErrCLIVersionTooOld):
		return http.StatusUnprocessableEntity, map[string]any{
			"code":            "daemon_version_unsupported",
			"current_version": current,
			"min_version":     agent.MinQuickCreateCLIVersion,
			"runtime_id":      uuidToString(runtimeID),
		}
	default:
		// Defensive fall-through: unknown error from the version check is
		// also fail-closed, since the gate exists precisely because we
		// can't trust older daemons with this flow.
		return http.StatusUnprocessableEntity, map[string]any{
			"code":            "daemon_version_unsupported",
			"current_version": current,
			"min_version":     agent.MinQuickCreateCLIVersion,
			"runtime_id":      uuidToString(runtimeID),
		}
	}
}

// readRuntimeCLIVersion pulls metadata.cli_version off a runtime row. The
// metadata column is JSONB on the wire; the daemon stores the multica CLI
// version under that key during registration (see DaemonRegister).
func readRuntimeCLIVersion(metadata []byte) string {
	if len(metadata) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(metadata, &m); err != nil {
		return ""
	}
	if v, ok := m["cli_version"].(string); ok {
		return v
	}
	return ""
}

type CreateIssueRequest struct {
	Title         string                     `json:"title"`
	Description   *string                    `json:"description"`
	Status        string                     `json:"status"`
	Priority      string                     `json:"priority"`
	AssigneeType  *string                    `json:"assignee_type"`
	AssigneeID    *string                    `json:"assignee_id"`
	ParentIssueID *string                    `json:"parent_issue_id"`
	ProjectID     *string                    `json:"project_id"`
	StartDate     *string                    `json:"start_date"`
	DueDate       *string                    `json:"due_date"`
	AttachmentIDs []string                   `json:"attachment_ids,omitempty"`
	Metadata      map[string]json.RawMessage `json:"metadata,omitempty"`
	// OriginType / OriginID stamp the new issue with its provenance so
	// platform-internal flows can deterministically locate it later. Only
	// trusted callers should set these — currently the daemon CLI passes
	// them through for quick-create tasks (origin_type=quick_create,
	// origin_id=agent_task_queue.id).
	OriginType *string `json:"origin_type,omitempty"`
	OriginID   *string `json:"origin_id,omitempty"`
}

