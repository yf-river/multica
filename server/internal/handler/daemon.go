package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ---------------------------------------------------------------------------
// Daemon workspace ownership helpers
// ---------------------------------------------------------------------------

// requireDaemonWorkspaceAccess verifies the caller has access to the given workspace.
// For daemon tokens (mdt_), compares the token's workspace ID directly.
// For PAT/JWT fallback, verifies user membership in the workspace.
func (h *Handler) requireDaemonWorkspaceAccess(w http.ResponseWriter, r *http.Request, workspaceID string) bool {
	if workspaceID == "" {
		writeError(w, http.StatusNotFound, "not found")
		return false
	}

	// Daemon token: workspace must match.
	if daemonWsID := middleware.DaemonWorkspaceIDFromContext(r.Context()); daemonWsID != "" {
		if daemonWsID != workspaceID {
			writeError(w, http.StatusNotFound, "not found")
			return false
		}
		return true
	}

	// PAT/JWT fallback: check membership cache before hitting DB.
	userID := requestUserID(r)
	if userID != "" {
		if h.MembershipCache.Get(r.Context(), userID, workspaceID) {
			return true
		}
	}

	_, ok := h.requireWorkspaceMember(w, r, workspaceID, "not found")
	if ok && userID != "" {
		h.MembershipCache.Set(r.Context(), userID, workspaceID)
	}
	return ok
}

// requireDaemonRuntimeAccess looks up a runtime and verifies the caller owns its workspace.
//
// Only pgx.ErrNoRows is treated as a real "runtime gone" 404 — the daemon uses
// that response to drop the stale runtime from its in-memory map and re-register,
// so collapsing transient DB errors into the same 404 would force the daemon to
// self-cleanup on a hiccup. Other DB errors become 500.
func (h *Handler) requireDaemonRuntimeAccess(w http.ResponseWriter, r *http.Request, runtimeID string) (db.AgentRuntime, bool) {
	runtimeUUID, ok := parseUUIDOrBadRequest(w, runtimeID, "runtime_id")
	if !ok {
		return db.AgentRuntime{}, false
	}
	rt, err := h.Queries.GetAgentRuntime(r.Context(), runtimeUUID)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "runtime not found")
			return db.AgentRuntime{}, false
		}
		slog.Warn("get agent runtime failed", "runtime_id", runtimeID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load runtime")
		return db.AgentRuntime{}, false
	}
	if !h.requireDaemonWorkspaceAccess(w, r, uuidToString(rt.WorkspaceID)) {
		return db.AgentRuntime{}, false
	}
	return rt, true
}

// requireDaemonTaskAccess looks up a task and verifies the caller owns its workspace.
func (h *Handler) requireDaemonTaskAccess(w http.ResponseWriter, r *http.Request, taskID string) (db.AgentTaskQueue, bool) {
	task, _, ok := h.requireDaemonTaskAccessWithWorkspace(w, r, taskID)
	return task, ok
}

// requireDaemonTaskAccessWithWorkspace is the workspace-aware variant of
// requireDaemonTaskAccess. It returns the resolved workspace ID alongside
// the task row so callers that need to forward workspace_id into
// taskToResponse (powering RelativeWorkDir) don't have to repeat the
// ResolveTaskWorkspaceID lookup. The two helpers share their entire
// implementation; the simpler one is preserved for ergonomic call sites
// that genuinely don't need workspace_id.
func (h *Handler) requireDaemonTaskAccessWithWorkspace(w http.ResponseWriter, r *http.Request, taskID string) (db.AgentTaskQueue, string, bool) {
	taskUUID, ok := parseUUIDOrBadRequest(w, taskID, "task_id")
	if !ok {
		return db.AgentTaskQueue{}, "", false
	}
	task, err := h.Queries.GetAgentTask(r.Context(), taskUUID)
	if err != nil {
		// Only treat pgx.ErrNoRows as a real "task gone" signal — daemon
		// uses this 404 to interrupt the running agent, so a transient DB
		// error must not be reported as a deletion.
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "task not found")
			return db.AgentTaskQueue{}, "", false
		}
		slog.Warn("get agent task failed", "task_id", taskID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load task")
		return db.AgentTaskQueue{}, "", false
	}

	wsID := h.TaskService.ResolveTaskWorkspaceID(r.Context(), task)
	if wsID == "" {
		writeError(w, http.StatusNotFound, "task not found")
		return db.AgentTaskQueue{}, "", false
	}

	if !h.requireDaemonWorkspaceAccess(w, r, wsID) {
		return db.AgentTaskQueue{}, "", false
	}
	return task, wsID, true
}

// verifyDaemonWorkspaceAccess checks workspace access without writing an HTTP error.
// Used in loops where individual items may be skipped silently.
func (h *Handler) verifyDaemonWorkspaceAccess(r *http.Request, workspaceID string) bool {
	if workspaceID == "" {
		return false
	}
	if daemonWsID := middleware.DaemonWorkspaceIDFromContext(r.Context()); daemonWsID != "" {
		return daemonWsID == workspaceID
	}
	userID := requestUserID(r)
	if userID == "" {
		return false
	}
	if h.MembershipCache.Get(r.Context(), userID, workspaceID) {
		return true
	}
	_, err := h.getWorkspaceMember(r.Context(), userID, workspaceID)
	if err != nil {
		return false
	}
	h.MembershipCache.Set(r.Context(), userID, workspaceID)
	return true
}

// ---------------------------------------------------------------------------
// Daemon Registration & Heartbeat
// ---------------------------------------------------------------------------

type DaemonRegisterRequest struct {
	WorkspaceID string `json:"workspace_id"`
	DaemonID    string `json:"daemon_id"`
	// LegacyDaemonIDs lists prior hostname-derived daemon_ids this machine
	// may have registered under before switching to a persistent UUID. The
	// handler merges any matching runtime rows into the new row so agents
	// and tasks keep working without manual intervention.
	LegacyDaemonIDs []string `json:"legacy_daemon_ids"`
	DeviceName      string   `json:"device_name"`
	CLIVersion      string   `json:"cli_version"` // multica CLI version
	LaunchedBy      string   `json:"launched_by"` // "desktop" when spawned by the Electron app
	Runtimes        []struct {
		Name     string          `json:"name"`
		Type     string          `json:"type"`
		Version  string          `json:"version"` // agent CLI version (claude/codex)
		Status   string          `json:"status"`
		Metadata json.RawMessage `json:"metadata,omitempty"`
		// ProfileID, when non-empty, marks this as an instance of a custom
		// runtime_profile (MUL-3284). Empty = built-in runtime (legacy path).
		// Type carries the protocol family for both built-in and custom rows
		// so task routing (agent.New) is unchanged.
		ProfileID string `json:"profile_id"`
	} `json:"runtimes"`
}

type daemonWorkspaceReposResponse struct {
	WorkspaceID  string          `json:"workspace_id"`
	Repos        []RepoData      `json:"repos"`
	ReposVersion string          `json:"repos_version"`
	Settings     json.RawMessage `json:"settings,omitempty"`
}

func normalizeWorkspaceRepos(repos []RepoData) []RepoData {
	if len(repos) == 0 {
		return []RepoData{}
	}

	normalized := make([]RepoData, 0, len(repos))
	seen := make(map[string]struct{}, len(repos))
	for _, repo := range repos {
		url := strings.TrimSpace(repo.URL)
		if url == "" {
			continue
		}
		if _, exists := seen[url]; exists {
			continue
		}
		seen[url] = struct{}{}
		normalized = append(normalized, RepoData{URL: url, Description: repo.Description})
	}
	return normalized
}

func workspaceReposVersion(repos []RepoData) string {
	urls := make([]string, 0, len(repos))
	for _, repo := range repos {
		if repo.URL == "" {
			continue
		}
		urls = append(urls, repo.URL)
	}
	sort.Strings(urls)
	sum := sha256.Sum256([]byte(strings.Join(urls, "\n")))
	return hex.EncodeToString(sum[:])
}

func parseWorkspaceRepos(raw []byte) []RepoData {
	if len(raw) == 0 {
		return []RepoData{}
	}

	var repos []RepoData
	if err := json.Unmarshal(raw, &repos); err != nil {
		return []RepoData{}
	}
	return normalizeWorkspaceRepos(repos)
}

func workspaceReposResponse(workspaceID string, raw []byte, settingsRaw []byte) daemonWorkspaceReposResponse {
	repos := parseWorkspaceRepos(raw)
	resp := daemonWorkspaceReposResponse{
		WorkspaceID:  workspaceID,
		Repos:        repos,
		ReposVersion: workspaceReposVersion(repos),
	}
	if len(settingsRaw) > 0 {
		resp.Settings = json.RawMessage(settingsRaw)
	}
	return resp
}

// normalizeProvider canonicalizes a provider string for storage: trimmed and
// lowercased so client-side pricing lookups tolerate case drift. Returns "" for
// a blank input.
func normalizeProvider(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

