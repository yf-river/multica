package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

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
		ancestor, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: cursor, WorkspaceID: issue.WorkspaceID})
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
	_, err := h.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: projectID, WorkspaceID: workspaceID})
	return err
}

func writeAssigneeValidationError(w http.ResponseWriter, r *http.Request, err error) {
	if writeClientClosedIfCanceled(w, err) {
		return
	}
	slog.Error("assignee validation failed", append(logger.RequestAttrs(r), "error", err)...)
	writeError(w, http.StatusInternalServerError, "failed to validate assignee")
}

// validateAssigneePair owns the current member/agent/squad assignment and
// personal-scope authorization contract shared by create, update, batch and
// quick-create paths.
func (h *Handler) validateAssigneePair(ctx context.Context, r *http.Request, workspaceID string, assigneeType pgtype.Text, assigneeID pgtype.UUID) (int, string, error) {
	if !assigneeType.Valid && !assigneeID.Valid {
		return 0, "", nil
	}
	if assigneeType.Valid != assigneeID.Valid {
		return http.StatusBadRequest, "assignee_type and assignee_id must be provided together", nil
	}
	wsUUID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return http.StatusBadRequest, "invalid workspace_id", nil
	}
	switch assigneeType.String {
	case "member":
		if _, err := h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{UserID: assigneeID, WorkspaceID: wsUUID}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return http.StatusBadRequest, "assignee_id does not refer to a member of this workspace", nil
			}
			return 0, "", fmt.Errorf("validate member assignee: %w", err)
		}
		return 0, "", nil
	case "agent":
		agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: assigneeID, WorkspaceID: wsUUID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return http.StatusBadRequest, "assignee_id does not refer to an agent of this workspace", nil
			}
			return 0, "", fmt.Errorf("validate agent assignee: %w", err)
		}
		if agent.ArchivedAt.Valid {
			return http.StatusBadRequest, "cannot assign to archived agent", nil
		}
		actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
		allowed, err := h.personalAgentAccess(ctx, agent, actorType, actorID, workspaceID)
		if err != nil {
			return 0, "", fmt.Errorf("authorize agent assignee: %w", err)
		}
		if !allowed {
			return http.StatusForbidden, "cannot assign to personal agent", nil
		}
		return 0, "", nil
	case "squad":
		squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{ID: assigneeID, WorkspaceID: wsUUID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return http.StatusBadRequest, "assignee_id does not refer to a squad in this workspace", nil
			}
			return 0, "", fmt.Errorf("validate squad assignee: %w", err)
		}
		if squad.ArchivedAt.Valid {
			return http.StatusBadRequest, "cannot assign to an archived squad", nil
		}
		actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
		allowed, err := h.squadAccess(ctx, squad, actorType, actorID, workspaceID)
		if err != nil {
			return 0, "", fmt.Errorf("authorize squad assignee: %w", err)
		}
		if !allowed {
			return http.StatusForbidden, "cannot assign to personal squad", nil
		}
		leader, err := h.Queries.GetAgent(ctx, squad.LeaderID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return http.StatusBadRequest, "squad leader is archived; cannot assign to this squad", nil
			}
			return 0, "", fmt.Errorf("validate squad leader assignee: %w", err)
		}
		if leader.ArchivedAt.Valid {
			return http.StatusBadRequest, "squad leader is archived; cannot assign to this squad", nil
		}
		allowed, err = h.personalAgentAccess(ctx, leader, actorType, actorID, workspaceID)
		if err != nil {
			return 0, "", fmt.Errorf("authorize squad leader assignee: %w", err)
		}
		if !allowed {
			return http.StatusForbidden, "cannot assign to squad with personal leader", nil
		}
		return 0, "", nil
	default:
		return http.StatusBadRequest, "assignee_type must be 'member', 'agent', or 'squad'", nil
	}
}
