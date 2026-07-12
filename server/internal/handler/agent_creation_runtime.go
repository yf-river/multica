package handler

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type agentCreationRuntimeReader interface {
	GetAgentRuntimeForWorkspace(context.Context, db.GetAgentRuntimeForWorkspaceParams) (db.AgentRuntime, error)
}

func loadAgentCreationRuntime(
	w http.ResponseWriter,
	r *http.Request,
	reader agentCreationRuntimeReader,
	workspaceID pgtype.UUID,
	runtimeID pgtype.UUID,
) (db.AgentRuntime, bool) {
	runtime, err := reader.GetAgentRuntimeForWorkspace(r.Context(), db.GetAgentRuntimeForWorkspaceParams{
		ID:          runtimeID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		writeValidationLookupError(w, r, err, "invalid runtime_id", "agent creation runtime", "runtime_id", uuidToString(runtimeID))
		return db.AgentRuntime{}, false
	}
	return runtime, true
}

func (h *Handler) authorizeAgentCreationRuntime(
	w http.ResponseWriter,
	r *http.Request,
	workspaceID string,
	workspaceUUID pgtype.UUID,
	ownerID string,
	runtimeID pgtype.UUID,
	scope string,
) (db.AgentRuntime, bool) {
	runtime, ok := loadAgentCreationRuntime(w, r, h.Queries, workspaceUUID, runtimeID)
	if !ok {
		return db.AgentRuntime{}, false
	}
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return db.AgentRuntime{}, false
	}
	if !canAccessRuntime(member, runtime) {
		writeError(w, http.StatusForbidden, "this runtime is personal; only its owner or a workspace admin can create agents on it")
		return db.AgentRuntime{}, false
	}
	if err := validateAgentRuntimeScope(scope, parseUUID(ownerID), runtime); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return db.AgentRuntime{}, false
	}
	return runtime, true
}
