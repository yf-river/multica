package handler

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// notifyParentOfChildDone records a system comment after the last open child
// reaches done, then explicitly wakes an Agent or Squad assignee. System
// comments bypass generic mention listeners, so this function owns both the
// safe mention text and the single permitted trigger. Notification failure is
// best-effort and must not roll back the completed Issue update.
func (h *Handler) notifyParentOfChildDone(ctx context.Context, prev, issue db.Issue, actorType, actorID string) {
	if !issue.ParentIssueID.Valid {
		return
	}
	if prev.Status == "done" || issue.Status != "done" {
		return
	}
	parent, err := h.Queries.GetIssue(ctx, issue.ParentIssueID)
	if err != nil {
		slog.Warn("child done: failed to load parent",
			"error", err,
			"child_id", uuidToString(issue.ID),
			"parent_id", uuidToString(issue.ParentIssueID))
		return
	}
	if parent.Status == "done" || parent.Status == "cancelled" {
		return
	}
	if !h.allSiblingChildrenResolved(ctx, parent.ID, issue.ID) {
		return
	}
	// Human assignees have no automated task to wake.
	if parent.AssigneeType.Valid && parent.AssigneeType.String == "member" {
		return
	}

	prefix := h.getIssuePrefix(ctx, issue.WorkspaceID)
	identifier := prefix + "-" + strconv.Itoa(int(issue.Number))
	childID := uuidToString(issue.ID)
	title := sanitizeChildTitleForSystemComment(issue.Title)

	mentionPrefix := h.buildParentAssigneeMention(ctx, parent)

	content := fmt.Sprintf(
		"%s子任务 [%s](mention://issue/%s)「%s」已完成，且同一父任务下所有子任务均已结束。请汇总子任务结果后再推进父任务验收；如果子任务结论之间存在冲突，先评论确认，不要直接收口。",
		mentionPrefix, identifier, childID, title,
	)

	// author_id is required even though author_type identifies this as system.
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		slog.Warn("child done: begin system comment transaction failed", "error", err, "child_id", childID, "parent_id", uuidToString(parent.ID))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := h.Queries.WithTx(tx)
	comment, err := queries.CreateComment(ctx, db.CreateCommentParams{
		IssueID:     parent.ID,
		WorkspaceID: parent.WorkspaceID,
		AuthorType:  "system",
		AuthorID:    pgtype.UUID{Valid: true},
		Content:     content,
		Type:        "system",
		ParentID:    pgtype.UUID{Valid: false},
	})
	if err != nil {
		slog.Warn("child done: create system comment failed",
			"error", err,
			"child_id", childID,
			"parent_id", uuidToString(parent.ID))
		return
	}
	createdEvent := buildCommentCreatedEvent(parent, commentToResponse(comment, nil, nil), "system", "")
	createdEvent, err = eventoutbox.Enqueue(ctx, queries, createdEvent)
	if err != nil {
		slog.Warn("child done: enqueue system comment event failed", "error", err, "child_id", childID, "parent_id", uuidToString(parent.ID))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		slog.Warn("child done: commit system comment failed", "error", err, "child_id", childID, "parent_id", uuidToString(parent.ID))
		return
	}
	h.publishEvent(createdEvent)

	h.dispatchParentAssigneeTrigger(ctx, parent, issue, comment, actorType, actorID)
}

func (h *Handler) allSiblingChildrenResolved(ctx context.Context, parentID, completedChildID pgtype.UUID) bool {
	var openSiblings int
	if err := h.DB.QueryRow(ctx, `
		SELECT count(*)::int
		FROM issue
		WHERE parent_issue_id = $1
		  AND id <> $2
		  AND status NOT IN ('done', 'cancelled')
	`, parentID, completedChildID).Scan(&openSiblings); err != nil {
		slog.Warn("child done: failed to count sibling children",
			"error", err,
			"parent_id", uuidToString(parentID),
			"child_id", uuidToString(completedChildID))
		return false
	}
	return openSiblings == 0
}

// sanitizeChildTitleForSystemComment prevents untrusted titles from rendering
// an active mention inside the parent system comment.
func sanitizeChildTitleForSystemComment(title string) string {
	return strings.ReplaceAll(title, "](mention://", "] (mention-stripped://")
}

// buildParentAssigneeMention returns an empty prefix when the assignee is
// absent or no longer belongs to the workspace.
func (h *Handler) buildParentAssigneeMention(ctx context.Context, parent db.Issue) string {
	if !parent.AssigneeType.Valid || !parent.AssigneeID.Valid {
		return ""
	}
	label, ok := h.resolveAssigneeMentionLabel(ctx, parent.WorkspaceID, parent.AssigneeType.String, parent.AssigneeID)
	if !ok {
		return ""
	}
	return fmt.Sprintf("[@%s](mention://%s/%s) ", label, parent.AssigneeType.String, uuidToString(parent.AssigneeID))
}

// resolveAssigneeMentionLabel loads display text without weakening the
// workspace boundary used by the mention target.
func (h *Handler) resolveAssigneeMentionLabel(ctx context.Context, workspaceID pgtype.UUID, assigneeType string, assigneeID pgtype.UUID) (string, bool) {
	switch assigneeType {
	case "agent":
		agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
			ID:          assigneeID,
			WorkspaceID: workspaceID,
		})
		if err != nil {
			return "", false
		}
		return sanitizeMentionLabel(agent.Name), true
	case "squad":
		squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
			ID:          assigneeID,
			WorkspaceID: workspaceID,
		})
		if err != nil {
			return "", false
		}
		return sanitizeMentionLabel(squad.Name), true
	}
	return "", false
}

// sanitizeMentionLabel keeps user-controlled names inside the Markdown label.
func sanitizeMentionLabel(name string) string {
	cleaned := strings.ReplaceAll(name, "]", "")
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		return "assignee"
	}
	return cleaned
}

// dispatchParentAssigneeTrigger wakes an Agent directly or a Squad leader.
// Both paths reject archived targets and deduplicate pending parent work; the
// Squad path additionally prevents distinct child owners with the same leader
// from forming a trigger loop.
func (h *Handler) dispatchParentAssigneeTrigger(ctx context.Context, parent, child db.Issue, systemComment db.Comment, actorType, actorID string) {
	if !parent.AssigneeType.Valid || !parent.AssigneeID.Valid {
		return
	}

	switch parent.AssigneeType.String {
	case "agent":
		h.triggerChildDoneAgent(ctx, parent, systemComment.ID)
	case "squad":
		h.triggerChildDoneSquad(ctx, parent, child, systemComment.ID, actorType, actorID)
	}
}

// triggerChildDoneAgent permits the same Agent to own parent and child: this
// is a handoff between Issues, not re-entry into one Issue.
func (h *Handler) triggerChildDoneAgent(ctx context.Context, parent db.Issue, triggerCommentID pgtype.UUID) {
	agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID:          parent.AssigneeID,
		WorkspaceID: parent.WorkspaceID,
	})
	if err != nil || agent.ArchivedAt.Valid {
		return
	}

	hasPending, err := h.Queries.HasPendingTaskForIssueAndAgent(ctx, db.HasPendingTaskForIssueAndAgentParams{
		IssueID: parent.ID,
		AgentID: parent.AssigneeID,
	})
	if err != nil || hasPending {
		return
	}

	if _, err := h.TaskService.EnqueueTaskForMention(ctx, parent, parent.AssigneeID, triggerCommentID); err != nil {
		slog.Warn("child done: enqueue parent agent task failed",
			"error", err,
			"parent_id", uuidToString(parent.ID),
			"agent_id", uuidToString(parent.AssigneeID))
	}
}

// triggerChildDoneSquad wakes the leader for same-Squad children but rejects a
// distinct child Agent or Squad that resolves to that same leader.
func (h *Handler) triggerChildDoneSquad(ctx context.Context, parent, child db.Issue, triggerCommentID pgtype.UUID, actorType, actorID string) {
	squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
		ID:          parent.AssigneeID,
		WorkspaceID: parent.WorkspaceID,
	})
	if err != nil {
		return
	}

	// Private-leader gate: deny if the actor cannot access the leader.
	allowed, err := h.canEnqueueSquadLeader(ctx, squad.LeaderID, actorType, actorID, uuidToString(parent.WorkspaceID))
	if err != nil {
		slog.Warn("child done: verify parent squad leader access failed", "error", err, "parent_id", uuidToString(parent.ID), "leader_id", uuidToString(squad.LeaderID))
		return
	}
	if !allowed {
		return
	}

	sameSquadChild := childAssigneeIsSquad(child, parent.AssigneeID)
	// Shared-leader loop: child driven directly by the parent squad's leader,
	// or by another squad whose leader is the same agent. Same-squad children
	// are allowed because the signal is a cross-issue handoff back to the
	// parent, mirroring the same-agent parent path above.
	if !sameSquadChild {
		if owner := h.effectiveChildAgentOwner(ctx, child); owner.Valid &&
			uuidToString(owner) == uuidToString(squad.LeaderID) {
			return
		}
	}

	agent, err := h.Queries.GetAgent(ctx, squad.LeaderID)
	if err != nil || agent.ArchivedAt.Valid {
		return
	}

	hasPending, err := h.Queries.HasPendingTaskForIssueAndAgent(ctx, db.HasPendingTaskForIssueAndAgentParams{
		IssueID: parent.ID,
		AgentID: squad.LeaderID,
	})
	if err != nil || hasPending {
		return
	}

	if _, err := h.TaskService.EnqueueTaskForSquadLeader(ctx, parent, squad.LeaderID, triggerCommentID); err != nil {
		slog.Warn("child done: enqueue parent squad leader task failed",
			"error", err,
			"parent_id", uuidToString(parent.ID),
			"squad_id", uuidToString(squad.ID),
			"leader_id", uuidToString(squad.LeaderID))
	}
}

// effectiveChildAgentOwner resolves an Agent assignee directly and a Squad
// assignee to its leader. Other or missing assignees return an invalid UUID.
func (h *Handler) effectiveChildAgentOwner(ctx context.Context, child db.Issue) pgtype.UUID {
	if !child.AssigneeType.Valid || !child.AssigneeID.Valid {
		return pgtype.UUID{}
	}
	switch child.AssigneeType.String {
	case "agent":
		return child.AssigneeID
	case "squad":
		squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
			ID:          child.AssigneeID,
			WorkspaceID: child.WorkspaceID,
		})
		if err != nil {
			return pgtype.UUID{}
		}
		return squad.LeaderID
	}
	return pgtype.UUID{}
}

func childAssigneeIsSquad(child db.Issue, squadID pgtype.UUID) bool {
	if !child.AssigneeType.Valid || child.AssigneeType.String != "squad" || !child.AssigneeID.Valid {
		return false
	}
	return uuidToString(child.AssigneeID) == uuidToString(squadID)
}
