package handler

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// GetTenantInitialAdminStatusResponse is the JSON response for the
// GetTenantInitialAdminStatus endpoint.
type GetTenantInitialAdminStatusResponse struct {
	Exists      bool    `json:"exists"`
	WorkspaceID string  `json:"workspaceId"`
	UserName    *string `json:"userName,omitempty"`
	NickName    *string `json:"nickName,omitempty"`
}

// GetTenantInitialAdminStatus returns whether the specified workspace has an
// initial admin (the earliest owner by created_at). When the workspace exists
// but has no owner-role members, it returns exists=false with a 200 — this is
// not a business error. When the workspace itself does not exist, it returns
// 404.
func (h *Handler) GetTenantInitialAdminStatus(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	// Verify the workspace exists first.
	_, err := h.Queries.GetWorkspace(r.Context(), wsUUID)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "workspace not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get workspace")
		return
	}

	// Query the initial owner (earliest owner-role member).
	owner, err := h.Queries.GetInitialOwnerByWorkspace(r.Context(), wsUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// No initial admin — normal case, not a business error.
			writeJSON(w, http.StatusOK, GetTenantInitialAdminStatusResponse{
				Exists:      false,
				WorkspaceID: uuidToString(wsUUID),
			})
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to query initial admin")
		return
	}

	// Initial admin exists — return full details.
	name := owner.UserAccount
	nickName := owner.UserName
	writeJSON(w, http.StatusOK, GetTenantInitialAdminStatusResponse{
		Exists:      true,
		WorkspaceID: uuidToString(owner.WorkspaceID),
		UserName:    &name,
		NickName:    &nickName,
	})
}
