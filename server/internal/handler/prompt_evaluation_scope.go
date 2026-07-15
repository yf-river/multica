package handler

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
)

func (h *Handler) requirePromptEvaluationWorkspaceUser(
	w http.ResponseWriter,
	r *http.Request,
) (string, pgtype.UUID, string, bool) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return "", pgtype.UUID{}, "", false
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return "", pgtype.UUID{}, "", false
	}
	return workspaceID, workspaceUUID, userID, true
}
