package handler

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// requireRuntimeAccess is the user-facing runtime authorization boundary.
// Cross-workspace callers receive the same not-found response as a missing
// runtime; members inside the workspace receive an explicit forbidden response
// when the runtime is personal and belongs to somebody else.
func (h *Handler) requireRuntimeAccess(w http.ResponseWriter, r *http.Request, runtimeID string) (db.AgentRuntime, db.Member, bool) {
	runtimeUUID, ok := parseUUIDOrBadRequest(w, runtimeID, "runtime_id")
	if !ok {
		return db.AgentRuntime{}, db.Member{}, false
	}

	runtime, err := h.Queries.GetAgentRuntime(r.Context(), runtimeUUID)
	if err != nil {
		if writeClientClosedIfCanceled(w, err) {
			return db.AgentRuntime{}, db.Member{}, false
		}
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "runtime not found")
		} else {
			slog.Error("load runtime failed", "runtime_id", runtimeID, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to load runtime")
		}
		return db.AgentRuntime{}, db.Member{}, false
	}

	member, ok := h.requireWorkspaceMember(w, r, uuidToString(runtime.WorkspaceID), "runtime not found")
	if !ok {
		return db.AgentRuntime{}, db.Member{}, false
	}
	if !canAccessRuntime(member, runtime) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return db.AgentRuntime{}, db.Member{}, false
	}

	return runtime, member, true
}
