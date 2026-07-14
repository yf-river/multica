package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/logger"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

var nonAlpha = regexp.MustCompile(`[^a-zA-Z]`)
var workspaceSlugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

var workspaceBooleanSettingDefaults = map[string]bool{
	"github_enabled":               true,
	"github_pr_sidebar_enabled":    true,
	"co_authored_by_enabled":       true,
	"github_auto_link_prs_enabled": true,
}

func canonicalizeWorkspaceSettings(settings map[string]any) (map[string]any, error) {
	canonical := maps.Clone(settings)
	if canonical == nil {
		canonical = make(map[string]any, len(workspaceBooleanSettingDefaults))
	}
	for key, defaultValue := range workspaceBooleanSettingDefaults {
		value, exists := canonical[key]
		if !exists {
			canonical[key] = defaultValue
			continue
		}
		if _, ok := value.(bool); !ok {
			return nil, fmt.Errorf("settings.%s must be a boolean", key)
		}
	}
	return canonical, nil
}

// generateIssuePrefix produces a 2-5 char uppercase prefix from a workspace name.
// Examples: "Jiayuan's Workspace" → "JIA", "My Team" → "MYT", "AB" → "AB".
func generateIssuePrefix(name string) string {
	letters := nonAlpha.ReplaceAllString(name, "")
	if len(letters) == 0 {
		return "WS"
	}
	letters = strings.ToUpper(letters)
	if len(letters) > 3 {
		letters = letters[:3]
	}
	return letters
}

type WorkspaceResponse struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Slug        string         `json:"slug"`
	Description *string        `json:"description"`
	Context     *string        `json:"context"`
	Settings    map[string]any `json:"settings"`
	Repos       []any          `json:"repos"`
	IssuePrefix string         `json:"issue_prefix"`
	AvatarURL   *string        `json:"avatar_url"`
	CreatedAt   string         `json:"created_at"`
	UpdatedAt   string         `json:"updated_at"`
}

func workspaceToResponse(w db.Workspace) (WorkspaceResponse, error) {
	var settings map[string]any
	if err := json.Unmarshal(w.Settings, &settings); err != nil {
		return WorkspaceResponse{}, fmt.Errorf("decode workspace settings: %w", err)
	}
	if settings == nil {
		return WorkspaceResponse{}, fmt.Errorf("decode workspace settings: expected JSON object")
	}
	var repos []any
	if err := json.Unmarshal(w.Repos, &repos); err != nil {
		return WorkspaceResponse{}, fmt.Errorf("decode workspace repos: %w", err)
	}
	if repos == nil {
		return WorkspaceResponse{}, fmt.Errorf("decode workspace repos: expected JSON array")
	}
	return WorkspaceResponse{
		ID:          uuidToString(w.ID),
		Name:        w.Name,
		Slug:        w.Slug,
		Description: textToPtr(w.Description),
		Context:     textToPtr(w.Context),
		Settings:    settings,
		Repos:       repos,
		IssuePrefix: w.IssuePrefix,
		AvatarURL:   textToPtr(w.AvatarUrl),
		CreatedAt:   timestampToString(w.CreatedAt),
		UpdatedAt:   timestampToString(w.UpdatedAt),
	}, nil
}

func (h *Handler) ListWorkspaces(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	workspaces, err := h.Queries.ListWorkspaces(r.Context(), parseUUID(userID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list workspaces")
		return
	}

	resp := make([]WorkspaceResponse, len(workspaces))
	for i, ws := range workspaces {
		resp[i], err = workspaceToResponse(ws)
		if err != nil {
			slog.Error("encode workspace response failed", append(logger.RequestAttrs(r), "workspace_id", uuidToString(ws.ID), "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to list workspaces")
			return
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) GetWorkspace(w http.ResponseWriter, r *http.Request) {
	id := workspaceIDFromURL(r, "id")
	idUUID, ok := parseUUIDOrBadRequest(w, id, "workspace id")
	if !ok {
		return
	}

	ws, err := h.Queries.GetWorkspace(r.Context(), idUUID)
	if err != nil {
		writeEntityLoadError(w, r, err, "workspace", "workspace_id", id)
		return
	}
	resp, err := workspaceToResponse(ws)
	if err != nil {
		slog.Error("encode workspace response failed", append(logger.RequestAttrs(r), "workspace_id", id, "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to load workspace")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

type CreateWorkspaceRequest struct {
	Name        string  `json:"name"`
	Slug        string  `json:"slug"`
	Description *string `json:"description"`
	Context     *string `json:"context"`
	IssuePrefix *string `json:"issue_prefix"`
}

func (h *Handler) CreateWorkspace(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	// Self-host gate (#3433): when the operator has set
	// DISABLE_WORKSPACE_CREATION=true, no caller — including existing
	// workspace owners — may create additional workspaces. The frontend
	// hides every "Create workspace" affordance via /api/config, but the
	// 403 here is the only authoritative check.
	if h.cfg.DisableWorkspaceCreation {
		writeError(w, http.StatusForbidden, "workspace creation is disabled for this instance")
		return
	}

	var req CreateWorkspaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Slug = strings.ToLower(strings.TrimSpace(req.Slug))
	if req.Name == "" || req.Slug == "" {
		writeError(w, http.StatusBadRequest, "name and slug are required")
		return
	}
	if !workspaceSlugPattern.MatchString(req.Slug) {
		writeError(w, http.StatusBadRequest, "slug must contain only lowercase letters, numbers, and hyphens")
		return
	}
	if isReservedSlug(req.Slug) {
		writeError(w, http.StatusBadRequest, "slug is reserved")
		return
	}
	issuePrefix := generateIssuePrefix(req.Name)
	if req.IssuePrefix != nil && strings.TrimSpace(*req.IssuePrefix) != "" {
		issuePrefix = strings.ToUpper(strings.TrimSpace(*req.IssuePrefix))
	}
	actorID := parseUUID(userID)
	requestHash, err := hashRequestFingerprint(struct {
		Name        string  `json:"name"`
		Slug        string  `json:"slug"`
		Description *string `json:"description"`
		Context     *string `json:"context"`
		IssuePrefix string  `json:"issue_prefix"`
	}{
		Name: req.Name, Slug: req.Slug, Description: req.Description,
		Context: req.Context, IssuePrefix: issuePrefix,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fingerprint workspace request")
		return
	}
	idempotencyKey, ok := optionalIdempotencyKey(w, r)
	if !ok {
		return
	}
	workspaceRequestID := idempotencyKey
	replay, found, replayErr := h.loadWorkspaceCreateReplay(r.Context(), workspaceRequestID, actorID, idempotencyKey, requestHash)
	if replayErr != nil {
		writeWorkspaceCreateReplayError(w, replayErr)
		return
	}
	if found {
		h.writeWorkspaceCreateReplay(w, r.Context(), workspaceRequestID, actorID, replay)
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create workspace")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	qtx := h.Queries.WithTx(tx)
	ws, err := qtx.CreateWorkspace(r.Context(), db.CreateWorkspaceParams{
		ID:          workspaceRequestID,
		Name:        req.Name,
		Slug:        req.Slug,
		Description: ptrToText(req.Description),
		Context:     ptrToText(req.Context),
		IssuePrefix: issuePrefix,
	})
	if err != nil {
		if isUniqueViolation(err) {
			_ = tx.Rollback(r.Context())
			replay, found, replayErr := h.loadWorkspaceCreateReplay(r.Context(), workspaceRequestID, actorID, idempotencyKey, requestHash)
			if replayErr != nil {
				writeWorkspaceCreateReplayError(w, replayErr)
				return
			}
			if found {
				h.writeWorkspaceCreateReplay(w, r.Context(), workspaceRequestID, actorID, replay)
				return
			}
			writeError(w, http.StatusConflict, "workspace slug already exists")
			return
		}
		slog.Error("create workspace failed", append(logger.RequestAttrs(r), "slug", req.Slug, "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to create workspace")
		return
	}

	_, err = qtx.CreateMember(r.Context(), db.CreateMemberParams{
		WorkspaceID: ws.ID,
		UserID:      parseUUID(userID),
		Role:        "owner",
	})
	if err != nil {
		slog.Error("create workspace owner failed", append(logger.RequestAttrs(r), "workspace_id", uuidToString(ws.ID), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to add workspace owner")
		return
	}
	err = reserveResourceCreateRequest(r.Context(), qtx, workspaceRequestID, actorID, resourceTypeWorkspace, idempotencyKey, requestHash)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reserve workspace request")
		return
	}

	wsID := uuidToString(ws.ID)
	resp, err := workspaceToResponse(ws)
	if err != nil {
		slog.Error("encode created workspace response failed", append(logger.RequestAttrs(r), "workspace_id", wsID, "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to load created workspace")
		return
	}
	if err := completeResourceCreateRequest(r.Context(), qtx, workspaceRequestID, actorID, resourceTypeWorkspace, idempotencyKey, requestHash, ws.ID, resp); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to complete workspace request")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create workspace")
		return
	}

	// "Is this the user's first workspace?" is derived in PostHog by looking
	// at whether they have a prior workspace_created event, not stamped at
	// emit time. Stamping here would race under concurrent creates without
	// a schema change, and the event stream answers the question exactly.
	obsmetrics.RecordEvent(h.Analytics, h.Metrics, analytics.WorkspaceCreated(userID, wsID))

	slog.Info("workspace created", append(logger.RequestAttrs(r), "workspace_id", wsID, "name", ws.Name, "slug", ws.Slug)...)
	writeJSON(w, http.StatusCreated, resp)
}

type UpdateWorkspaceRequest struct {
	Name        *string         `json:"name"`
	Description *string         `json:"description"`
	Context     *string         `json:"context"`
	Settings    *map[string]any `json:"settings"`
	Repos       any             `json:"repos"`
	IssuePrefix *string         `json:"issue_prefix"`
	AvatarURL   *string         `json:"avatar_url"`
}

type workspaceRepoRef struct {
	URL              string `json:"url"`
	Description      string `json:"description,omitempty"`
	Provider         string `json:"provider,omitempty"`
	ProjectPath      string `json:"project_path,omitempty"`
	DefaultBranch    string `json:"default_branch,omitempty"`
	HeadCommit       string `json:"head_commit,omitempty"`
	ConnectionStatus string `json:"connection_status,omitempty"`
	SyncStatus       string `json:"sync_status,omitempty"`
	TestStatus       string `json:"test_status,omitempty"`
	LastTestedAt     string `json:"last_tested_at,omitempty"`
	LastSyncedAt     string `json:"last_synced_at,omitempty"`
}

func validateAndNormalizeWorkspaceRepos(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}

	var repos []workspaceRepoRef
	if err := json.Unmarshal(raw, &repos); err != nil {
		return nil, fmt.Errorf("repos must be an array of repository objects: %w", err)
	}

	normalized := make([]workspaceRepoRef, 0, len(repos))
	seen := make(map[string]struct{}, len(repos))
	for i, repo := range repos {
		repo.URL = strings.TrimSpace(repo.URL)
		repo.Description = strings.TrimSpace(repo.Description)
		repo.Provider = strings.TrimSpace(repo.Provider)
		repo.ProjectPath = strings.Trim(strings.TrimSpace(repo.ProjectPath), "/")
		repo.DefaultBranch = strings.TrimSpace(repo.DefaultBranch)
		repo.HeadCommit = strings.TrimSpace(repo.HeadCommit)
		repo.ConnectionStatus = strings.TrimSpace(repo.ConnectionStatus)
		repo.SyncStatus = strings.TrimSpace(repo.SyncStatus)
		repo.TestStatus = strings.TrimSpace(repo.TestStatus)
		repo.LastTestedAt = strings.TrimSpace(repo.LastTestedAt)
		repo.LastSyncedAt = strings.TrimSpace(repo.LastSyncedAt)
		if repo.URL == "" {
			return nil, fmt.Errorf("repos[%d]: url is required", i)
		}
		if !isValidGitRepoURL(repo.URL) {
			return nil, fmt.Errorf("repos[%d]: url must be a valid http(s) or ssh git URL", i)
		}
		parsedURL, _ := url.Parse(repo.URL)
		if parsedURL != nil && strings.EqualFold(parsedURL.Hostname(), "git.code.tencent.com") {
			parsed, err := parseGongfengURL(repo.URL)
			if err != nil {
				return nil, fmt.Errorf("repos[%d]: %w", i, err)
			}
			repo.Provider = "gongfeng"
			if repo.ProjectPath == "" || repo.DefaultBranch == "" {
				return nil, fmt.Errorf("repos[%d]: gongfeng repositories require project_path and default_branch", i)
			}
			if repo.ProjectPath != parsed.ProjectPath {
				return nil, fmt.Errorf("repos[%d]: project_path must match the Gongfeng repository URL", i)
			}
		}
		if _, ok := seen[repo.URL]; ok {
			continue
		}
		seen[repo.URL] = struct{}{}
		normalized = append(normalized, repo)
	}

	out, err := json.Marshal(normalized)
	if err != nil {
		return nil, err
	}
	return out, nil
}

type resolveWorkspaceRepoRequest struct {
	URL           string `json:"url"`
	DefaultBranch string `json:"default_branch"`
}

type probeWorkspaceRepoRequest struct {
	URL string `json:"url"`
}

type probeWorkspaceRepoResponse struct {
	URL              string   `json:"url"`
	Provider         string   `json:"provider"`
	ProjectPath      string   `json:"project_path"`
	DefaultBranch    string   `json:"default_branch"`
	Branches         []string `json:"branches"`
	ConnectionStatus string   `json:"connection_status"`
	TestStatus       string   `json:"test_status"`
}

func (h *Handler) prepareGongfengWorkspaceRepo(w http.ResponseWriter, r *http.Request, userID string, rawURL string, missingCredentialMessage string) (parsedGongfengURL, db.ExternalCredentialProfile, string, bool) {
	if rawURL == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return parsedGongfengURL{}, db.ExternalCredentialProfile{}, "", false
	}
	if !isValidGitRepoURL(rawURL) {
		writeError(w, http.StatusBadRequest, "url must be a valid http(s) or ssh git URL")
		return parsedGongfengURL{}, db.ExternalCredentialProfile{}, "", false
	}
	if !strings.Contains(rawURL, "git.code.tencent.com") {
		writeError(w, http.StatusBadRequest, "only Gongfeng repository URLs are supported")
		return parsedGongfengURL{}, db.ExternalCredentialProfile{}, "", false
	}
	parsed, err := parseGongfengURL(rawURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return parsedGongfengURL{}, db.ExternalCredentialProfile{}, "", false
	}
	profile, hasProfile, err := h.loadUsableGongfengCredentialProfile(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load gongfeng credential profile")
		return parsedGongfengURL{}, db.ExternalCredentialProfile{}, "", false
	}
	if !hasProfile {
		writeError(w, http.StatusBadRequest, missingCredentialMessage)
		return parsedGongfengURL{}, db.ExternalCredentialProfile{}, "", false
	}
	token, err := h.resolveExternalCredentialToken(profile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve gongfeng credential token")
		return parsedGongfengURL{}, db.ExternalCredentialProfile{}, "", false
	}
	return parsed, profile, token, true
}

func (h *Handler) ProbeWorkspaceRepo(w http.ResponseWriter, r *http.Request) {
	id := workspaceIDFromURL(r, "id")
	if _, ok := parseUUIDOrBadRequest(w, id, "workspace id"); !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req probeWorkspaceRepoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	rawURL := strings.TrimSpace(req.URL)
	parsed, profile, token, ok := h.prepareGongfengWorkspaceRepo(
		w,
		r,
		userID,
		rawURL,
		"gongfeng credential is required to inspect branches",
	)
	if !ok {
		return
	}
	defaultBranch, err := fetchGongfengDefaultBranch(r.Context(), parsed.ProjectPath, token)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	branches, err := fetchGongfengBranches(r.Context(), parsed.ProjectPath, token)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	credentialProbe := probeGongfengWithCredential(r.Context(), gongfengRepoRef{
		URL:         rawURL,
		ProjectPath: parsed.ProjectPath,
	}, token)
	ref := applyGongfengCredentialProbeResult(
		gongfengRepoRef{URL: rawURL, ProjectPath: parsed.ProjectPath},
		probeGongfengURL(r.Context(), rawURL),
		credentialProbe,
		profile,
		true,
	)
	writeJSON(w, http.StatusOK, probeWorkspaceRepoResponse{
		URL:              rawURL,
		Provider:         "gongfeng",
		ProjectPath:      parsed.ProjectPath,
		DefaultBranch:    defaultBranch,
		Branches:         branches,
		ConnectionStatus: ref.ConnectionStatus,
		TestStatus:       ref.TestStatus,
	})
}

func (h *Handler) ResolveWorkspaceRepo(w http.ResponseWriter, r *http.Request) {
	id := workspaceIDFromURL(r, "id")
	if _, ok := parseUUIDOrBadRequest(w, id, "workspace id"); !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req resolveWorkspaceRepoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	rawURL := strings.TrimSpace(req.URL)
	requestedBranch := strings.TrimSpace(req.DefaultBranch)
	parsed, profile, token, ok := h.prepareGongfengWorkspaceRepo(
		w,
		r,
		userID,
		rawURL,
		"gongfeng credential is required to resolve default branch",
	)
	if !ok {
		return
	}
	branch := requestedBranch
	if branch == "" {
		branch = parsedGongfengWorkspaceRepoBranch(parsed)
	}
	if branch == "" {
		var err error
		branch, err = fetchGongfengDefaultBranch(r.Context(), parsed.ProjectPath, token)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	commit, err := fetchGongfengBranchHeadCommit(r.Context(), parsed.ProjectPath, branch, token)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	credentialProbe := probeGongfengWithCredential(r.Context(), gongfengRepoRef{
		URL:         rawURL,
		ProjectPath: parsed.ProjectPath,
	}, token)
	ref := applyGongfengCredentialProbeResult(
		gongfengRepoRef{URL: rawURL, ProjectPath: parsed.ProjectPath},
		probeGongfengURL(r.Context(), rawURL),
		credentialProbe,
		profile,
		true,
	)
	writeJSON(w, http.StatusOK, workspaceRepoRef{
		URL:              rawURL,
		Provider:         "gongfeng",
		ProjectPath:      parsed.ProjectPath,
		DefaultBranch:    branch,
		HeadCommit:       commit,
		ConnectionStatus: ref.ConnectionStatus,
		SyncStatus:       "synced",
		TestStatus:       ref.TestStatus,
		LastTestedAt:     now,
		LastSyncedAt:     now,
	})
}

func parsedGongfengWorkspaceRepoBranch(parsed parsedGongfengURL) string {
	switch parsed.ResourceKind {
	case "branch", "commits":
		return strings.TrimSpace(parsed.Ref)
	default:
		return ""
	}
}

func (h *Handler) UpdateWorkspace(w http.ResponseWriter, r *http.Request) {
	id := workspaceIDFromURL(r, "id")
	idUUID, ok := parseUUIDOrBadRequest(w, id, "workspace id")
	if !ok {
		return
	}

	var req UpdateWorkspaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	params := db.UpdateWorkspaceParams{
		ID: idUUID,
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}
		params.Name = pgtype.Text{String: name, Valid: true}
	}
	if req.Description != nil {
		params.Description = pgtype.Text{String: *req.Description, Valid: true}
	}
	if req.Context != nil {
		params.Context = pgtype.Text{String: *req.Context, Valid: true}
	}
	if req.Settings != nil {
		canonical, err := canonicalizeWorkspaceSettings(*req.Settings)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s, err := json.Marshal(canonical)
		if err != nil {
			writeError(w, http.StatusBadRequest, "settings must be a JSON object")
			return
		}
		params.Settings = s
	}
	if req.Repos != nil {
		reposJSON, err := validateAndNormalizeWorkspaceRepos(req.Repos)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := h.ensureWorkspaceReposKeepGongfengProjectResources(r.Context(), idUUID, reposJSON); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		params.Repos = reposJSON
	}
	if req.IssuePrefix != nil {
		prefix := strings.ToUpper(strings.TrimSpace(*req.IssuePrefix))
		if prefix != "" {
			params.IssuePrefix = pgtype.Text{String: prefix, Valid: true}
		}
	}
	if req.AvatarURL != nil {
		params.AvatarUrl = pgtype.Text{String: *req.AvatarURL, Valid: true}
	}

	ws, err := h.Queries.UpdateWorkspace(r.Context(), params)
	if err != nil {
		slog.Warn("update workspace failed", append(logger.RequestAttrs(r), "error", err, "workspace_id", id)...)
		writeError(w, http.StatusInternalServerError, "failed to update workspace: "+err.Error())
		return
	}

	slog.Info("workspace updated", append(logger.RequestAttrs(r), "workspace_id", id)...)
	resp, err := workspaceToResponse(ws)
	if err != nil {
		slog.Error("encode updated workspace response failed", append(logger.RequestAttrs(r), "workspace_id", id, "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to load updated workspace")
		return
	}
	userID := requestUserID(r)
	h.publish(protocol.EventWorkspaceUpdated, uuidToString(ws.ID), "member", userID, map[string]any{"workspace": resp})

	writeJSON(w, http.StatusOK, resp)
}

type MemberWithUserResponse struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	UserID      string  `json:"user_id"`
	Role        string  `json:"role"`
	CreatedAt   string  `json:"created_at"`
	Name        string  `json:"name"`
	Account     string  `json:"account"`
	AvatarURL   *string `json:"avatar_url"`
}

func (h *Handler) ListMembersWithUser(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	members, err := h.Queries.ListMembersWithUser(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list members")
		return
	}

	resp := make([]MemberWithUserResponse, len(members))
	for i, m := range members {
		resp[i] = MemberWithUserResponse{
			ID:          uuidToString(m.ID),
			WorkspaceID: uuidToString(m.WorkspaceID),
			UserID:      uuidToString(m.UserID),
			Role:        m.Role,
			CreatedAt:   timestampToString(m.CreatedAt),
			Name:        m.UserName,
			Account:     m.UserAccount,
			AvatarURL:   textToPtr(m.UserAvatarUrl),
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

type CreateMemberRequest struct {
	Account  string `json:"account"`
	Name     string `json:"name"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

func memberWithUserResponse(member db.Member, user db.User) MemberWithUserResponse {
	return MemberWithUserResponse{
		ID:          uuidToString(member.ID),
		WorkspaceID: uuidToString(member.WorkspaceID),
		UserID:      uuidToString(member.UserID),
		Role:        member.Role,
		CreatedAt:   timestampToString(member.CreatedAt),
		Name:        user.Name,
		Account:     user.Account,
		AvatarURL:   textToPtr(user.AvatarUrl),
	}
}

func normalizeMemberRole(role string) (string, bool) {
	if role == "" {
		return "member", true
	}

	role = strings.TrimSpace(role)
	switch role {
	case "owner", "admin", "member":
		return role, true
	default:
		return "", false
	}
}

func (h *Handler) CreateMember(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	requester, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}
	if requester.Role != "owner" && requester.Role != "admin" {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	var req CreateMemberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	account, ok := normalizeAccount(req.Account)
	if !ok {
		writeError(w, http.StatusBadRequest, "account must be 3-64 characters using letters, numbers, dot, dash, or underscore")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = account
	}
	role, valid := normalizeMemberRole(req.Role)
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid member role")
		return
	}
	if role == "owner" && requester.Role != "owner" {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}
	requestHash, err := hashRequestFingerprint(struct {
		Account          string `json:"account"`
		Name             string `json:"name"`
		Role             string `json:"role"`
		PasswordProvided bool   `json:"password_provided"`
	}{Account: account, Name: name, Role: role, PasswordProvided: req.Password != ""})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fingerprint workspace member request")
		return
	}
	idempotencyKey, ok := optionalIdempotencyKey(w, r)
	if !ok {
		return
	}
	actorID := requester.UserID
	if replay, found, err := h.loadWorkspaceMemberCreateReplay(
		r.Context(), requester.WorkspaceID, actorID, idempotencyKey, requestHash,
	); err != nil {
		writeWorkspaceMemberCreateReplayError(w, err)
		return
	} else if found {
		writeJSON(w, http.StatusCreated, replay)
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start member transaction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	err = reserveResourceCreateRequest(r.Context(), qtx, requester.WorkspaceID, actorID, resourceTypeWorkspaceMember, idempotencyKey, requestHash)
	if errors.Is(err, pgx.ErrNoRows) {
		replay, replayErr := loadReplayAfterReservationConflict(r.Context(), tx, func() (MemberWithUserResponse, bool, error) {
			return h.loadWorkspaceMemberCreateReplay(
				r.Context(), requester.WorkspaceID, actorID, idempotencyKey, requestHash,
			)
		})
		if replayErr != nil {
			writeWorkspaceMemberCreateReplayError(w, replayErr)
			return
		}
		writeJSON(w, http.StatusCreated, replay)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reserve workspace member request")
		return
	}

	user, err := qtx.GetUserByAccount(r.Context(), account)
	if errors.Is(err, pgx.ErrNoRows) {
		if msg := validatePassword(req.Password); msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		passwordHash, hashErr := hashPassword(req.Password)
		if hashErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to hash password")
			return
		}
		user, err = qtx.CreateUserWithPassword(r.Context(), db.CreateUserWithPasswordParams{
			Name: name, Account: account,
			PasswordHash: pgtype.Text{String: passwordHash, Valid: true},
		})
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create or load user")
		return
	}

	member, err := qtx.CreateMemberWithID(r.Context(), db.CreateMemberWithIDParams{
		ID: idempotencyKey, WorkspaceID: requester.WorkspaceID, UserID: user.ID, Role: role,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "user is already a member")
			return
		}
		slog.Warn("create member failed", append(logger.RequestAttrs(r), "error", err, "workspace_id", workspaceID, "account", account)...)
		writeError(w, http.StatusInternalServerError, "failed to create member")
		return
	}
	response := memberWithUserResponse(member, user)
	if err := completeResourceCreateRequest(
		r.Context(), qtx, requester.WorkspaceID, actorID, resourceTypeWorkspaceMember,
		idempotencyKey, requestHash, member.ID, response,
	); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to complete workspace member request")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create member")
		return
	}

	slog.Info("member added", append(logger.RequestAttrs(r), "member_id", uuidToString(member.ID), "workspace_id", workspaceID, "account", account, "role", role)...)
	userID := requestUserID(r)
	eventPayload := map[string]any{"member": response}
	if ws, err := h.Queries.GetWorkspace(r.Context(), requester.WorkspaceID); err == nil {
		eventPayload["workspace_name"] = ws.Name
	}
	h.publish(protocol.EventMemberAdded, uuidToString(requester.WorkspaceID), "member", userID, eventPayload)

	writeJSON(w, http.StatusCreated, response)
}

type UpdateMemberRequest struct {
	Role string `json:"role"`
}

func (h *Handler) workspaceMemberTarget(w http.ResponseWriter, r *http.Request, requester db.Member, memberID string) (db.Member, bool) {
	memberUUID, ok := parseUUIDOrBadRequest(w, memberID, "member id")
	if !ok {
		return db.Member{}, false
	}
	target, err := h.Queries.GetMember(r.Context(), memberUUID)
	if err != nil {
		writeEntityLoadError(w, r, err, "member", "member_id", memberID)
		return db.Member{}, false
	}
	if uuidToString(target.WorkspaceID) != uuidToString(requester.WorkspaceID) {
		writeError(w, http.StatusNotFound, "member not found")
		return db.Member{}, false
	}
	return target, true
}

func (h *Handler) ensureWorkspaceHasAnotherOwner(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, failureMessage string) bool {
	members, err := h.Queries.ListMembers(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, failureMessage)
		return false
	}
	if countOwners(members) <= 1 {
		writeError(w, http.StatusBadRequest, "workspace must have at least one owner")
		return false
	}
	return true
}

func (h *Handler) UpdateMember(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	requester, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}

	memberID := chi.URLParam(r, "memberId")
	target, ok := h.workspaceMemberTarget(w, r, requester, memberID)
	if !ok {
		return
	}

	var req UpdateMemberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Role) == "" {
		writeError(w, http.StatusBadRequest, "role is required")
		return
	}

	role, valid := normalizeMemberRole(req.Role)
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid member role")
		return
	}

	if (target.Role == "owner" || role == "owner") && requester.Role != "owner" {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	if target.Role == "owner" && role != "owner" {
		if !h.ensureWorkspaceHasAnotherOwner(w, r, target.WorkspaceID, "failed to update member") {
			return
		}
	}

	updatedMember, err := h.Queries.UpdateMemberRole(r.Context(), db.UpdateMemberRoleParams{
		ID:   target.ID,
		Role: role,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update member")
		return
	}

	h.MembershipCache.Invalidate(r.Context(), uuidToString(target.UserID), workspaceID)

	user, err := h.Queries.GetUser(r.Context(), updatedMember.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load member")
		return
	}

	userID := requestUserID(r)
	h.publish(protocol.EventMemberUpdated, uuidToString(requester.WorkspaceID), "member", userID, map[string]any{
		"member": memberWithUserResponse(updatedMember, user),
	})

	writeJSON(w, http.StatusOK, memberWithUserResponse(updatedMember, user))
}

func (h *Handler) DeleteMember(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	requester, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}

	memberID := chi.URLParam(r, "memberId")
	target, ok := h.workspaceMemberTarget(w, r, requester, memberID)
	if !ok {
		return
	}

	if target.Role == "owner" && requester.Role != "owner" {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	if target.Role == "owner" {
		if !h.ensureWorkspaceHasAnotherOwner(w, r, target.WorkspaceID, "failed to delete member") {
			return
		}
	}

	requesterUserID := requestUserID(r)
	result, err := h.revokeAndRemoveMember(r.Context(), target.WorkspaceID, target.UserID, target.ID, parseUUID(requesterUserID))
	if err != nil {
		slog.Warn("delete member failed", append(logger.RequestAttrs(r), "error", err, "member_id", memberID, "workspace_id", workspaceID)...)
		writeError(w, http.StatusInternalServerError, "failed to delete member")
		return
	}

	h.MembershipCache.Invalidate(r.Context(), uuidToString(target.UserID), workspaceID)

	wsIDStr := uuidToString(requester.WorkspaceID)
	logRevocation(result, wsIDStr, uuidToString(target.UserID))
	h.publishRevocation(r.Context(), result, wsIDStr, "member", requesterUserID)

	slog.Info("member removed", append(logger.RequestAttrs(r), "member_id", uuidToString(target.ID), "workspace_id", workspaceID, "user_id", uuidToString(target.UserID))...)
	h.publish(protocol.EventMemberRemoved, wsIDStr, "member", requesterUserID, map[string]any{
		"member_id":    uuidToString(target.ID),
		"workspace_id": wsIDStr,
		"user_id":      uuidToString(target.UserID),
	})

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) LeaveWorkspace(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}

	if member.Role == "owner" {
		if !h.ensureWorkspaceHasAnotherOwner(w, r, member.WorkspaceID, "failed to leave workspace") {
			return
		}
	}

	result, err := h.revokeAndRemoveMember(r.Context(), member.WorkspaceID, member.UserID, member.ID, member.UserID)
	if err != nil {
		slog.Warn("leave workspace failed", append(logger.RequestAttrs(r), "error", err, "workspace_id", workspaceID)...)
		writeError(w, http.StatusInternalServerError, "failed to leave workspace")
		return
	}

	h.MembershipCache.Invalidate(r.Context(), uuidToString(member.UserID), workspaceID)

	userID := requestUserID(r)
	logRevocation(result, workspaceID, uuidToString(member.UserID))
	h.publishRevocation(r.Context(), result, workspaceID, "member", userID)

	slog.Info("member removed", append(logger.RequestAttrs(r), "member_id", uuidToString(member.ID), "workspace_id", workspaceID, "user_id", uuidToString(member.UserID))...)
	h.publish(protocol.EventMemberRemoved, workspaceID, "member", userID, map[string]any{
		"member_id":    uuidToString(member.ID),
		"workspace_id": workspaceID,
		"user_id":      uuidToString(member.UserID),
	})

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) DeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")

	// Defense in depth: the route is already gated by the
	// RequireWorkspaceRoleFromURL("owner") middleware, but we re-check here
	// so that the handler is safe regardless of how it gets wired up
	// (direct calls in tests, future router refactors, etc.).
	requester, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}
	if requester.Role != "owner" {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	// Invalidate membership cache for all workspace members before deletion.
	// After CASCADE deletes the member rows, cache entries become harmless
	// orphans (downstream lookups for the deleted workspace will fail), but
	// proactive invalidation prevents any stale-access window up to TTL.
	if members, err := h.Queries.ListMembers(r.Context(), requester.WorkspaceID); err == nil {
		for _, m := range members {
			h.MembershipCache.Invalidate(r.Context(), uuidToString(m.UserID), workspaceID)
		}
	}

	// At this point workspaceMember has resolved → workspaceID is a valid UUID
	// (the lookup would have errored otherwise), so reuse the resolved value.
	if err := h.Queries.DeleteWorkspace(r.Context(), requester.WorkspaceID); err != nil {
		slog.Warn("delete workspace failed", append(logger.RequestAttrs(r), "error", err, "workspace_id", workspaceID)...)
		writeError(w, http.StatusInternalServerError, "failed to delete workspace")
		return
	}

	slog.Info("workspace deleted", append(logger.RequestAttrs(r), "workspace_id", workspaceID)...)
	h.publish(protocol.EventWorkspaceDeleted, workspaceID, "member", requestUserID(r), map[string]any{
		"workspace_id": workspaceID,
	})

	w.WriteHeader(http.StatusNoContent)
}
