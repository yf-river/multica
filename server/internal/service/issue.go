package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/issueposition"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// IssueService is the single service-layer entry point for creating issues.
// Both the HTTP `POST /issues` handler and the future Lark `/issue` command
// call into Create so that issue numbering, attachment linking, broadcast,
// analytics, and agent/squad enqueue stay aligned. The
// service deliberately does NOT depend on http.Request — callers parse
// their own transport and pass a fully-resolved IssueCreateParams.
type IssueService struct {
	Queries   *db.Queries
	TxStarter TxStarter
	Bus       *events.Bus
	Analytics analytics.Client
	// Metrics is the shared business-metrics collector. Wired by
	// cmd/server/router.go after construction; nil in tests / self-hosted
	// without the metrics listener — obsmetrics.RecordEvent treats a nil
	// Metrics as "PostHog only", so leaving it unset is safe.
	Metrics     *obsmetrics.BusinessMetrics
	TaskService *TaskService
}

func NewIssueService(q *db.Queries, tx TxStarter, bus *events.Bus, ac analytics.Client, ts *TaskService) *IssueService {
	return &IssueService{
		Queries:     q,
		TxStarter:   tx,
		Bus:         bus,
		Analytics:   ac,
		TaskService: ts,
	}
}

// IssueCreateParams carries the already-validated, already-resolved inputs
// to IssueService.Create. The handler owns the parsing step that turns its
// request payload into this struct; the service stays transport-agnostic.
type IssueCreateParams struct {
	WorkspaceID   pgtype.UUID
	Title         string
	Description   pgtype.Text
	Status        string
	Priority      string
	AssigneeType  pgtype.Text
	AssigneeID    pgtype.UUID
	CreatorType   string // "agent" or "member"
	CreatorID     pgtype.UUID
	ParentIssueID pgtype.UUID
	ProjectID     pgtype.UUID
	StartDate     pgtype.Date
	DueDate       pgtype.Date
	OriginType    pgtype.Text
	OriginID      pgtype.UUID
	AttachmentIDs []pgtype.UUID
	// Metadata is a handler-validated flat KV map written in the same
	// transaction as the issue row so broadcasts and HTTP responses agree.
	Metadata map[string][]byte
}

// IssueCreateOpts groups optional knobs for IssueService.Create. Most
// callers leave it zero-valued.
type IssueCreateOpts struct {
	// BroadcastPayload, if non-nil, is invoked after the issue row is
	// created and attachments are linked, but before the transaction commits.
	// It must be a pure conversion with no external I/O. Its return value
	// becomes both the durable EventIssueCreated payload and the immediate
	// event-bus payload. The HTTP handler uses this hook to include
	// transport-only response fields without depending on handler types.
	BroadcastPayload func(issue db.Issue, attachments []db.Attachment) map[string]any

	// ActorID overrides the actor ID used for broadcast + analytics
	// when it differs from the creator on the row. Agent-created issues
	// use the agent UUID here (the creator_id column is the daemon
	// owner). Empty falls back to CreatorID.
	ActorID string

	// AnalyticsAgentID is the agent associated with the issue for
	// analytics purposes (assignee agent or, for agent-created issues,
	// the creator agent). Resolved by the caller because it depends on
	// transport context.
	AnalyticsAgentID string

	// Platform tags the IssueCreated analytics + business-metrics event
	// with the client surface the request came in on (web / desktop /
	// daemon / lark / autopilot). Derived from middleware's client
	// metadata at the handler layer.
	Platform string

	// SuppressAutoEnqueue lets callers create the issue and finish
	// platform-side preparation before the assignee starts. TAPD quick-create
	// uses this to fetch source content first so the squad PM sees a fetched
	// source_context on its first task.
	SuppressAutoEnqueue bool
}

// ErrParentIssueNotFound signals that the supplied ParentIssueID does
// not exist in the issue's workspace. The service refuses to create
// orphaned or cross-workspace child issues; callers translate this into
// their transport's 400 / Lark card error.
var ErrParentIssueNotFound = errors.New("parent issue not found in this workspace")

// ErrProjectNotFound signals that the supplied ProjectID does not exist
// in the issue's workspace. Cross-workspace project IDs are rejected
// here so every create entry (HTTP `POST /issues`, Lark `/issue`, future
// MCP / API key callers) enforces the same workspace boundary without
// having to remember it. Callers translate this into 400.
var ErrProjectNotFound = errors.New("project not found in this workspace")

// ErrAttachmentsUnavailable means at least one requested attachment does not
// exist as an unbound row in the issue's workspace. Issue creation is rolled
// back rather than silently succeeding without the user's selected files.
var ErrAttachmentsUnavailable = errors.New("one or more attachments are unavailable in this workspace")

// IssueCreateResult is the typed return from IssueService.Create.
type IssueCreateResult struct {
	Issue       db.Issue
	Attachments []db.Attachment
}

// Create runs the full issue-creation pipeline atomically end-to-end:
//
//  1. Begin transaction.
//  2. Resolve & validate parent / project belong to the same workspace.
//  3. Increment the workspace issue counter.
//  4. Insert the issue row (with optional origin stamping).
//  5. Link every requested pre-uploaded attachment.
//  6. Commit the issue and attachment links together.
//  7. Publish EventIssueCreated to the bus (payload via opts.BroadcastPayload).
//  8. Capture the IssueCreated analytics event.
//  9. Enqueue an agent task or trigger the squad leader when the issue is
//     assigned and not in `backlog`.
//
// Validation that lives in the service (parent existence, project
// workspace membership, parent → project back-fill) is enforced here so
// every create entry — HTTP `POST /issues`, Lark `/issue`, future
// MCP/API-key callers — shares the same workspace boundary semantics.
// Caller-owned validation is limited to transport-shaped checks: title
// required, RFC3339 date format, assignee pair sanity.
func (s *IssueService) Create(ctx context.Context, p IssueCreateParams, opts IssueCreateOpts) (IssueCreateResult, error) {
	requestedStatus := p.Status
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return IssueCreateResult{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.Queries.WithTx(tx)

	// Resolve and validate parent / project before touching the issue
	// counter. Both checks scope by WorkspaceID — there is no path from
	// this service to a row in a foreign workspace.
	projectID := p.ProjectID
	var project db.Project
	var hasProject bool
	if p.ParentIssueID.Valid {
		parent, err := qtx.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
			ID:          p.ParentIssueID,
			WorkspaceID: p.WorkspaceID,
		})
		if err != nil || !parent.ID.Valid {
			return IssueCreateResult{}, ErrParentIssueNotFound
		}
		// Back-fill project from parent when the caller did not pin
		// one explicitly. Matches the long-standing HTTP behavior: a
		// sub-issue inherits its parent's project unless overridden.
		if !projectID.Valid {
			projectID = parent.ProjectID
		}
	}
	if projectID.Valid {
		var err error
		project, err = qtx.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{
			ID:          projectID,
			WorkspaceID: p.WorkspaceID,
		})
		if err != nil {
			return IssueCreateResult{}, ErrProjectNotFound
		}
		hasProject = true
	}

	assigneeType := p.AssigneeType
	assigneeID := p.AssigneeID
	if !assigneeType.Valid && !assigneeID.Valid && hasProject {
		assigneeType, assigneeID = s.defaultProjectLeadAssignee(ctx, qtx, project)
	}
	agentLeadReviewRequested := false
	if requestedStatus == "backlog" && hasProject && project.LeadType.Valid && project.LeadType.String == "agent" && project.LeadID.Valid {
		if leadAgent, err := qtx.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
			ID:          project.LeadID,
			WorkspaceID: project.WorkspaceID,
		}); err == nil && !leadAgent.ArchivedAt.Valid {
			agentLeadReviewRequested = true
			if p.Metadata == nil {
				p.Metadata = map[string][]byte{}
			}
			setMetadataString(p.Metadata, "project_owner_approval_status", "pending")
			setMetadataString(p.Metadata, "project_owner_approval_mode", "agent_review_task")
			setMetadataString(p.Metadata, "project_owner_reviewer_type", "agent")
			setMetadataString(p.Metadata, "project_owner_reviewer_id", util.UUIDToString(project.LeadID))
		}
	}

	issueNumber, err := qtx.IncrementIssueCounter(ctx, p.WorkspaceID)
	if err != nil {
		return IssueCreateResult{}, fmt.Errorf("increment counter: %w", err)
	}

	// New issues sort to the top of their (workspace, status) column for
	// manual ordering. Computed inside the tx, after IncrementIssueCounter
	// has already taken the workspace row lock, so two concurrent creates
	// in the same workspace see each other's positions and don't both
	// land on the same min-1 slot. Concurrent manual reorder via
	// UpdateIssue(position) does NOT take this lock, so a create racing
	// a reorder is still allowed to collide on position — manual ordering
	// is best-effort and the UI tolerates equal positions by falling back
	// to the secondary ORDER BY key.
	newPosition, err := issueposition.NextTopPosition(ctx, tx, p.WorkspaceID, p.Status)
	if err != nil {
		return IssueCreateResult{}, fmt.Errorf("next top position: %w", err)
	}

	var issue db.Issue
	if p.OriginType.Valid {
		issue, err = qtx.CreateIssueWithOrigin(ctx, db.CreateIssueWithOriginParams{
			WorkspaceID:   p.WorkspaceID,
			Title:         p.Title,
			Description:   p.Description,
			Status:        p.Status,
			Priority:      p.Priority,
			AssigneeType:  assigneeType,
			AssigneeID:    assigneeID,
			CreatorType:   p.CreatorType,
			CreatorID:     p.CreatorID,
			ParentIssueID: p.ParentIssueID,
			Position:      newPosition,
			StartDate:     p.StartDate,
			DueDate:       p.DueDate,
			Number:        issueNumber,
			ProjectID:     projectID,
			OriginType:    p.OriginType,
			OriginID:      p.OriginID,
		})
	} else {
		issue, err = qtx.CreateIssue(ctx, db.CreateIssueParams{
			WorkspaceID:   p.WorkspaceID,
			Title:         p.Title,
			Description:   p.Description,
			Status:        p.Status,
			Priority:      p.Priority,
			AssigneeType:  assigneeType,
			AssigneeID:    assigneeID,
			CreatorType:   p.CreatorType,
			CreatorID:     p.CreatorID,
			ParentIssueID: p.ParentIssueID,
			Position:      newPosition,
			StartDate:     p.StartDate,
			DueDate:       p.DueDate,
			Number:        issueNumber,
			ProjectID:     projectID,
		})
	}
	if err != nil {
		return IssueCreateResult{}, fmt.Errorf("create issue: %w", err)
	}
	for key, value := range p.Metadata {
		issue, err = qtx.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{
			ID:          issue.ID,
			WorkspaceID: issue.WorkspaceID,
			Key:         key,
			Value:       value,
		})
		if err != nil {
			return IssueCreateResult{}, fmt.Errorf("set issue metadata %q: %w", key, err)
		}
	}

	attachments, err := linkAttachmentsForNewIssue(ctx, qtx, issue, p.AttachmentIDs)
	if err != nil {
		return IssueCreateResult{}, err
	}

	actorID := opts.ActorID
	if actorID == "" {
		actorID = util.UUIDToString(issue.CreatorID)
	}
	createdEvent := s.buildIssueCreatedEvent(issue, attachments, p.CreatorType, actorID, opts)
	createdEvent, err = eventoutbox.Enqueue(ctx, qtx, createdEvent)
	if err != nil {
		return IssueCreateResult{}, fmt.Errorf("enqueue issue-created event: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return IssueCreateResult{}, fmt.Errorf("commit: %w", err)
	}

	if s.Bus != nil {
		s.Bus.Publish(createdEvent)
	}

	s.captureCreatedAnalytics(issue, p.CreatorType, actorID, opts)
	if requestedStatus == "backlog" && hasProject && agentLeadReviewRequested {
		s.enqueueProjectOwnerApprovalTask(ctx, issue, project)
	} else if requestedStatus == "backlog" && hasProject {
		s.notifyProjectLeadApprovalRequested(ctx, project, issue, p.CreatorType, actorID)
	}
	if !opts.SuppressAutoEnqueue {
		s.maybeEnqueueOnAssign(ctx, issue, p.CreatorType, actorID)
	}

	return IssueCreateResult{Issue: issue, Attachments: attachments}, nil
}

// EnqueueOnAssignForIssue triggers the same create-time assignee behavior
// after a caller deliberately suppressed auto-enqueue during Create.
func (s *IssueService) EnqueueOnAssignForIssue(ctx context.Context, issue db.Issue, actorType, actorID string) {
	s.maybeEnqueueOnAssign(ctx, issue, actorType, actorID)
}

// EnsureProjectOwnerApprovalForBacklog reconciles the approval gate when an
// existing issue is moved or corrected into backlog. Create already handles
// status=backlog, but agent handoffs can create an issue as todo and then fix
// project/status/assignee in a follow-up update; that path must not bypass the
// project owner gate.
func (s *IssueService) EnsureProjectOwnerApprovalForBacklog(ctx context.Context, issue db.Issue, actorType, actorID string) {
	if issue.Status != "backlog" || !issue.ProjectID.Valid {
		return
	}
	project, err := s.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{
		ID:          issue.ProjectID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		slog.Warn("project owner approval reconcile skipped: project lookup failed",
			"issue_id", util.UUIDToString(issue.ID),
			"project_id", util.UUIDToString(issue.ProjectID),
			"error", err)
		return
	}
	if !project.LeadType.Valid || !project.LeadID.Valid {
		return
	}
	switch project.LeadType.String {
	case "agent":
		if issueMetadataString(issue.Metadata, "project_owner_approval_status") == "pending" {
			return
		}
		leadAgent, err := s.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
			ID:          project.LeadID,
			WorkspaceID: project.WorkspaceID,
		})
		if err != nil || leadAgent.ArchivedAt.Valid {
			return
		}
		for key, value := range map[string]string{
			"project_owner_approval_status": "pending",
			"project_owner_approval_mode":   "agent_review_task",
			"project_owner_reviewer_type":   "agent",
			"project_owner_reviewer_id":     util.UUIDToString(project.LeadID),
		} {
			if _, err := s.Queries.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{
				ID:          issue.ID,
				WorkspaceID: issue.WorkspaceID,
				Key:         key,
				Value:       mustJSONStringBytes(value),
			}); err != nil {
				slog.Warn("project owner approval metadata reconcile failed",
					"issue_id", util.UUIDToString(issue.ID),
					"project_id", util.UUIDToString(project.ID),
					"key", key,
					"error", err)
				return
			}
		}
		s.enqueueProjectOwnerApprovalTask(ctx, issue, project)
	case "member":
		if s.hasOpenProjectLeadApprovalInbox(ctx, project, issue) {
			return
		}
		s.notifyProjectLeadApprovalRequested(ctx, project, issue, actorType, actorID)
	}
}

func (s *IssueService) enqueueProjectOwnerApprovalTask(ctx context.Context, issue db.Issue, project db.Project) {
	if s.TaskService == nil {
		return
	}
	task, err := s.TaskService.EnqueueProjectOwnerApprovalTask(ctx, issue, project)
	if err != nil {
		slog.Warn("project owner approval task enqueue failed",
			"project_id", util.UUIDToString(project.ID),
			"issue_id", util.UUIDToString(issue.ID),
			"lead_id", util.UUIDToString(project.LeadID),
			"error", err)
		return
	}
	updated, err := s.Queries.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{
		ID:          issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Key:         "project_owner_review_task_id",
		Value:       mustJSONStringBytes(util.UUIDToString(task.ID)),
	})
	if err != nil {
		slog.Warn("project owner approval task metadata write failed",
			"project_id", util.UUIDToString(project.ID),
			"issue_id", util.UUIDToString(issue.ID),
			"task_id", util.UUIDToString(task.ID),
			"error", err)
		return
	}
	if s.Bus != nil {
		s.Bus.Publish(events.Event{
			Type:        protocol.EventIssueMetadataChanged,
			WorkspaceID: util.UUIDToString(issue.WorkspaceID),
			ActorType:   "system",
			ActorID:     "",
			Payload: map[string]any{
				"issue_id": util.UUIDToString(issue.ID),
				"issue":    updated,
				"metadata": map[string]any{"project_owner_review_task_id": util.UUIDToString(task.ID)},
			},
		})
	}
}

func setMetadataString(metadata map[string][]byte, key, value string) {
	raw, _ := json.Marshal(value)
	metadata[key] = raw
}

func issueMetadataString(metadata []byte, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(metadata, &values); err != nil {
		return ""
	}
	raw, ok := values[key]
	if !ok {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func mustJSONStringBytes(value string) []byte {
	raw, _ := json.Marshal(value)
	return raw
}

func (s *IssueService) hasOpenProjectLeadApprovalInbox(ctx context.Context, project db.Project, issue db.Issue) bool {
	if !project.LeadID.Valid {
		return false
	}
	items, err := s.Queries.ListInboxItems(ctx, db.ListInboxItemsParams{
		WorkspaceID:   issue.WorkspaceID,
		RecipientType: "member",
		RecipientID:   project.LeadID,
	})
	if err != nil {
		slog.Warn("project owner approval inbox lookup failed",
			"issue_id", util.UUIDToString(issue.ID),
			"project_id", util.UUIDToString(project.ID),
			"recipient_id", util.UUIDToString(project.LeadID),
			"error", err)
		return false
	}
	for _, item := range items {
		if item.IssueID.Valid && item.IssueID == issue.ID && item.Type == "project_issue_approval_requested" {
			return true
		}
	}
	return false
}

func (s *IssueService) notifyProjectLeadApprovalRequested(ctx context.Context, project db.Project, issue db.Issue, actorType, actorID string) {
	if s.Bus == nil || !project.LeadType.Valid || !project.LeadID.Valid || project.LeadType.String != "member" {
		return
	}
	details, _ := json.Marshal(map[string]string{
		"project_id":    util.UUIDToString(project.ID),
		"project_title": project.Title,
		"reason":        "project_backlog_approval",
	})
	item, err := s.Queries.CreateInboxItem(ctx, db.CreateInboxItemParams{
		WorkspaceID:   issue.WorkspaceID,
		RecipientType: "member",
		RecipientID:   project.LeadID,
		Type:          "project_issue_approval_requested",
		Severity:      "info",
		IssueID:       issue.ID,
		Title:         issue.Title,
		Body:          pgtype.Text{String: "Project backlog issue is waiting for owner approval.", Valid: true},
		ActorType:     pgtype.Text{String: actorType, Valid: actorType != ""},
		ActorID:       parseServiceUUID(actorID),
		Details:       details,
	})
	if err != nil {
		slog.Error("project lead approval inbox write failed",
			"project_id", util.UUIDToString(project.ID),
			"issue_id", util.UUIDToString(issue.ID),
			"recipient_id", util.UUIDToString(project.LeadID),
			"error", err,
		)
		return
	}
	s.Bus.Publish(events.Event{
		Type:        protocol.EventInboxNew,
		WorkspaceID: util.UUIDToString(issue.WorkspaceID),
		ActorType:   actorType,
		ActorID:     actorID,
		Payload: map[string]any{
			"item": map[string]any{
				"id":             util.UUIDToString(item.ID),
				"workspace_id":   util.UUIDToString(item.WorkspaceID),
				"recipient_type": item.RecipientType,
				"recipient_id":   util.UUIDToString(item.RecipientID),
				"type":           item.Type,
				"severity":       item.Severity,
				"issue_id":       util.UUIDToPtr(item.IssueID),
				"issue_status":   issue.Status,
				"title":          item.Title,
				"body":           util.TextToPtr(item.Body),
				"read":           item.Read,
				"archived":       item.Archived,
				"created_at":     util.TimestampToString(item.CreatedAt),
				"actor_type":     util.TextToPtr(item.ActorType),
				"actor_id":       util.UUIDToPtr(item.ActorID),
				"details":        json.RawMessage(item.Details),
			},
		},
	})
}

func parseServiceUUID(value string) pgtype.UUID {
	id, err := util.ParseUUID(value)
	if err != nil {
		return pgtype.UUID{}
	}
	return id
}

func (s *IssueService) defaultProjectLeadAssignee(ctx context.Context, q *db.Queries, project db.Project) (pgtype.Text, pgtype.UUID) {
	if !project.LeadType.Valid || !project.LeadID.Valid {
		return pgtype.Text{}, pgtype.UUID{}
	}
	switch project.LeadType.String {
	case "member":
		if _, err := q.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
			UserID:      project.LeadID,
			WorkspaceID: project.WorkspaceID,
		}); err != nil {
			return pgtype.Text{}, pgtype.UUID{}
		}
	case "agent":
		agent, err := q.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
			ID:          project.LeadID,
			WorkspaceID: project.WorkspaceID,
		})
		if err != nil || agent.ArchivedAt.Valid {
			return pgtype.Text{}, pgtype.UUID{}
		}
	default:
		return pgtype.Text{}, pgtype.UUID{}
	}
	return project.LeadType, project.LeadID
}

// linkAttachmentsForNewIssue claims every requested attachment inside the
// issue creation transaction. A partial match is a failed create: otherwise
// the API would acknowledge an issue while silently dropping user files.
func linkAttachmentsForNewIssue(ctx context.Context, q *db.Queries, issue db.Issue, ids []pgtype.UUID) ([]db.Attachment, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	uniqueIDs := make([]pgtype.UUID, 0, len(ids))
	seen := make(map[[16]byte]struct{}, len(ids))
	for _, id := range ids {
		if !id.Valid {
			return nil, ErrAttachmentsUnavailable
		}
		if _, exists := seen[id.Bytes]; exists {
			continue
		}
		seen[id.Bytes] = struct{}{}
		uniqueIDs = append(uniqueIDs, id)
	}

	linkedIDs, err := q.LinkAttachmentsToIssue(ctx, db.LinkAttachmentsToIssueParams{
		IssueID:       issue.ID,
		WorkspaceID:   issue.WorkspaceID,
		AttachmentIds: uniqueIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("link attachments to issue: %w", err)
	}
	if len(linkedIDs) != len(uniqueIDs) {
		return nil, ErrAttachmentsUnavailable
	}

	attachments, err := q.ListAttachmentsByIssue(ctx, db.ListAttachmentsByIssueParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return nil, fmt.Errorf("list linked attachments: %w", err)
	}
	return attachments, nil
}

func (s *IssueService) buildIssueCreatedEvent(issue db.Issue, attachments []db.Attachment, creatorType, actorID string, opts IssueCreateOpts) events.Event {
	var payload map[string]any
	if opts.BroadcastPayload != nil {
		payload = opts.BroadcastPayload(issue, attachments)
	} else {
		payload = map[string]any{"issue": issueCreatedProjection(issue)}
	}
	return events.Event{
		Type:        protocol.EventIssueCreated,
		StreamKey:   "issue:" + util.UUIDToString(issue.ID),
		WorkspaceID: util.UUIDToString(issue.WorkspaceID),
		ActorType:   creatorType,
		ActorID:     actorID,
		Payload:     payload,
	}
}

// issueCreatedProjection is the transport-neutral subset required by durable
// subscribers, notifications, and activity projections. Non-HTTP producers
// must emit the same semantic event shape as HTTP producers; a bare issue ID
// cannot be replayed into those consumers after a process restart.
func issueCreatedProjection(issue db.Issue) map[string]any {
	return map[string]any{
		"id":              util.UUIDToString(issue.ID),
		"workspace_id":    util.UUIDToString(issue.WorkspaceID),
		"title":           issue.Title,
		"description":     util.TextToPtr(issue.Description),
		"status":          issue.Status,
		"priority":        issue.Priority,
		"assignee_type":   util.TextToPtr(issue.AssigneeType),
		"assignee_id":     util.UUIDToPtr(issue.AssigneeID),
		"creator_type":    issue.CreatorType,
		"creator_id":      util.UUIDToString(issue.CreatorID),
		"start_date":      util.DateToPtr(issue.StartDate),
		"due_date":        util.DateToPtr(issue.DueDate),
		"parent_issue_id": util.UUIDToPtr(issue.ParentIssueID),
	}
}

func (s *IssueService) captureCreatedAnalytics(issue db.Issue, creatorType, actorID string, opts IssueCreateOpts) {
	if s.Analytics == nil {
		return
	}
	source, taskID, autopilotRunID := classifyOrigin(issue, opts)
	analyticsActorID := actorID
	if creatorType == "agent" {
		analyticsActorID = "agent:" + actorID
	}
	obsmetrics.RecordEvent(s.Analytics, s.Metrics, analytics.IssueCreated(
		analyticsActorID,
		util.UUIDToString(issue.WorkspaceID),
		util.UUIDToString(issue.ID),
		opts.AnalyticsAgentID,
		taskID,
		autopilotRunID,
		source,
		opts.Platform,
	))
}

// classifyOrigin maps the issue's origin_type / origin_id columns into the
// analytics source labels. Unknown origin_type falls back to SourceManual
// with the warning logged — analytics drift is preferable to dropping the
// event entirely.
func classifyOrigin(issue db.Issue, opts IssueCreateOpts) (source, taskID, autopilotRunID string) {
	source = analytics.SourceManual
	if !issue.OriginType.Valid {
		return source, "", ""
	}
	originID := util.UUIDToString(issue.OriginID)
	switch issue.OriginType.String {
	case "quick_create":
		return analytics.SourceManual, originID, ""
	case "autopilot":
		return analytics.SourceAutopilot, "", originID
	default:
		slog.Warn("analytics: unknown issue origin type",
			"origin_type", issue.OriginType.String,
			"issue_id", util.UUIDToString(issue.ID),
		)
		return analytics.SourceManual, "", ""
	}
}

func (s *IssueService) maybeEnqueueOnAssign(ctx context.Context, issue db.Issue, creatorType, actorID string) {
	if !issue.AssigneeType.Valid || !issue.AssigneeID.Valid {
		return
	}
	if s.shouldEnqueueAgentTask(ctx, issue) {
		if _, err := s.TaskService.EnqueueTaskForIssue(ctx, issue); err != nil {
			slog.Warn("enqueue agent task on create failed",
				"issue_id", util.UUIDToString(issue.ID),
				"error", err)
		}
	}
	if s.shouldEnqueueSquadLeaderOnAssign(ctx, issue) {
		s.enqueueSquadLeaderTask(ctx, issue, pgtype.UUID{}, creatorType, actorID)
	}
}

// shouldEnqueueAgentTask returns true when an issue create or assignment
// should trigger the assigned agent. Backlog issues are skipped — backlog
// acts as a parking lot for pre-assigning without immediate execution.
// Mirrors handler.shouldEnqueueAgentTask; kept here to make the service
// self-contained, since both code paths must move together.
func (s *IssueService) shouldEnqueueAgentTask(ctx context.Context, issue db.Issue) bool {
	if issue.Status == "backlog" {
		return false
	}
	return s.isAgentAssigneeReady(ctx, issue)
}

func (s *IssueService) isAgentAssigneeReady(ctx context.Context, issue db.Issue) bool {
	if !issue.AssigneeType.Valid || issue.AssigneeType.String != "agent" || !issue.AssigneeID.Valid {
		return false
	}
	agent, err := s.Queries.GetAgent(ctx, issue.AssigneeID)
	if err != nil || !agent.RuntimeID.Valid || agent.ArchivedAt.Valid {
		return false
	}
	return true
}

func (s *IssueService) shouldEnqueueSquadLeaderOnAssign(ctx context.Context, issue db.Issue) bool {
	if issue.Status == "backlog" {
		return false
	}
	return s.isSquadLeaderReady(ctx, issue)
}

func (s *IssueService) isSquadLeaderReady(ctx context.Context, issue db.Issue) bool {
	if !issue.AssigneeType.Valid || issue.AssigneeType.String != "squad" || !issue.AssigneeID.Valid {
		return false
	}
	squad, err := s.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
		ID:          issue.AssigneeID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return false
	}
	agent, err := s.Queries.GetAgent(ctx, squad.LeaderID)
	if err != nil {
		return false
	}
	ready, _, err := AgentReadiness(ctx, s.Queries, agent)
	if err != nil {
		return false
	}
	return ready
}

func (s *IssueService) enqueueSquadLeaderTask(ctx context.Context, issue db.Issue, triggerCommentID pgtype.UUID, authorType, authorID string) {
	squad, err := s.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
		ID:          issue.AssigneeID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return
	}
	hasPending, err := s.Queries.HasPendingTaskForIssueAndAgent(ctx, db.HasPendingTaskForIssueAndAgentParams{
		IssueID: issue.ID,
		AgentID: squad.LeaderID,
	})
	if err != nil || hasPending {
		return
	}
	if _, err := s.TaskService.EnqueueTaskForSquadLeader(ctx, issue, squad.LeaderID, triggerCommentID); err != nil {
		slog.Warn("enqueue squad leader task on create failed",
			"issue_id", util.UUIDToString(issue.ID),
			"squad_id", util.UUIDToString(squad.ID),
			"leader_id", util.UUIDToString(squad.LeaderID),
			"error", err)
	}
}
