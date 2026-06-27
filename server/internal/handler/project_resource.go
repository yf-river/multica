package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ProjectResourceResponse is the JSON shape returned by the project resource API.
type ProjectResourceResponse struct {
	ID           string          `json:"id"`
	ProjectID    string          `json:"project_id"`
	WorkspaceID  string          `json:"workspace_id"`
	ResourceType string          `json:"resource_type"`
	ResourceRef  json.RawMessage `json:"resource_ref"`
	Label        *string         `json:"label"`
	Position     int32           `json:"position"`
	CreatedAt    string          `json:"created_at"`
	CreatedBy    *string         `json:"created_by"`
}

func projectResourceToResponse(r db.ProjectResource) ProjectResourceResponse {
	ref := json.RawMessage(r.ResourceRef)
	if len(ref) == 0 {
		ref = json.RawMessage("{}")
	}
	return ProjectResourceResponse{
		ID:           uuidToString(r.ID),
		ProjectID:    uuidToString(r.ProjectID),
		WorkspaceID:  uuidToString(r.WorkspaceID),
		ResourceType: r.ResourceType,
		ResourceRef:  ref,
		Label:        textToPtr(r.Label),
		Position:     r.Position,
		CreatedAt:    timestampToString(r.CreatedAt),
		CreatedBy:    uuidToPtr(r.CreatedBy),
	}
}

// CreateProjectResourceRequest is the body for POST /api/projects/{id}/resources.
type CreateProjectResourceRequest struct {
	ResourceType string          `json:"resource_type"`
	ResourceRef  json.RawMessage `json:"resource_ref"`
	Label        *string         `json:"label"`
	Position     *int32          `json:"position"`
}

// UpdateProjectResourceRequest is the body for PUT /api/projects/{id}/resources/{resourceId}.
// resource_type cannot change after creation — pick a new type by deleting and
// re-adding. Every field is optional; omitted fields keep their current value.
type UpdateProjectResourceRequest struct {
	ResourceRef json.RawMessage `json:"resource_ref"`
	Label       *string         `json:"label"`
	Position    *int32          `json:"position"`
}

// validateAndNormalizeResourceRef checks the payload for a known resource_type.
// New types are added here without schema migration; unknown types are rejected
// at the API boundary so a typo can't slip through and produce a resource the
// daemon/UI doesn't understand.
func validateAndNormalizeResourceRef(resourceType string, ref json.RawMessage) (json.RawMessage, error) {
	if len(ref) == 0 {
		return nil, errors.New("resource_ref is required")
	}
	switch resourceType {
	case "github_repo":
		return validateGithubRepoRef(ref)
	case "gongfeng_repo":
		return validateGongfengRepoRef(ref)
	case "local_directory":
		return validateLocalDirectoryRef(ref)
	default:
		return nil, fmt.Errorf("unknown resource_type %q", resourceType)
	}
}

type githubRepoRef struct {
	URL               string `json:"url"`
	DefaultBranchHint string `json:"default_branch_hint,omitempty"`
}

type gongfengRepoRef struct {
	Provider                        string `json:"provider"`
	URL                             string `json:"url"`
	ProjectPath                     string `json:"project_path"`
	ResourceKind                    string `json:"resource_kind"`
	Ref                             string `json:"ref,omitempty"`
	HeadCommit                      string `json:"head_commit,omitempty"`
	Branch                          string `json:"branch,omitempty"`
	CommitSHA                       string `json:"commit_sha,omitempty"`
	ConnectionStatus                string `json:"connection_status,omitempty"`
	SyncStatus                      string `json:"sync_status,omitempty"`
	TestStatus                      string `json:"test_status,omitempty"`
	LastTestedAt                    string `json:"last_tested_at,omitempty"`
	LastSyncedAt                    string `json:"last_synced_at,omitempty"`
	CredentialStatus                string `json:"credential_status,omitempty"`
	CredentialProfileID             string `json:"credential_profile_id,omitempty"`
	CredentialProfileStatus         string `json:"credential_profile_status,omitempty"`
	CredentialSecretHint            string `json:"credential_secret_hint,omitempty"`
	CredentialLastVerifiedAt        string `json:"credential_last_verified_at,omitempty"`
	CredentialProbeStatus           string `json:"credential_probe_status,omitempty"`
	CredentialProbeHTTPStatus       string `json:"credential_probe_http_status,omitempty"`
	CredentialProbeTarget           string `json:"credential_probe_target,omitempty"`
	UnauthenticatedConnectionStatus string `json:"unauthenticated_connection_status,omitempty"`
	Disabled                        bool   `json:"disabled,omitempty"`
	DisabledAt                      string `json:"disabled_at,omitempty"`
	Title                           string `json:"title,omitempty"`
}

func validateGithubRepoRef(ref json.RawMessage) (json.RawMessage, error) {
	var payload githubRepoRef
	if err := json.Unmarshal(ref, &payload); err != nil {
		return nil, fmt.Errorf("invalid github_repo payload: %w", err)
	}
	payload.URL = strings.TrimSpace(payload.URL)
	if payload.URL == "" {
		return nil, errors.New("github_repo: url is required")
	}
	if !isValidGitRepoURL(payload.URL) {
		return nil, errors.New("github_repo: url must be a valid http(s) or ssh git URL")
	}
	payload.DefaultBranchHint = strings.TrimSpace(payload.DefaultBranchHint)
	out, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func validateGongfengRepoRef(ref json.RawMessage) (json.RawMessage, error) {
	var payload gongfengRepoRef
	if err := json.Unmarshal(ref, &payload); err != nil {
		return nil, fmt.Errorf("invalid gongfeng_repo payload: %w", err)
	}
	payload.URL = strings.TrimSpace(payload.URL)
	if payload.URL == "" {
		return nil, errors.New("gongfeng_repo: url is required")
	}
	parsed, err := parseGongfengURL(payload.URL)
	if err != nil {
		return nil, err
	}
	payload.Provider = "gongfeng"
	payload.ProjectPath = strings.Trim(strings.TrimSpace(payload.ProjectPath), "/")
	if payload.ProjectPath == "" {
		payload.ProjectPath = parsed.ProjectPath
	}
	if payload.ProjectPath == "" {
		return nil, errors.New("gongfeng_repo: project_path is required")
	}
	payload.ResourceKind = strings.TrimSpace(payload.ResourceKind)
	if payload.ResourceKind == "" {
		payload.ResourceKind = parsed.ResourceKind
	}
	if !isValidGongfengResourceKind(payload.ResourceKind) {
		return nil, errors.New("gongfeng_repo: resource_kind must be project, branch, commits, commit, tag, file, or merge_request")
	}
	payload.Ref = strings.TrimSpace(payload.Ref)
	if payload.Ref == "" {
		payload.Ref = parsed.Ref
	}
	payload.HeadCommit = strings.TrimSpace(payload.HeadCommit)
	payload.Branch = strings.TrimSpace(payload.Branch)
	payload.CommitSHA = strings.TrimSpace(payload.CommitSHA)
	payload.ConnectionStatus = strings.TrimSpace(payload.ConnectionStatus)
	payload.SyncStatus = strings.TrimSpace(payload.SyncStatus)
	payload.TestStatus = strings.TrimSpace(payload.TestStatus)
	payload.LastTestedAt = strings.TrimSpace(payload.LastTestedAt)
	payload.LastSyncedAt = strings.TrimSpace(payload.LastSyncedAt)
	payload.CredentialStatus = strings.TrimSpace(payload.CredentialStatus)
	payload.CredentialProfileID = strings.TrimSpace(payload.CredentialProfileID)
	payload.CredentialProfileStatus = strings.TrimSpace(payload.CredentialProfileStatus)
	payload.CredentialSecretHint = strings.TrimSpace(payload.CredentialSecretHint)
	payload.CredentialLastVerifiedAt = strings.TrimSpace(payload.CredentialLastVerifiedAt)
	payload.CredentialProbeStatus = strings.TrimSpace(payload.CredentialProbeStatus)
	payload.CredentialProbeHTTPStatus = strings.TrimSpace(payload.CredentialProbeHTTPStatus)
	payload.CredentialProbeTarget = strings.TrimSpace(payload.CredentialProbeTarget)
	payload.UnauthenticatedConnectionStatus = strings.TrimSpace(payload.UnauthenticatedConnectionStatus)
	payload.DisabledAt = strings.TrimSpace(payload.DisabledAt)
	payload.Title = strings.TrimSpace(payload.Title)
	out, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return out, nil
}

type parsedGongfengURL struct {
	ProjectPath  string
	ResourceKind string
	Ref          string
}

func parseGongfengURL(raw string) (parsedGongfengURL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return parsedGongfengURL{}, errors.New("gongfeng_repo: url must be a valid https URL")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return parsedGongfengURL{}, errors.New("gongfeng_repo: url must be a valid http(s) URL")
	}
	if !strings.EqualFold(u.Host, "git.code.tencent.com") {
		return parsedGongfengURL{}, errors.New("gongfeng_repo: host must be git.code.tencent.com")
	}
	segments := splitCleanPath(u.Path)
	if len(segments) == 0 {
		return parsedGongfengURL{}, errors.New("gongfeng_repo: project_path is required")
	}
	if i := indexSegment(segments, "-"); i >= 0 {
		segments = append(segments[:i], segments[i+1:]...)
	}
	marker := len(segments)
	kind := "project"
	ref := ""
	for i, segment := range segments {
		switch segment {
		case "commits":
			marker, kind = i, "commits"
		case "commit":
			marker, kind = i, "commit"
		case "tree", "branches", "branch":
			marker, kind = i, "branch"
		case "tags", "tag":
			marker, kind = i, "tag"
		case "blob", "file", "files":
			marker, kind = i, "file"
		case "merge_requests", "merge_request":
			marker, kind = i, "merge_request"
		default:
			continue
		}
		if marker+1 < len(segments) {
			ref = strings.Join(segments[marker+1:], "/")
		}
		break
	}
	projectSegments := segments[:marker]
	if len(projectSegments) == 0 {
		return parsedGongfengURL{}, errors.New("gongfeng_repo: project_path is required")
	}
	projectPath := strings.TrimSuffix(strings.Join(projectSegments, "/"), ".git")
	if projectPath == "" {
		return parsedGongfengURL{}, errors.New("gongfeng_repo: project_path is required")
	}
	return parsedGongfengURL{ProjectPath: projectPath, ResourceKind: kind, Ref: ref}, nil
}

func splitCleanPath(path string) []string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func indexSegment(segments []string, needle string) int {
	for i, segment := range segments {
		if segment == needle {
			return i
		}
	}
	return -1
}

func isValidGongfengResourceKind(kind string) bool {
	switch kind {
	case "project", "branch", "commits", "commit", "tag", "file", "merge_request":
		return true
	default:
		return false
	}
}

// localDirectoryRef is the JSONB shape stored for resource_type=local_directory.
// It pins a project to an existing directory on a specific user machine, so
// agent tasks run in-place rather than in an isolated git worktree. The
// daemon_id scopes the path to one daemon registration — the same string path
// on a different machine is a different resource. The optional label is a
// human-readable hint used by the UI; the row-level project_resource.label
// column remains the generic column for any resource type.
type localDirectoryRef struct {
	LocalPath string `json:"local_path"`
	DaemonID  string `json:"daemon_id"`
	Label     string `json:"label,omitempty"`
}

func validateLocalDirectoryRef(ref json.RawMessage) (json.RawMessage, error) {
	var payload localDirectoryRef
	if err := json.Unmarshal(ref, &payload); err != nil {
		return nil, fmt.Errorf("invalid local_directory payload: %w", err)
	}
	payload.LocalPath = strings.TrimSpace(payload.LocalPath)
	if payload.LocalPath == "" {
		return nil, errors.New("local_directory: local_path is required")
	}
	if !isAbsoluteLocalPath(payload.LocalPath) {
		return nil, errors.New("local_directory: local_path must be an absolute path")
	}
	payload.DaemonID = strings.TrimSpace(payload.DaemonID)
	if payload.DaemonID == "" {
		return nil, errors.New("local_directory: daemon_id is required")
	}
	payload.Label = strings.TrimSpace(payload.Label)
	out, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// isAbsoluteLocalPath checks the path looks absolute on either POSIX or
// Windows daemons. The server can't know which OS the daemon runs on, so we
// accept the union: a leading "/" (POSIX), a UNC prefix "\\", or a drive
// letter like "C:\" or "C:/". The daemon still verifies existence at run
// time — this is a typo guard, not a filesystem check.
func isAbsoluteLocalPath(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '/' {
		return true
	}
	if strings.HasPrefix(s, `\\`) {
		return true
	}
	if len(s) >= 3 && isDriveLetter(s[0]) && s[1] == ':' && (s[2] == '\\' || s[2] == '/') {
		return true
	}
	return false
}

func isDriveLetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// isValidGitRepoURL accepts the three forms a user can paste from GitHub's
// "Code" menu: https://, ssh:// (with explicit scheme), and the scp-like
// shorthand `git@host:owner/repo.git`. The check is intentionally lax — we are
// guarding against pasted garbage like "not-a-url", not enforcing a strict
// grammar — because the actual fetch happens client-side via `git clone` and
// the user gets a clearer error from git than from us.
func isValidGitRepoURL(s string) bool {
	if u, err := url.Parse(s); err == nil && u.Host != "" {
		switch u.Scheme {
		case "http", "https", "ssh", "git":
			return true
		}
	}
	// scp-like ssh shorthand: [user@]host:path with a non-empty host and path,
	// and no spaces. Reject anything that looks like a URL with a scheme
	// (those should go through url.Parse above).
	if strings.Contains(s, " ") || strings.Contains(s, "://") {
		return false
	}
	colon := strings.Index(s, ":")
	if colon <= 0 || colon == len(s)-1 {
		return false
	}
	// In scp-like ssh shorthand `[user@]host:path`, `@` is only meaningful
	// as a user separator before the first ':'. If '@' appears at or after
	// the colon it is not the user separator — reject as malformed rather
	// than guess (and avoid a slice-bounds panic from blindly slicing).
	at := strings.Index(s, "@")
	if at >= colon {
		return false
	}
	hostStart := 0
	if at >= 0 {
		hostStart = at + 1
	}
	host := s[hostStart:colon]
	path := s[colon+1:]
	if host == "" || path == "" {
		return false
	}
	return true
}

// loadProjectForResource resolves the project, enforces workspace ownership,
// and returns its DB row. Used by all project_resource handlers.
func (h *Handler) loadProjectForResource(w http.ResponseWriter, r *http.Request, projectIDParam string) (db.Project, bool) {
	projectUUID, ok := parseUUIDOrBadRequest(w, projectIDParam, "project id")
	if !ok {
		return db.Project{}, false
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace id")
	if !ok {
		return db.Project{}, false
	}
	project, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{
		ID: projectUUID, WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return db.Project{}, false
	}
	return project, true
}

// ListProjectResources returns the resources attached to a project.
func (h *Handler) ListProjectResources(w http.ResponseWriter, r *http.Request) {
	project, ok := h.loadProjectForResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	resources, err := h.Queries.ListProjectResources(r.Context(), project.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list project resources")
		return
	}
	resp := make([]ProjectResourceResponse, len(resources))
	for i, res := range resources {
		resp[i] = projectResourceToResponse(res)
	}
	writeJSON(w, http.StatusOK, map[string]any{"resources": resp, "total": len(resp)})
}

// CreateProjectResource attaches a new resource to a project.
func (h *Handler) CreateProjectResource(w http.ResponseWriter, r *http.Request) {
	project, ok := h.loadProjectForResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req CreateProjectResourceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.ResourceType = strings.TrimSpace(req.ResourceType)
	if req.ResourceType == "" {
		writeError(w, http.StatusBadRequest, "resource_type is required")
		return
	}
	normalizedRef, err := validateAndNormalizeResourceRef(req.ResourceType, req.ResourceRef)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if conflict, err := h.findLocalDirectoryConflict(r.Context(), project.ID, req.ResourceType, normalizedRef, pgtype.UUID{}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check existing resources")
		return
	} else if conflict {
		writeError(w, http.StatusConflict, "this daemon already has a local_directory attached to the project; remove it before adding another")
		return
	}

	var label pgtype.Text
	if req.Label != nil && strings.TrimSpace(*req.Label) != "" {
		label = pgtype.Text{String: strings.TrimSpace(*req.Label), Valid: true}
	}
	var position int32
	if req.Position != nil {
		position = *req.Position
	} else {
		// Append after existing resources.
		count, _ := h.Queries.CountProjectResources(r.Context(), project.ID)
		position = int32(count)
	}

	creator, _ := h.parseUserUUIDOrZero(userID)
	resource, err := h.Queries.CreateProjectResource(r.Context(), db.CreateProjectResourceParams{
		ProjectID:    project.ID,
		WorkspaceID:  project.WorkspaceID,
		ResourceType: req.ResourceType,
		ResourceRef:  normalizedRef,
		Label:        label,
		Position:     position,
		CreatedBy:    creator,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "this resource is already attached to the project")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create project resource")
		return
	}

	resp := projectResourceToResponse(resource)
	h.publish(
		protocol.EventProjectResourceCreated,
		uuidToString(project.WorkspaceID),
		"member",
		userID,
		map[string]any{"resource": resp, "project_id": uuidToString(project.ID)},
	)
	writeJSON(w, http.StatusCreated, resp)
}

// UpdateProjectResource edits an existing resource's ref/label/position.
// resource_type is immutable — re-pointing a resource at a different type is
// almost always a different conceptual entity, so the caller should delete and
// re-add instead. Omitted fields keep their current value, including the
// `label` JSON null vs. missing distinction (missing = keep, explicit "" =
// clear).
func (h *Handler) UpdateProjectResource(w http.ResponseWriter, r *http.Request) {
	project, ok := h.loadProjectForResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	resourceUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "resourceId"), "resource id")
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	existing, err := h.Queries.GetProjectResourceInWorkspace(r.Context(), db.GetProjectResourceInWorkspaceParams{
		ID: resourceUUID, WorkspaceID: project.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "project resource not found")
		return
	}
	if uuidToString(existing.ProjectID) != uuidToString(project.ID) {
		writeError(w, http.StatusNotFound, "project resource not found")
		return
	}

	// Decode into a raw map first so we can tell "field omitted" from
	// "field present with zero value" — the label clear case in particular
	// relies on this distinction.
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	nextRef := json.RawMessage(existing.ResourceRef)
	if rawRef, ok := raw["resource_ref"]; ok {
		normalized, err := validateAndNormalizeResourceRef(existing.ResourceType, rawRef)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		nextRef = normalized
	}

	if conflict, err := h.findLocalDirectoryConflict(r.Context(), project.ID, existing.ResourceType, nextRef, existing.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check existing resources")
		return
	} else if conflict {
		writeError(w, http.StatusConflict, "another local_directory on this daemon is already attached to the project")
		return
	}

	nextLabel := existing.Label
	if rawLabel, ok := raw["label"]; ok {
		var labelStr *string
		if err := json.Unmarshal(rawLabel, &labelStr); err != nil {
			writeError(w, http.StatusBadRequest, "label must be a string or null")
			return
		}
		if labelStr == nil || strings.TrimSpace(*labelStr) == "" {
			nextLabel = pgtype.Text{}
		} else {
			nextLabel = pgtype.Text{String: strings.TrimSpace(*labelStr), Valid: true}
		}
	}

	nextPosition := existing.Position
	if rawPos, ok := raw["position"]; ok {
		var pos *int32
		if err := json.Unmarshal(rawPos, &pos); err != nil {
			writeError(w, http.StatusBadRequest, "position must be an integer")
			return
		}
		if pos != nil {
			nextPosition = *pos
		}
	}

	updated, err := h.Queries.UpdateProjectResource(r.Context(), db.UpdateProjectResourceParams{
		ID:          existing.ID,
		ResourceRef: nextRef,
		Label:       nextLabel,
		Position:    nextPosition,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "this resource is already attached to the project")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update project resource")
		return
	}

	resp := projectResourceToResponse(updated)
	h.publish(
		protocol.EventProjectResourceUpdated,
		uuidToString(project.WorkspaceID),
		"member",
		userID,
		map[string]any{"resource": resp, "project_id": uuidToString(project.ID)},
	)
	writeJSON(w, http.StatusOK, resp)
}

// TestProjectResource performs a real reachability probe for a Gongfeng
// project resource and records the result in resource_ref. A 302 to the
// sign-in page is deliberately stored as auth_required instead of pretending
// repository content was readable.
func (h *Handler) TestProjectResource(w http.ResponseWriter, r *http.Request) {
	h.updateGongfengResourceState(w, r, "test")
}

// SyncProjectResource refreshes locally known branch/sync metadata. Full
// commit discovery requires a bound Gongfeng profile; until then this action
// records that the resource was refreshed without inventing a commit SHA.
func (h *Handler) SyncProjectResource(w http.ResponseWriter, r *http.Request) {
	h.updateGongfengResourceState(w, r, "sync")
}

// DisableProjectResource keeps the resource row for audit/history but removes
// it from training selection paths that filter disabled resources.
func (h *Handler) DisableProjectResource(w http.ResponseWriter, r *http.Request) {
	h.updateGongfengResourceState(w, r, "disable")
}

// EnableProjectResource re-activates a disabled Gongfeng project resource. The
// next test/sync is intentionally left pending instead of reusing stale status.
func (h *Handler) EnableProjectResource(w http.ResponseWriter, r *http.Request) {
	h.updateGongfengResourceState(w, r, "enable")
}

func (h *Handler) updateGongfengResourceState(w http.ResponseWriter, r *http.Request, action string) {
	project, ok := h.loadProjectForResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	resourceUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "resourceId"), "resource id")
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	existing, err := h.Queries.GetProjectResourceInWorkspace(r.Context(), db.GetProjectResourceInWorkspaceParams{
		ID: resourceUUID, WorkspaceID: project.WorkspaceID,
	})
	if err != nil || uuidToString(existing.ProjectID) != uuidToString(project.ID) {
		writeError(w, http.StatusNotFound, "project resource not found")
		return
	}
	if existing.ResourceType != "gongfeng_repo" {
		writeError(w, http.StatusBadRequest, "project resource is not a gongfeng_repo")
		return
	}
	var ref gongfengRepoRef
	if err := json.Unmarshal(existing.ResourceRef, &ref); err != nil {
		writeError(w, http.StatusBadRequest, "invalid gongfeng_repo resource_ref")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	switch action {
	case "test":
		result := probeGongfengURL(r.Context(), ref.URL)
		profile, hasProfile, err := h.loadUsableGongfengCredentialProfile(r.Context(), userID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load gongfeng credential profile")
			return
		}
		credentialProbe := gongfengCredentialProbeResult{ConnectionStatus: "not_configured", TestStatus: "failed"}
		if hasProfile {
			credentialProbe = probeGongfengWithCredential(r.Context(), ref, h.resolveExternalCredentialToken(profile))
		}
		ref = applyGongfengCredentialProbeResult(ref, result, credentialProbe, profile, hasProfile)
		ref.LastTestedAt = now
	case "sync":
		if ref.Disabled {
			writeError(w, http.StatusConflict, "disabled gongfeng_repo cannot be synced")
			return
		}
		if ref.Branch == "" {
			ref.Branch = ref.Ref
		}
		if ref.CommitSHA == "" {
			ref.CommitSHA = ref.HeadCommit
		}
		ref.SyncStatus = "synced"
		ref.LastSyncedAt = now
	case "disable":
		ref.Disabled = true
		ref.DisabledAt = now
		ref.ConnectionStatus = "disabled"
		ref.SyncStatus = "disabled"
		ref.TestStatus = "disabled"
	case "enable":
		ref.Disabled = false
		ref.DisabledAt = ""
		ref.ConnectionStatus = "pending_verification"
		ref.SyncStatus = "pending_verification"
		ref.TestStatus = "pending_verification"
	default:
		writeError(w, http.StatusBadRequest, "unknown project resource action")
		return
	}
	nextRef, err := validateAndNormalizeResourceRef(existing.ResourceType, mustMarshalRaw(ref))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	updated, err := h.Queries.UpdateProjectResource(r.Context(), db.UpdateProjectResourceParams{
		ID:          existing.ID,
		ResourceRef: nextRef,
		Label:       existing.Label,
		Position:    existing.Position,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update project resource")
		return
	}
	resp := projectResourceToResponse(updated)
	h.publish(
		protocol.EventProjectResourceUpdated,
		uuidToString(project.WorkspaceID),
		"member",
		userID,
		map[string]any{"resource": resp, "project_id": uuidToString(project.ID), "action": action},
	)
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) loadUsableGongfengCredentialProfile(ctx context.Context, userID string) (db.ExternalCredentialProfile, bool, error) {
	userUUID, ok := h.parseUserUUIDOrZero(userID)
	if !ok {
		return db.ExternalCredentialProfile{}, false, nil
	}
	profile, err := h.Queries.GetDefaultExternalCredentialProfileForUser(ctx, db.GetDefaultExternalCredentialProfileForUserParams{
		UserID:   userUUID,
		Provider: externalCredentialProviderGongfeng,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.ExternalCredentialProfile{}, false, nil
	}
	if err != nil {
		return db.ExternalCredentialProfile{}, false, err
	}
	if !isUsableGongfengCredentialProfile(profile) {
		return db.ExternalCredentialProfile{}, false, nil
	}
	return profile, true, nil
}

func isUsableGongfengCredentialProfile(profile db.ExternalCredentialProfile) bool {
	if profile.Provider != externalCredentialProviderGongfeng {
		return false
	}
	if profile.Status == "disabled" || profile.Status == "failed" {
		return false
	}
	return profile.SecretRef != "" || len(profile.EncryptedSecret) > 0
}

func applyGongfengCredentialProbeResult(ref gongfengRepoRef, result gongfengProbeResult, credentialProbe gongfengCredentialProbeResult, profile db.ExternalCredentialProfile, hasProfile bool) gongfengRepoRef {
	ref.ConnectionStatus = result.ConnectionStatus
	ref.TestStatus = result.TestStatus
	ref.CredentialStatus = "not_configured"
	ref.CredentialProfileID = ""
	ref.CredentialProfileStatus = ""
	ref.CredentialSecretHint = ""
	ref.CredentialLastVerifiedAt = ""
	ref.CredentialProbeStatus = ""
	ref.CredentialProbeHTTPStatus = ""
	ref.CredentialProbeTarget = ""
	ref.UnauthenticatedConnectionStatus = ""
	if !hasProfile {
		return ref
	}

	ref.CredentialStatus = "account_profile_configured"
	ref.CredentialProfileID = uuidToString(profile.ID)
	ref.CredentialProfileStatus = profile.Status
	if profile.SecretRef != "" {
		ref.CredentialSecretHint = "secret_ref"
	} else {
		ref.CredentialSecretHint = profile.SecretHint
	}
	ref.CredentialLastVerifiedAt = timestampToString(profile.LastVerifiedAt)
	ref.CredentialProbeStatus = credentialProbe.ConnectionStatus
	ref.CredentialProbeHTTPStatus = credentialProbe.HTTPStatus
	ref.CredentialProbeTarget = credentialProbe.Target
	if result.ConnectionStatus == "auth_required" && credentialProbe.ConnectionStatus == "credential_backed" && credentialProbe.TestStatus == "passed" {
		ref.UnauthenticatedConnectionStatus = result.ConnectionStatus
		ref.ConnectionStatus = "credential_backed"
		ref.TestStatus = "passed"
	}
	return ref
}

type gongfengProbeResult struct {
	ConnectionStatus string
	TestStatus       string
}

type gongfengCredentialProbeResult struct {
	ConnectionStatus string
	TestStatus       string
	HTTPStatus       string
	Target           string
}

func probeGongfengURL(ctx context.Context, rawURL string) gongfengProbeResult {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, rawURL, nil)
	if err != nil {
		return gongfengProbeResult{ConnectionStatus: "invalid_url", TestStatus: "failed"}
	}
	client := &http.Client{
		Timeout: 8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return gongfengProbeResult{ConnectionStatus: "unreachable", TestStatus: "failed"}
	}
	defer resp.Body.Close()
	location := resp.Header.Get("Location")
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden ||
		(resp.StatusCode >= 300 && resp.StatusCode < 400 && strings.Contains(location, "/users/sign_in")) {
		return gongfengProbeResult{ConnectionStatus: "auth_required", TestStatus: "passed"}
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		return gongfengProbeResult{ConnectionStatus: "reachable", TestStatus: "passed"}
	}
	return gongfengProbeResult{ConnectionStatus: fmt.Sprintf("http_%d", resp.StatusCode), TestStatus: "failed"}
}

func probeGongfengWithCredential(ctx context.Context, ref gongfengRepoRef, token string) gongfengCredentialProbeResult {
	token = strings.TrimSpace(token)
	if token == "" {
		return gongfengCredentialProbeResult{ConnectionStatus: "credential_token_unavailable", TestStatus: "failed"}
	}
	target := gongfengAPIProjectURL(ref.ProjectPath)
	if target == "" {
		target = ref.URL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return gongfengCredentialProbeResult{ConnectionStatus: "invalid_url", TestStatus: "failed", Target: target}
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	req.Header.Set("Private-Token", token)
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{
		Timeout: 8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return gongfengCredentialProbeResult{ConnectionStatus: "credential_probe_unreachable", TestStatus: "failed", Target: target}
	}
	defer resp.Body.Close()
	status := fmt.Sprintf("%d", resp.StatusCode)
	location := resp.Header.Get("Location")
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return gongfengCredentialProbeResult{ConnectionStatus: "credential_backed", TestStatus: "passed", HTTPStatus: status, Target: target}
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return gongfengCredentialProbeResult{ConnectionStatus: "credential_unauthorized", TestStatus: "failed", HTTPStatus: status, Target: target}
	}
	if resp.StatusCode == http.StatusForbidden {
		return gongfengCredentialProbeResult{ConnectionStatus: "credential_forbidden", TestStatus: "failed", HTTPStatus: status, Target: target}
	}
	if resp.StatusCode == http.StatusNotFound {
		return gongfengCredentialProbeResult{ConnectionStatus: "credential_not_found_or_no_access", TestStatus: "failed", HTTPStatus: status, Target: target}
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 && strings.Contains(location, "/users/sign_in") {
		return gongfengCredentialProbeResult{ConnectionStatus: "credential_redirected_to_login", TestStatus: "failed", HTTPStatus: status, Target: target}
	}
	return gongfengCredentialProbeResult{ConnectionStatus: fmt.Sprintf("credential_http_%d", resp.StatusCode), TestStatus: "failed", HTTPStatus: status, Target: target}
}

func gongfengAPIProjectURL(projectPath string) string {
	projectPath = strings.Trim(strings.TrimSpace(projectPath), "/")
	if projectPath == "" {
		return ""
	}
	return "https://git.code.tencent.com/api/v3/projects/" + url.PathEscape(projectPath)
}

func mustMarshalRaw(v any) json.RawMessage {
	out, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return out
}

// findLocalDirectoryConflict enforces "at most one local_directory resource
// per (project, daemon)". The daemon picks the first matching daemon_id row
// out of a task's resources (findLocalDirectoryAssignment), so letting a
// project carry two rows for the same daemon would mean the agent silently
// writes into whichever happens to come back first — a safety hazard for a
// feature that operates directly on the user's real working directory.
//
// The DB-level UNIQUE(project_id, resource_type, resource_ref) constraint
// alone is not enough here: it only fires on full ref-JSON equality, so a
// different local_path or even a typoed label on the same daemon would slip
// through. We do the daemon-scoped check here in application code instead.
//
// `excludeID` lets the update path ignore the row being edited.
func (h *Handler) findLocalDirectoryConflict(ctx context.Context, projectID pgtype.UUID, resourceType string, normalizedRef json.RawMessage, excludeID pgtype.UUID) (bool, error) {
	if resourceType != "local_directory" {
		return false, nil
	}
	var incoming localDirectoryRef
	if err := json.Unmarshal(normalizedRef, &incoming); err != nil {
		return false, err
	}
	rows, err := h.Queries.ListProjectResources(ctx, projectID)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if row.ResourceType != "local_directory" {
			continue
		}
		if excludeID.Valid && uuidToString(row.ID) == uuidToString(excludeID) {
			continue
		}
		var existing localDirectoryRef
		if err := json.Unmarshal(row.ResourceRef, &existing); err != nil {
			continue
		}
		// Daemon-scoped uniqueness: one local_directory per daemon per
		// project. Different daemons can each carry one row (one per
		// user device); the daemon-side resolver routes each daemon to
		// its own assignment by daemon_id.
		if existing.DaemonID == incoming.DaemonID {
			return true, nil
		}
	}
	return false, nil
}

// DeleteProjectResource removes a resource from a project.
func (h *Handler) DeleteProjectResource(w http.ResponseWriter, r *http.Request) {
	project, ok := h.loadProjectForResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	resourceUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "resourceId"), "resource id")
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	resource, err := h.Queries.GetProjectResourceInWorkspace(r.Context(), db.GetProjectResourceInWorkspaceParams{
		ID: resourceUUID, WorkspaceID: project.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "project resource not found")
		return
	}
	if uuidToString(resource.ProjectID) != uuidToString(project.ID) {
		writeError(w, http.StatusNotFound, "project resource not found")
		return
	}
	if err := h.Queries.DeleteProjectResource(r.Context(), resource.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete project resource")
		return
	}
	h.publish(
		protocol.EventProjectResourceDeleted,
		uuidToString(project.WorkspaceID),
		"member",
		userID,
		map[string]any{
			"project_id":  uuidToString(project.ID),
			"resource_id": uuidToString(resource.ID),
		},
	)
	w.WriteHeader(http.StatusNoContent)
}

// parseUserUUIDOrZero converts a user ID string to a pgtype.UUID, returning a
// zero value on any error so the caller can store NULL for created_by when the
// authenticated principal is not a workspace member (e.g. internal-server use).
func (h *Handler) parseUserUUIDOrZero(userID string) (pgtype.UUID, bool) {
	if userID == "" {
		return pgtype.UUID{}, false
	}
	u, err := parseUUIDLoose(userID)
	if err != nil {
		return pgtype.UUID{}, false
	}
	return u, true
}

// parseUUIDLoose mirrors util.ParseUUID but lives here to avoid pulling util
// into a tiny one-off helper. Keep the body minimal.
func parseUUIDLoose(s string) (pgtype.UUID, error) {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		return pgtype.UUID{}, err
	}
	return u, nil
}

// listProjectResourcesForProject is a small helper used by the daemon claim
// handler to attach project resources to outgoing tasks.
func (h *Handler) listProjectResourcesForProject(ctx context.Context, projectID pgtype.UUID) []db.ProjectResource {
	if !projectID.Valid {
		return nil
	}
	rows, err := h.Queries.ListProjectResources(ctx, projectID)
	if err != nil {
		return nil
	}
	return rows
}
