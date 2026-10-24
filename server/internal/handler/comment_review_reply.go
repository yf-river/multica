package handler

// 1. Reply routing for the managed reviewer (GONG-63). The reviewer only accepts
// server-dispatched review rounds, so a reply addressed to it must not try to wake
// it; the issue's current owner takes over instead. The resolver below is the one
// place that decides who receives such a reply, and both the trigger preview and
// the real dispatch read it, so the composer never promises a recipient the save
// path disagrees with.

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/review"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 2. Wire statuses that are not dispatch statuses: the reply is saved but no run
// starts (the owner is a human, the issue has no owner, the user skipped the
// target), or the preview describes what the save path would do.
const (
	// ReviewReplyStatusWillNotify is preview-only: saving this draft would hand
	// the reply to the current owner.
	ReviewReplyStatusWillNotify = "will_notify"
	// ReviewReplyStatusHumanAssignee: the owner is a workspace member, so the
	// reply is saved for that person and no agent runs.
	ReviewReplyStatusHumanAssignee = "human_assignee"
	// ReviewReplyStatusNoAssignee: the issue has no owner to hand the reply to.
	ReviewReplyStatusNoAssignee = "no_assignee"
	// ReviewReplyStatusSkipped: the owner was resolved but the user unchecked it
	// in the composer, so nothing was dispatched.
	ReviewReplyStatusSkipped = "skipped"
)

// 3. commentReviewReplyRoute is the resolution of "who takes over a reply that was
// addressed to the managed reviewer". A zero value means the issue has no owner.
type commentReviewReplyRoute struct {
	// Trigger is the run to enqueue; HasTrigger is false when nobody can run.
	Trigger    commentAgentTrigger
	HasTrigger bool
	// RecipientType is "agent", "squad", "member", or "" when no owner is set.
	// The name is the owner as shown to the user, so the receipt can say who
	// actually received the reply instead of implying the reviewer did.
	RecipientType string
	RecipientID   string
	RecipientName string
	// ReasonCode is set when the owner exists but cannot run (permission,
	// archived target, unusable runtime). It is reported, never swallowed.
	ReasonCode DispatchReasonCode
}

// CommentReviewReplyResponse is the wire form of a reviewer-reply route. It is an
// additive field on the trigger preview and on the create/edit comment responses:
// old clients ignore it, new ones use it to say where the reply went.
type CommentReviewReplyResponse struct {
	RecipientType string             `json:"recipient_type,omitempty"` // agent | squad | member
	RecipientID   string             `json:"recipient_id,omitempty"`
	RecipientName string             `json:"recipient_name,omitempty"`
	Status        string             `json:"status"` // will_notify | queued | coalesced | deferred | blocked | human_assignee | no_assignee | skipped
	ReasonCode    DispatchReasonCode `json:"reason_code,omitempty"`
}

// 4. isManagedReviewerAgent reports whether the agent row is the workspace-managed
// reviewer. It keys on system_key, never on a name, avatar or fixed agent id.
func isManagedReviewerAgent(agent db.Agent) bool {
	return agent.SystemKey.Valid && agent.SystemKey.String == review.SystemKey
}

// 5. isReviewerComment reports whether a comment was authored by the managed
// reviewer of this workspace, which is what makes a reply "addressed to the
// reviewer" even when the thread's newest message came from someone else.
func (h *Handler) isReviewerComment(ctx context.Context, issue db.Issue, comment *db.Comment) bool {
	if comment == nil || comment.AuthorType != "agent" || !comment.AuthorID.Valid {
		return false
	}
	agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID:          comment.AuthorID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return false
	}
	return isManagedReviewerAgent(agent)
}

// 6. resolveReviewReplyRoute hands a reviewer-addressed reply to the issue's
// current owner: an agent runs, a squad's leader runs, a human owner just gets the
// reply in the timeline, and a missing or unusable owner is reported with its real
// reason. It reuses the same permission gate (canInvokeAgent), readiness verdict
// and pending-task probe as the other comment routes, so a handover never widens
// who may run an agent and never opens the reviewer to ordinary comment calls.
func (h *Handler) resolveReviewReplyRoute(ctx context.Context, issue db.Issue, authorType, authorID string, opts commentTriggerComputeOptions) commentReviewReplyRoute {
	if !issue.AssigneeType.Valid || !issue.AssigneeID.Valid {
		return commentReviewReplyRoute{}
	}
	switch issue.AssigneeType.String {
	case "agent":
		return h.resolveReviewReplyAgentRoute(ctx, issue, issue.AssigneeID, nil, authorType, authorID, opts)
	case "squad":
		squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
			ID:          issue.AssigneeID,
			WorkspaceID: issue.WorkspaceID,
		})
		if err != nil {
			return commentReviewReplyRoute{
				RecipientType: "squad",
				RecipientID:   uuidToString(issue.AssigneeID),
				ReasonCode:    ReasonTargetUnavailable,
			}
		}
		return h.resolveReviewReplyAgentRoute(ctx, issue, squad.LeaderID, &squad, authorType, authorID, opts)
	case "member":
		return commentReviewReplyRoute{
			RecipientType: "member",
			RecipientID:   uuidToString(issue.AssigneeID),
			RecipientName: h.memberDisplayName(ctx, issue.WorkspaceID, uuidToString(issue.AssigneeID)),
		}
	default:
		return commentReviewReplyRoute{}
	}
}

// 7. resolveReviewReplyAgentRoute is the shared agent/squad-owner half: load the
// target, run the invocation gate before reading any archived/runtime state (so a
// caller who may not invoke it cannot enumerate it), then decide readiness and
// whether an existing run can absorb the reply.
func (h *Handler) resolveReviewReplyAgentRoute(ctx context.Context, issue db.Issue, agentID pgtype.UUID, squad *db.Squad, authorType, authorID string, opts commentTriggerComputeOptions) commentReviewReplyRoute {
	recipient := commentReviewReplyRoute{RecipientType: "agent", RecipientID: uuidToString(agentID)}
	if squad != nil {
		recipient.RecipientType = "squad"
		recipient.RecipientID = uuidToString(squad.ID)
	}
	agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID:          agentID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		recipient.ReasonCode = ReasonTargetUnavailable
		return recipient
	}
	recipient.RecipientName = agent.Name
	if !h.canInvokeAgent(ctx, agent, authorType, authorID, opts.effectiveInvokerFor(agent), uuidToString(issue.WorkspaceID)) {
		recipient.ReasonCode = ReasonInvocationNotAllowed
		return recipient
	}
	if agent.ArchivedAt.Valid {
		recipient.ReasonCode = ReasonTargetUnavailable
		return recipient
	}
	if verdict, err := service.AgentReadiness(ctx, h.runtimeLookup(obsmetrics.RuntimeLookupSourceComment), agent); err == nil && verdict.Blocked() {
		recipient.ReasonCode = verdict.Reason
		return recipient
	}
	hasPending, err := h.hasPendingTaskForIssueAndAgent(ctx, issue.ID, agentID, opts)
	if err != nil {
		recipient.ReasonCode = ReasonInternalError
		return recipient
	}
	recipient.Trigger = commentAgentTrigger{
		Agent:          agent,
		Source:         commentTriggerSourceReviewAssignee,
		Squad:          squad,
		AlreadyPending: hasPending,
	}
	recipient.HasTrigger = true
	return recipient
}

// 8. memberDisplayName resolves a human owner's display name for the receipt. It
// falls back to an empty string when the member row or name is missing, and the
// client then renders its own neutral label.
func (h *Handler) memberDisplayName(ctx context.Context, ws pgtype.UUID, userID string) string {
	userUUID, err := util.ParseUUID(userID)
	if err != nil {
		return ""
	}
	var name string
	if err := h.DB.QueryRow(ctx, `SELECT COALESCE(u.name,'') FROM member m JOIN "user" u ON u.id=m.user_id
		WHERE m.workspace_id=$1 AND m.user_id=$2`, ws, userUUID).Scan(&name); err != nil {
		slog.Debug("review reply route: member name lookup failed", "workspace_id", uuidToString(ws), "user_id", userID, "error", err)
		return ""
	}
	return name
}

// 9. reviewReplyToResponse projects a resolved route onto the wire for the trigger
// preview, where a routable owner always reads as "will_notify" — whether the run
// is queued or merged is only known once the comment is saved.
func reviewReplyToResponse(route *commentReviewReplyRoute) *CommentReviewReplyResponse {
	if route == nil {
		return nil
	}
	resp := &CommentReviewReplyResponse{
		RecipientType: route.RecipientType,
		RecipientID:   route.RecipientID,
		RecipientName: route.RecipientName,
		ReasonCode:    route.ReasonCode,
	}
	switch {
	case route.ReasonCode != "":
		resp.Status = string(DispatchBlocked)
	case route.HasTrigger:
		resp.Status = ReviewReplyStatusWillNotify
	case route.RecipientType == "member":
		resp.Status = ReviewReplyStatusHumanAssignee
	default:
		resp.Status = ReviewReplyStatusNoAssignee
	}
	return resp
}

// 10. reviewReplyDispatchResponse reports what actually happened to a
// reviewer-addressed reply, using the enqueue results of the runs it resolved to:
// queued, merged into an existing run, deferred to it, or a specific reason it was
// not dispatched. A user-skipped target is reported as skipped rather than as if
// nothing had been asked for.
func reviewReplyDispatchResponse(route *commentReviewReplyRoute, enqueued map[string]commentEnqueueResult) *CommentReviewReplyResponse {
	if route == nil {
		return nil
	}
	resp := &CommentReviewReplyResponse{
		RecipientType: route.RecipientType,
		RecipientID:   route.RecipientID,
		RecipientName: route.RecipientName,
	}
	switch {
	case route.ReasonCode != "":
		resp.Status = string(DispatchBlocked)
		resp.ReasonCode = route.ReasonCode
		return resp
	case route.HasTrigger:
		result, ok := enqueued[uuidToString(route.Trigger.Agent.ID)]
		if !ok {
			// Resolved but absent from the enqueue results: the composer
			// suppressed this target on purpose.
			resp.Status = ReviewReplyStatusSkipped
			return resp
		}
		resp.Status = string(result.status)
		resp.ReasonCode = result.reason
		return resp
	case route.RecipientType == "member":
		resp.Status = ReviewReplyStatusHumanAssignee
		return resp
	default:
		resp.Status = ReviewReplyStatusNoAssignee
		return resp
	}
}
