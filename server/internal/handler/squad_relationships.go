package handler

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func loadSquadAgent(
	w http.ResponseWriter,
	r *http.Request,
	queries *db.Queries,
	workspaceID pgtype.UUID,
	agentID pgtype.UUID,
	invalidMessage string,
) (db.Agent, bool) {
	agent, err := queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          agentID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		writeValidationLookupError(w, err, invalidMessage, "squad agent", "agent_id", uuidToString(agentID))
		return db.Agent{}, false
	}
	return agent, true
}

func loadSquadMember(
	w http.ResponseWriter,
	r *http.Request,
	queries *db.Queries,
	workspaceID pgtype.UUID,
	userID pgtype.UUID,
	invalidMessage string,
) bool {
	_, err := queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{
		UserID:      userID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		writeValidationLookupError(w, err, invalidMessage, "squad member", "user_id", uuidToString(userID))
		return false
	}
	return true
}
