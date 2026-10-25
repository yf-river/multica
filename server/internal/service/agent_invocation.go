package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/review"
	"github.com/multica-ai/multica/server/internal/upstream"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// memberMayInvokeAgent is the policy itself, with query failures kept apart
// from denials.
//
// (false, nil) is a real verdict: no user id, not the owner of a private
// agent, or a public_to agent this member is not a target of — including the
// member row simply not being there, which is what pgx.ErrNoRows means here.
//
// (false, err) is "we do not know". Collapsing the two is what turns one
// transient database error into a message the platform will never redeliver
// and a person told they lack a permission they have.
func memberMayInvokeAgent(ctx context.Context, queries *db.Queries, agent db.Agent, memberUserID pgtype.UUID, workspaceID pgtype.UUID) (bool, error) {
	// 托管审查与上游同步执行体只接受平台派发。
	if agent.SystemKey.Valid && (agent.SystemKey.String == review.SystemKey || upstream.IsExecutorSystemKey(agent.SystemKey.String)) {
		return false, nil
	}
	userID := util.UUIDToString(memberUserID)
	if userID == "" {
		return false, nil
	}
	if util.UUIDToString(agent.OwnerID) == userID {
		return true, nil
	}
	if agent.PermissionMode != "public_to" {
		return false, nil
	}
	targets, err := queries.ListAgentInvocationTargets(ctx, agent.ID)
	if err != nil {
		return false, fmt.Errorf("list agent invocation targets: %w", err)
	}
	isWorkspaceMember := false
	switch _, err := queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID:      memberUserID,
		WorkspaceID: workspaceID,
	}); {
	case err == nil:
		isWorkspaceMember = true
	case errors.Is(err, pgx.ErrNoRows):
		// Not a member. A fact, not a failure.
	default:
		return false, fmt.Errorf("load workspace member: %w", err)
	}
	for _, t := range targets {
		switch t.TargetType {
		case "workspace":
			if isWorkspaceMember {
				return true, nil
			}
		case "member":
			if util.UUIDToString(t.TargetID) == userID {
				return true, nil
			}
		}
	}
	return false, nil
}
