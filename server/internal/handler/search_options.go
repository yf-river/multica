package handler

import (
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgtype"
)

type searchQueryOptions struct {
	limit         int
	offset        int
	includeClosed bool
}

func (h *Handler) parseSearchRequest(w http.ResponseWriter, r *http.Request) (string, pgtype.UUID, searchQueryOptions, bool) {
	query := r.URL.Query().Get("q")
	if query == "" {
		writeError(w, http.StatusBadRequest, "q parameter is required")
		return "", pgtype.UUID{}, searchQueryOptions{}, false
	}
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return "", pgtype.UUID{}, searchQueryOptions{}, false
	}
	values := r.URL.Query()
	options := searchQueryOptions{
		limit:         20,
		includeClosed: values.Get("include_closed") == "true",
	}
	if value := values.Get("limit"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			options.limit = min(parsed, 50)
		}
	}
	if value := values.Get("offset"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed >= 0 {
			options.offset = parsed
		}
	}
	return query, workspaceUUID, options, true
}
