package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func writeClientClosedIfCanceled(w http.ResponseWriter, err error) bool {
	if !errors.Is(err, context.Canceled) {
		return false
	}
	writeError(w, 499, "request cancelled")
	return true
}

func decodeRequiredJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

func writeEntityLoadError(w http.ResponseWriter, err error, entity string, attrs ...any) {
	if writeClientClosedIfCanceled(w, err) {
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, entity+" not found")
		return
	}
	slog.Error("load "+entity+" failed", append(attrs, "error", err)...)
	writeError(w, http.StatusInternalServerError, "failed to load "+entity)
}

func (h *Handler) requirePersonalAgentAccess(w http.ResponseWriter, r *http.Request, agent db.Agent, actorType, actorID, workspaceID, deniedMessage string) bool {
	if h.canAccessPrivateAgent(r.Context(), agent, actorType, actorID, workspaceID) {
		return true
	}
	writeError(w, http.StatusForbidden, deniedMessage)
	return false
}
