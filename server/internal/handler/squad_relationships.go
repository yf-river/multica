package handler

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type squadAgentReader interface {
	GetAgentInWorkspace(context.Context, db.GetAgentInWorkspaceParams) (db.Agent, error)
}

func loadSquadAgent(
	w http.ResponseWriter,
	r *http.Request,
	reader squadAgentReader,
	workspaceID pgtype.UUID,
	agentID pgtype.UUID,
	invalidMessage string,
) (db.Agent, bool) {
	agent, err := reader.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          agentID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		writeValidationLookupError(w, r, err, invalidMessage, "squad agent", "agent_id", uuidToString(agentID))
		return db.Agent{}, false
	}
	return agent, true
}

type squadMemberReader interface {
	GetMemberByUserAndWorkspace(context.Context, db.GetMemberByUserAndWorkspaceParams) (db.Member, error)
}

func loadSquadMember(
	w http.ResponseWriter,
	r *http.Request,
	reader squadMemberReader,
	workspaceID pgtype.UUID,
	userID pgtype.UUID,
	invalidMessage string,
) (db.Member, bool) {
	member, err := reader.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{
		UserID:      userID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		writeValidationLookupError(w, r, err, invalidMessage, "squad member", "user_id", uuidToString(userID))
		return db.Member{}, false
	}
	return member, true
}
