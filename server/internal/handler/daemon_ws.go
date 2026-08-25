package handler

import (
	"net/http"
	"strings"

	"github.com/multica-ai/multica/server/internal/daemonws"
)

func (h *Handler) DaemonWebSocket(w http.ResponseWriter, r *http.Request) {
	if h.DaemonHub == nil {
		writeError(w, http.StatusServiceUnavailable, "daemon websocket unavailable")
		return
	}

	runtimeIDs := parseRuntimeIDs(r)
	if len(runtimeIDs) == 0 {
		writeError(w, http.StatusBadRequest, "runtime_ids required")
		return
	}

	workspaceIDs := make([]string, 0, len(runtimeIDs))
	seenWorkspaceIDs := make(map[string]struct{}, len(runtimeIDs))
	for _, runtimeID := range runtimeIDs {
		rt, ok := h.requireDaemonRuntimeAccess(w, r, runtimeID)
		if !ok {
			return
		}
		workspaceID := uuidToString(rt.WorkspaceID)
		if workspaceID != "" {
			if _, ok := seenWorkspaceIDs[workspaceID]; !ok {
				seenWorkspaceIDs[workspaceID] = struct{}{}
				workspaceIDs = append(workspaceIDs, workspaceID)
			}
		}
	}

	h.DaemonHub.HandleWebSocket(w, r, daemonws.ClientIdentity{
		UserID:        requestUserID(r),
		WorkspaceIDs:  workspaceIDs,
		RuntimeIDs:    runtimeIDs,
		ClientVersion: r.Header.Get("X-Client-Version"),
	})
}

func parseRuntimeIDs(r *http.Request) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(raw string) {
		for _, part := range strings.Split(raw, ",") {
			id := strings.TrimSpace(part)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	for _, raw := range r.URL.Query()["runtime_id"] {
		add(raw)
	}
	for _, raw := range r.URL.Query()["runtime_ids"] {
		add(raw)
	}
	return out
}
