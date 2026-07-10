package handler

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// githubAPIBase is the base URL for GitHub's REST API. Mutable so tests can
// point fetchInstallationAccount at an httptest server without touching the
// real GitHub.
var githubAPIBase = "https://api.github.com"

// ── Response shapes ─────────────────────────────────────────────────────────

// GitHubInstallationResponse is the JSON shape returned by the installation
// list endpoint and broadcast on installation-related WS events.
//
// InstallationID is admin-only: the numeric GitHub installation_id is the
// management handle used by the Connect/Disconnect flows, so non-admin
// members receive responses with the field omitted. The list handler gates
// it by role; realtime broadcasts always omit it because the WS fanout has
// no per-recipient view (admins re-query the list endpoint on invalidation
// to recover the management handle).
type GitHubInstallationResponse struct {
	ID               string  `json:"id"`
	WorkspaceID      string  `json:"workspace_id"`
	InstallationID   *int64  `json:"installation_id,omitempty"`
	AccountLogin     string  `json:"account_login"`
	AccountType      string  `json:"account_type"`
	AccountAvatarURL *string `json:"account_avatar_url"`
	CreatedAt        string  `json:"created_at"`
}

type GitHubPullRequestResponse struct {
	ID              string  `json:"id"`
	WorkspaceID     string  `json:"workspace_id"`
	RepoOwner       string  `json:"repo_owner"`
	RepoName        string  `json:"repo_name"`
	Number          int32   `json:"number"`
	Title           string  `json:"title"`
	State           string  `json:"state"`
	HtmlURL         string  `json:"html_url"`
	Branch          *string `json:"branch"`
	AuthorLogin     *string `json:"author_login"`
	AuthorAvatarURL *string `json:"author_avatar_url"`
	MergedAt        *string `json:"merged_at"`
	ClosedAt        *string `json:"closed_at"`
	PRCreatedAt     string  `json:"pr_created_at"`
	PRUpdatedAt     string  `json:"pr_updated_at"`
	// Mergeable state mirrors GitHub's `mergeable_state` field. We only
	// surface `clean`/`dirty` in the UI today; other values (`blocked`,
	// `behind`, `unstable`, `unknown`) round-trip but render as unknown.
	MergeableState *string `json:"mergeable_state"`
	// ChecksConclusion is the aggregated state of the latest CI check
	// suites for the PR's current head SHA. One of "passed", "failed",
	// "pending", or nil when no completed suite has been observed.
	ChecksConclusion *string `json:"checks_conclusion"`
	// Per-suite counts that drive the card's segmented progress bar.
	// Always present on list rows; bare upsert broadcasts default to 0
	// and the frontend hides the bar when total == 0.
	ChecksPassed  int64 `json:"checks_passed"`
	ChecksFailed  int64 `json:"checks_failed"`
	ChecksPending int64 `json:"checks_pending"`
	// Diff stats (lines added/removed and file count) sourced from the
	// `pull_request` webhook payload. Legacy rows that pre-date this
	// field default to 0; the frontend treats total == 0 as "unknown"
	// and hides the stats row.
	Additions    int32 `json:"additions"`
	Deletions    int32 `json:"deletions"`
	ChangedFiles int32 `json:"changed_files"`
}

type LinkPullRequestRequest struct {
	Provider       string  `json:"provider"`
	ProjectPath    string  `json:"project_path"`
	RepoURL        string  `json:"repo_url"`
	Number         int32   `json:"number"`
	IID            int32   `json:"iid"`
	Title          string  `json:"title"`
	State          string  `json:"state"`
	HtmlURL        string  `json:"html_url"`
	SourceBranch   string  `json:"source_branch"`
	TargetBranch   string  `json:"target_branch"`
	AuthorLogin    string  `json:"author_login"`
	HeadSHA        string  `json:"head_sha"`
	MergeableState *string `json:"mergeable_state"`
	Additions      int32   `json:"additions"`
	Deletions      int32   `json:"deletions"`
	ChangedFiles   int32   `json:"changed_files"`
	CloseIntent    bool    `json:"close_intent"`
}

type CreateMergeRequestRequest struct {
	Provider     string `json:"provider"`
	ProjectPath  string `json:"project_path"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	CloseIntent  bool   `json:"close_intent"`
	RemoveSource bool   `json:"remove_source_branch"`
	Squash       bool   `json:"squash"`
}

type GitHubConnectResponse struct {
	URL        string `json:"url"`
	Configured bool   `json:"configured"`
}

func githubInstallationToResponse(i db.GithubInstallation) GitHubInstallationResponse {
	instID := i.InstallationID
	return GitHubInstallationResponse{
		ID:               uuidToString(i.ID),
		WorkspaceID:      uuidToString(i.WorkspaceID),
		InstallationID:   &instID,
		AccountLogin:     i.AccountLogin,
		AccountType:      i.AccountType,
		AccountAvatarURL: textToPtr(i.AccountAvatarUrl),
		CreatedAt:        timestampToString(i.CreatedAt),
	}
}

// githubInstallationToBroadcast returns the same shape as the list endpoint's
// per-role response with the numeric `installation_id` stripped. Realtime
// events fan out to every WS client subscribed to the workspace, so the
// payload must match the weakest-role view — admin/owner clients re-query
// the list endpoint to recover the management handle. The frontend uses
// these events only to invalidate the installations query, so it does not
// read `installation_id` off the broadcast.
func githubInstallationToBroadcast(i db.GithubInstallation) GitHubInstallationResponse {
	resp := githubInstallationToResponse(i)
	resp.InstallationID = nil
	return resp
}

func githubPullRequestToResponse(p db.GithubPullRequest) GitHubPullRequestResponse {
	repoOwner, repoName := normalizePullRequestRepositoryFields(p.RepoOwner, p.RepoName, p.HtmlUrl)
	return GitHubPullRequestResponse{
		ID:              uuidToString(p.ID),
		WorkspaceID:     uuidToString(p.WorkspaceID),
		RepoOwner:       repoOwner,
		RepoName:        repoName,
		Number:          p.PrNumber,
		Title:           p.Title,
		State:           p.State,
		HtmlURL:         normalizeGongfengPullRequestURL(p.HtmlUrl, repoOwner, repoName, p.PrNumber),
		Branch:          textToPtr(p.Branch),
		AuthorLogin:     textToPtr(p.AuthorLogin),
		AuthorAvatarURL: textToPtr(p.AuthorAvatarUrl),
		MergedAt:        timestampToPtr(p.MergedAt),
		ClosedAt:        timestampToPtr(p.ClosedAt),
		PRCreatedAt:     timestampToString(p.PrCreatedAt),
		PRUpdatedAt:     timestampToString(p.PrUpdatedAt),
		MergeableState:  textToPtr(p.MergeableState),
		// A bare PR row has no aggregated check counts — webhook
		// broadcasts of a single PR fall through here and the frontend
		// re-queries the list for fresh counts.
		ChecksConclusion: nil,
		Additions:        p.Additions,
		Deletions:        p.Deletions,
		ChangedFiles:     p.ChangedFiles,
	}
}

func issuePullRequestRowToResponse(p db.ListPullRequestsByIssueRow) GitHubPullRequestResponse {
	repoOwner, repoName := normalizePullRequestRepositoryFields(p.RepoOwner, p.RepoName, p.HtmlUrl)
	return GitHubPullRequestResponse{
		ID:               uuidToString(p.ID),
		WorkspaceID:      uuidToString(p.WorkspaceID),
		RepoOwner:        repoOwner,
		RepoName:         repoName,
		Number:           p.PrNumber,
		Title:            p.Title,
		State:            p.State,
		HtmlURL:          normalizeGongfengPullRequestURL(p.HtmlUrl, repoOwner, repoName, p.PrNumber),
		Branch:           textToPtr(p.Branch),
		AuthorLogin:      textToPtr(p.AuthorLogin),
		AuthorAvatarURL:  textToPtr(p.AuthorAvatarUrl),
		MergedAt:         timestampToPtr(p.MergedAt),
		ClosedAt:         timestampToPtr(p.ClosedAt),
		PRCreatedAt:      timestampToString(p.PrCreatedAt),
		PRUpdatedAt:      timestampToString(p.PrUpdatedAt),
		MergeableState:   textToPtr(p.MergeableState),
		ChecksConclusion: aggregateChecksConclusion(p.ChecksFailed, p.ChecksPassed, p.ChecksPending, p.ChecksTotal),
		ChecksPassed:     p.ChecksPassed,
		ChecksFailed:     p.ChecksFailed,
		ChecksPending:    p.ChecksPending,
		Additions:        p.Additions,
		Deletions:        p.Deletions,
		ChangedFiles:     p.ChangedFiles,
	}
}

// aggregateChecksConclusion collapses the per-PR check_suite counts into a
// single status surfaced to the UI:
//   - any failed-class suite wins ("failed");
//   - any not-yet-completed suite makes the PR "pending";
//   - all completed and in the passed-class is "passed";
//   - no observed suite at all is nil (rendered as "no checks" / hidden).
func aggregateChecksConclusion(failed, passed, pending, total int64) *string {
	if total == 0 {
		return nil
	}
	var v string
	switch {
	case failed > 0:
		v = "failed"
	case pending > 0:
		v = "pending"
	case passed > 0:
		v = "passed"
	default:
		return nil
	}
	return &v
}

// ── Connect / state token ───────────────────────────────────────────────────

// githubAppSlug returns the GitHub App slug used to build the install URL.
// Empty when the integration is not configured for this deployment.
func githubAppSlug() string { return strings.TrimSpace(os.Getenv("GITHUB_APP_SLUG")) }

// githubWebhookSecret is shared by webhook verification and state-token signing.
// We reuse the webhook secret as the state HMAC key so operators only need to
// configure one value.
func githubWebhookSecret() string { return strings.TrimSpace(os.Getenv("GITHUB_WEBHOOK_SECRET")) }

// isGitHubConfigured returns true only when BOTH the install slug and the
// webhook secret are set. The Connect button uses this single flag, so the
// frontend never offers a flow that the backend would reject.
func isGitHubConfigured() bool { return githubAppSlug() != "" && githubWebhookSecret() != "" }

// signState produces an opaque token that binds a workspace ID to the
// install flow so the setup callback can recover the workspace without
// trusting query params alone. Format: "<workspaceID>.<nonce>.<sigHex>".
func signState(workspaceID string) (string, error) {
	secret := githubWebhookSecret()
	if secret == "" {
		return "", errors.New("github integration is not configured")
	}
	nonceBytes := make([]byte, 12)
	if _, err := rand.Read(nonceBytes); err != nil {
		return "", err
	}
	nonce := hex.EncodeToString(nonceBytes)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(workspaceID))
	mac.Write([]byte("."))
	mac.Write([]byte(nonce))
	sig := hex.EncodeToString(mac.Sum(nil))
	return workspaceID + "." + nonce + "." + sig, nil
}

func verifyState(token string) (string, bool) {
	secret := githubWebhookSecret()
	if secret == "" {
		return "", false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", false
	}
	workspaceID, nonce, sig := parts[0], parts[1], parts[2]
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(workspaceID))
	mac.Write([]byte("."))
	mac.Write([]byte(nonce))
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(sig)) {
		return "", false
	}
	return workspaceID, true
}

// GitHubConnect (GET /api/workspaces/{id}/github/connect) returns the URL the
// browser should open to install the Multica GitHub App against the caller's
// repos. The state token binds the resulting setup callback to this workspace.
func (h *Handler) GitHubConnect(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	if _, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id"); !ok {
		return
	}
	if !isGitHubConfigured() {
		writeJSON(w, http.StatusOK, GitHubConnectResponse{Configured: false})
		return
	}
	slug := githubAppSlug()
	state, err := signState(workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to sign state")
		return
	}
	installURL := fmt.Sprintf(
		"https://github.com/apps/%s/installations/new?state=%s",
		url.PathEscape(slug),
		url.QueryEscape(state),
	)
	writeJSON(w, http.StatusOK, GitHubConnectResponse{URL: installURL, Configured: true})
}

// GitHubSetupCallback (GET /api/github/setup) handles the redirect GitHub
// sends after a user installs (or re-authorizes) the App. We expect
// ?installation_id=<id>&state=<signed token>. We persist the installation
// row (workspace ↔ installation_id mapping), then bounce the user back to
// the new Settings → GitHub tab in the web app (RFC MUL-2414 §4.1). The
// previous destination was the catch-all Settings page, which after the
// GitHub-tab split would land users on the default profile tab instead of
// the place that shows the connection they just completed.
func (h *Handler) GitHubSetupCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	installationIDStr := q.Get("installation_id")
	state := q.Get("state")
	frontend := strings.TrimSpace(os.Getenv("FRONTEND_ORIGIN"))
	if frontend == "" {
		frontend = "http://localhost:3000"
	}
	settingsURL := strings.TrimRight(frontend, "/") + "/settings?tab=github"

	if installationIDStr == "" || state == "" {
		http.Redirect(w, r, settingsURL+"&github_error=missing_params", http.StatusFound)
		return
	}
	workspaceID, ok := verifyState(state)
	if !ok {
		http.Redirect(w, r, settingsURL+"&github_error=invalid_state", http.StatusFound)
		return
	}
	installationID, err := strconv.ParseInt(installationIDStr, 10, 64)
	if err != nil {
		http.Redirect(w, r, settingsURL+"&github_error=bad_installation_id", http.StatusFound)
		return
	}
	wsUUID, err := parseStrictUUID(workspaceID)
	if err != nil {
		http.Redirect(w, r, settingsURL+"&github_error=bad_workspace", http.StatusFound)
		return
	}
	// Resolve the installation against GitHub's API to capture display info.
	// If the App auth is not configured we still create the row with the
	// minimum we know; webhook events will refresh it as soon as one fires.
	login, accountType, avatar := fetchInstallationAccount(r.Context(), installationID)

	// Best-effort capture of the connecting user (may be nil if the public
	// callback was hit without a session — e.g. user wasn't logged in to
	// Multica when they finished the GitHub install). Either way we save
	// the row so the workspace owner sees the connection on next reload.
	connectedBy := pgtype.UUID{}
	if userID := requestUserID(r); userID != "" {
		if u, err := parseStrictUUID(userID); err == nil {
			connectedBy = u
		}
	}

	inst, err := h.Queries.CreateGitHubInstallation(r.Context(), db.CreateGitHubInstallationParams{
		WorkspaceID:      wsUUID,
		InstallationID:   installationID,
		AccountLogin:     login,
		AccountType:      accountType,
		AccountAvatarUrl: ptrToText(avatar),
		ConnectedByID:    connectedBy,
	})
	if err != nil {
		slog.Error("github: failed to persist installation", "err", err, "installation_id", installationID)
		http.Redirect(w, r, settingsURL+"&github_error=persist_failed", http.StatusFound)
		return
	}
	inst, err = h.consumePendingGitHubInstallation(r.Context(), inst)
	if err != nil {
		slog.Error("github: failed to apply pending installation metadata", "err", err, "installation_id", installationID)
		http.Redirect(w, r, settingsURL+"&github_error=persist_failed", http.StatusFound)
		return
	}
	h.publish(protocol.EventGitHubInstallationCreated, workspaceID, "system", "", map[string]any{
		"installation": githubInstallationToBroadcast(inst),
	})
	http.Redirect(w, r, settingsURL+"&github_connected=1", http.StatusFound)
}

func (h *Handler) consumePendingGitHubInstallation(ctx context.Context, inst db.GithubInstallation) (db.GithubInstallation, error) {
	pending, err := h.Queries.GetPendingGitHubInstallation(ctx, inst.InstallationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return inst, nil
		}
		return inst, err
	}
	refreshed, err := h.Queries.CreateGitHubInstallation(ctx, db.CreateGitHubInstallationParams{
		WorkspaceID:      inst.WorkspaceID,
		InstallationID:   inst.InstallationID,
		AccountLogin:     pending.AccountLogin,
		AccountType:      coalesce(pending.AccountType, "User"),
		AccountAvatarUrl: pending.AccountAvatarUrl,
		ConnectedByID:    inst.ConnectedByID,
	})
	if err != nil {
		return inst, err
	}
	if err := h.Queries.DeletePendingGitHubInstallation(ctx, inst.InstallationID); err != nil {
		return inst, err
	}
	return refreshed, nil
}

// fetchInstallationAccount tries to enrich the installation row with the
// account name + avatar from GitHub.
//
// GitHub's `GET /app/installations/{id}` endpoint requires GitHub App
// authentication (a JWT signed with the App's RSA private key). When the
// operator has configured GITHUB_APP_ID and GITHUB_APP_PRIVATE_KEY, we
// sign a short-lived JWT and use it; on any failure (env not set, key
// malformed, GitHub returns non-200) we fall back to the "unknown"
// placeholder. The next `installation` webhook delivery from GitHub will
// upsert the row with the real account info — see handleInstallationEvent.
//
// The HTTP call is synchronous (no independent timeout — that's a pre-
// existing wart of the install path), but we deliberately do NOT let a
// failure abort the setup callback: a network blip here just leaves the
// "unknown" placeholder in place, and the frontend re-queries on the
// realtime broadcast emitted by the webhook handler, so the UI converges
// without a manual refresh.
func fetchInstallationAccount(ctx context.Context, installationID int64) (login, accountType string, avatar *string) {
	login = "unknown"
	accountType = "User"
	avatar = nil
	endpoint := fmt.Sprintf("%s/app/installations/%d", strings.TrimRight(githubAPIBase, "/"), installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if token, err := signGitHubAppJWT(time.Now()); err != nil {
		// Misconfigured private key is operator-actionable — log so the
		// install path doesn't silently fall back to "unknown" forever
		// without leaving a breadcrumb.
		slog.Warn("github: sign App JWT failed", "err", err)
	} else if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	var body struct {
		Account struct {
			Login     string `json:"login"`
			Type      string `json:"type"`
			AvatarURL string `json:"avatar_url"`
		} `json:"account"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return
	}
	if body.Account.Login != "" {
		login = body.Account.Login
	}
	if body.Account.Type != "" {
		accountType = body.Account.Type
	}
	if body.Account.AvatarURL != "" {
		v := body.Account.AvatarURL
		avatar = &v
	}
	return
}

// signGitHubAppJWT mints the short-lived RS256 JWT GitHub requires for
// App-authenticated REST calls (see fetchInstallationAccount). Returns
// ("", nil) when the operator hasn't configured the App identity — that's
// a soft "App auth not available" signal, not an error, so callers can
// fall through to their unauthenticated path. A malformed
// GITHUB_APP_PRIVATE_KEY surfaces as an error so the operator notices.
//
// `now` is injected for deterministic tests; production callers pass
// time.Now().
func signGitHubAppJWT(now time.Time) (string, error) {
	appID := strings.TrimSpace(os.Getenv("GITHUB_APP_ID"))
	pemKey := strings.TrimSpace(os.Getenv("GITHUB_APP_PRIVATE_KEY"))
	if appID == "" || pemKey == "" {
		return "", nil
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(pemKey))
	if err != nil {
		return "", fmt.Errorf("parse GITHUB_APP_PRIVATE_KEY: %w", err)
	}
	// GitHub allows JWTs valid for up to 10 minutes. We back-date `iat`
	// by 60 seconds to absorb modest clock skew between us and GitHub
	// (otherwise an "iat in the future" verdict from GitHub fails the
	// request) and cap `exp` at 9 minutes ahead to stay inside the cap
	// even with the same skew applied.
	claims := jwt.MapClaims{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": appID,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := token.SignedString(key)
	if err != nil {
		return "", fmt.Errorf("sign App JWT: %w", err)
	}
	return signed, nil
}

// ── Listing / disconnect ────────────────────────────────────────────────────

// ListGitHubInstallations returns the workspace's connected GitHub
// installations to any workspace member. Connect/disconnect remain
// admin-only at the router level, so the response carries a `can_manage`
// hint and strips the numeric `installation_id` for non-admin callers —
// they get visibility into "is GitHub wired up, and by whom?" without the
// management handle.
func (h *Handler) ListGitHubInstallations(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	member, _ := middleware.MemberFromContext(r.Context())
	canManage := roleAllowed(member.Role, "owner", "admin")

	rows, err := h.Queries.ListGitHubInstallationsByWorkspace(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list installations")
		return
	}
	out := make([]GitHubInstallationResponse, 0, len(rows))
	for _, row := range rows {
		resp := githubInstallationToResponse(row)
		if !canManage {
			resp.InstallationID = nil
		}
		out = append(out, resp)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"installations": out,
		"configured":    isGitHubConfigured(),
		"can_manage":    canManage,
	})
}

func (h *Handler) DeleteGitHubInstallation(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	id := chi.URLParam(r, "installationId")
	idUUID, ok := parseUUIDOrBadRequest(w, id, "installation id")
	if !ok {
		return
	}
	if err := h.Queries.DeleteGitHubInstallation(r.Context(), db.DeleteGitHubInstallationParams{
		ID:          idUUID,
		WorkspaceID: wsUUID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remove installation")
		return
	}
	h.publish(protocol.EventGitHubInstallationDeleted, workspaceID, "system", "", map[string]any{
		"id": id,
	})
	w.WriteHeader(http.StatusNoContent)
}

// ── List PRs for an issue ───────────────────────────────────────────────────

func (h *Handler) ListPullRequestsForIssue(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	rows, err := h.Queries.ListPullRequestsByIssue(r.Context(), issue.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list pull requests")
		return
	}
	out := make([]GitHubPullRequestResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, issuePullRequestRowToResponse(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"pull_requests": out})
}

func (h *Handler) LinkPullRequestToIssue(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req LinkPullRequestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	normalized, ok := normalizeIssuePullRequestLinkRequest(w, req)
	if !ok {
		return
	}
	repoOwner, repoName := splitRepositoryPath(normalized.ProjectPath)
	now := time.Now().UTC()
	pr, err := h.Queries.UpsertGitHubPullRequest(r.Context(), db.UpsertGitHubPullRequestParams{
		WorkspaceID:         issue.WorkspaceID,
		InstallationID:      0,
		RepoOwner:           repoOwner,
		RepoName:            repoName,
		PrNumber:            normalized.Number,
		Title:               normalized.Title,
		State:               normalized.State,
		HtmlUrl:             normalized.HtmlURL,
		PrCreatedAt:         pgtype.Timestamptz{Time: now, Valid: true},
		PrUpdatedAt:         pgtype.Timestamptz{Time: now, Valid: true},
		HeadSha:             normalized.HeadSHA,
		Additions:           normalized.Additions,
		Deletions:           normalized.Deletions,
		ChangedFiles:        normalized.ChangedFiles,
		Branch:              textFromNonEmpty(normalized.SourceBranch),
		AuthorLogin:         textFromNonEmpty(normalized.AuthorLogin),
		AuthorAvatarUrl:     pgtype.Text{},
		MergedAt:            pgtype.Timestamptz{},
		ClosedAt:            pgtype.Timestamptz{},
		MergeableState:      textFromOptional(normalized.MergeableState),
		ClearMergeableState: pgtype.Bool{},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record pull request")
		return
	}
	if err := h.Queries.LinkIssueToPullRequest(r.Context(), db.LinkIssueToPullRequestParams{
		IssueID:             issue.ID,
		PullRequestID:       pr.ID,
		CloseIntent:         normalized.CloseIntent,
		LinkedByType:        pgtype.Text{String: "member", Valid: true},
		LinkedByID:          parseUUID(userID),
		PreserveCloseIntent: false,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to link pull request")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"pull_request": githubPullRequestToResponse(pr),
	})
}

func (h *Handler) CreateMergeRequestForIssue(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req CreateMergeRequestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	normalized, ok := normalizeCreateMergeRequestRequest(w, req)
	if !ok {
		return
	}
	profile, hasProfile, err := h.loadUsableGongfengCredentialProfile(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load gongfeng credential profile")
		return
	}
	if !hasProfile {
		writeError(w, http.StatusBadRequest, "gongfeng credential is required to create merge request")
		return
	}
	token := h.resolveExternalCredentialToken(profile)
	if strings.TrimSpace(token) == "" {
		writeError(w, http.StatusBadRequest, "gongfeng credential token is unavailable")
		return
	}
	mr, err := createGongfengMergeRequest(r.Context(), token, normalized)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	number := mr.Number()
	htmlURL := mr.URL()
	if number <= 0 {
		writeError(w, http.StatusBadGateway, "gongfeng merge request response missing iid")
		return
	}
	if strings.TrimSpace(htmlURL) == "" {
		htmlURL = fmt.Sprintf("https://git.code.tencent.com/%s/merge_requests/%d", strings.Trim(normalized.ProjectPath, "/"), number)
	}
	repoOwner, repoName := splitRepositoryPath(normalized.ProjectPath)
	now := time.Now().UTC()
	pr, err := h.Queries.UpsertGitHubPullRequest(r.Context(), db.UpsertGitHubPullRequestParams{
		WorkspaceID:         issue.WorkspaceID,
		InstallationID:      0,
		RepoOwner:           repoOwner,
		RepoName:            repoName,
		PrNumber:            number,
		Title:               firstNonEmpty(mr.Title, normalized.Title),
		State:               normalizeGongfengMergeRequestState(mr.State),
		HtmlUrl:             htmlURL,
		Branch:              textFromNonEmpty(firstNonEmpty(mr.SourceBranch, normalized.SourceBranch)),
		AuthorLogin:         textFromNonEmpty(firstNonEmpty(mr.Author.Username, mr.Author.Name)),
		AuthorAvatarUrl:     textFromNonEmpty(mr.Author.AvatarURL),
		MergedAt:            pgtype.Timestamptz{},
		ClosedAt:            pgtype.Timestamptz{},
		PrCreatedAt:         pgtype.Timestamptz{Time: now, Valid: true},
		PrUpdatedAt:         pgtype.Timestamptz{Time: now, Valid: true},
		HeadSha:             "",
		MergeableState:      pgtype.Text{},
		ClearMergeableState: pgtype.Bool{},
		Additions:           0,
		Deletions:           0,
		ChangedFiles:        0,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record merge request")
		return
	}
	if err := h.Queries.LinkIssueToPullRequest(r.Context(), db.LinkIssueToPullRequestParams{
		IssueID:             issue.ID,
		PullRequestID:       pr.ID,
		CloseIntent:         normalized.CloseIntent,
		LinkedByType:        pgtype.Text{String: "member", Valid: true},
		LinkedByID:          parseUUID(userID),
		PreserveCloseIntent: false,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to link merge request")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"pull_request": githubPullRequestToResponse(pr),
		"linked":       true,
		"merge_request": map[string]any{
			"iid":           number,
			"url":           htmlURL,
			"source_branch": firstNonEmpty(mr.SourceBranch, normalized.SourceBranch),
			"target_branch": firstNonEmpty(mr.TargetBranch, normalized.TargetBranch),
		},
	})
}

func normalizeIssuePullRequestLinkRequest(w http.ResponseWriter, req LinkPullRequestRequest) (LinkPullRequestRequest, bool) {
	req.Provider = strings.ToLower(strings.TrimSpace(req.Provider))
	if req.Provider == "" {
		req.Provider = "gongfeng"
	}
	if req.Provider != "gongfeng" && req.Provider != "github" {
		writeError(w, http.StatusBadRequest, "provider must be gongfeng or github")
		return req, false
	}
	req.ProjectPath = strings.Trim(strings.TrimSpace(req.ProjectPath), "/")
	req.RepoURL = strings.TrimSpace(req.RepoURL)
	req.HtmlURL = strings.TrimSpace(req.HtmlURL)
	if req.ProjectPath == "" {
		req.ProjectPath = gongfengProjectPathFromURL(firstNonEmpty(req.HtmlURL, req.RepoURL))
	}
	if req.ProjectPath == "" {
		writeError(w, http.StatusBadRequest, "project_path is required")
		return req, false
	}
	if req.HtmlURL == "" {
		writeError(w, http.StatusBadRequest, "html_url is required")
		return req, false
	}
	if req.Number == 0 {
		req.Number = req.IID
	}
	if req.Number <= 0 {
		req.Number = int32(gongfengMergeRequestIIDFromURL(req.HtmlURL))
	}
	if req.Number <= 0 {
		writeError(w, http.StatusBadRequest, "number or iid is required")
		return req, false
	}
	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" {
		req.Title = fmt.Sprintf("MR !%d", req.Number)
	}
	if req.Provider == "gongfeng" {
		req.HtmlURL = normalizeGongfengPullRequestURL(req.HtmlURL, "", req.ProjectPath, req.Number)
	}
	req.State = normalizePullRequestState(req.State)
	req.SourceBranch = strings.TrimSpace(req.SourceBranch)
	req.TargetBranch = strings.TrimSpace(req.TargetBranch)
	req.AuthorLogin = strings.TrimSpace(req.AuthorLogin)
	req.HeadSHA = strings.TrimSpace(req.HeadSHA)
	for label, value := range map[string]string{
		"project_path":  req.ProjectPath,
		"html_url":      req.HtmlURL,
		"title":         req.Title,
		"source_branch": req.SourceBranch,
		"target_branch": req.TargetBranch,
		"author_login":  req.AuthorLogin,
		"head_sha":      req.HeadSHA,
	} {
		if len(value) > 2048 {
			writeError(w, http.StatusBadRequest, label+" is too long")
			return req, false
		}
	}
	return req, true
}

func normalizeGongfengPullRequestURL(rawURL, repoOwner, repoName string, number int32) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	projectPath := strings.Trim(strings.Trim(repoOwner, "/")+"/"+strings.Trim(repoName, "/"), "/")
	if strings.Contains(repoName, "/") || repoOwner == "" {
		projectPath = strings.Trim(repoName, "/")
	}
	if parsedProjectPath := gongfengProjectPathFromURL(rawURL); parsedProjectPath != "" {
		projectPath = parsedProjectPath
	}
	if number <= 0 {
		number = int32(gongfengMergeRequestIIDFromURL(rawURL))
	}
	if projectPath == "" || number <= 0 || !strings.Contains(strings.ToLower(rawURL), "git.code.tencent.com") {
		return rawURL
	}
	return fmt.Sprintf("https://git.code.tencent.com/%s/merge_requests/%d", strings.Trim(projectPath, "/"), number)
}

func normalizePullRequestRepositoryFields(repoOwner, repoName, htmlURL string) (string, string) {
	repoOwner = strings.Trim(strings.TrimSpace(repoOwner), "/")
	repoName = strings.Trim(strings.TrimSpace(repoName), "/")
	projectPath := gongfengProjectPathFromURL(htmlURL)
	if projectPath == "" && (repoName == "" || repoName == "-" || strings.Contains(repoOwner, "/")) {
		projectPath = strings.Trim(strings.Trim(repoOwner, "/")+"/"+strings.Trim(repoName, "/"), "/")
	}
	if projectPath == "" {
		return repoOwner, repoName
	}
	parts := strings.Split(strings.Trim(projectPath, "/"), "/")
	if len(parts) < 2 {
		return repoOwner, repoName
	}
	return strings.Join(parts[:len(parts)-1], "/"), parts[len(parts)-1]
}

func normalizeCreateMergeRequestRequest(w http.ResponseWriter, req CreateMergeRequestRequest) (CreateMergeRequestRequest, bool) {
	req.Provider = strings.ToLower(strings.TrimSpace(req.Provider))
	if req.Provider == "" {
		req.Provider = "gongfeng"
	}
	if req.Provider != "gongfeng" {
		writeError(w, http.StatusBadRequest, "provider must be gongfeng")
		return req, false
	}
	req.ProjectPath = strings.Trim(strings.TrimSpace(req.ProjectPath), "/")
	req.SourceBranch = strings.TrimSpace(req.SourceBranch)
	req.TargetBranch = strings.TrimSpace(req.TargetBranch)
	req.Title = strings.TrimSpace(req.Title)
	req.Description = strings.TrimSpace(req.Description)
	for label, value := range map[string]string{
		"project_path":  req.ProjectPath,
		"source_branch": req.SourceBranch,
		"target_branch": req.TargetBranch,
		"title":         req.Title,
	} {
		if value == "" {
			writeError(w, http.StatusBadRequest, label+" is required")
			return req, false
		}
	}
	for label, value := range map[string]string{
		"project_path":  req.ProjectPath,
		"source_branch": req.SourceBranch,
		"target_branch": req.TargetBranch,
		"title":         req.Title,
		"description":   req.Description,
	} {
		if len(value) > 8192 {
			writeError(w, http.StatusBadRequest, label+" is too long")
			return req, false
		}
	}
	return req, true
}

func normalizePullRequestState(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "draft":
		return "draft"
	case "closed", "close":
		return "closed"
	case "merged", "merge":
		return "merged"
	default:
		return "open"
	}
}

func splitRepositoryPath(projectPath string) (string, string) {
	parts := strings.Split(strings.Trim(projectPath, "/"), "/")
	if len(parts) <= 1 {
		return "", projectPath
	}
	return strings.Join(parts[:len(parts)-1], "/"), parts[len(parts)-1]
}

func gongfengProjectPathFromURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	if !strings.Contains(strings.ToLower(parsed.Host), "git.code.tencent.com") {
		return ""
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	stop := len(parts)
	for i, part := range parts {
		switch part {
		case "-", "tree", "commits", "commit", "tags", "merge_requests", "blob":
			stop = i
		}
		if stop != len(parts) {
			break
		}
	}
	if stop <= 0 {
		return ""
	}
	return strings.Join(parts[:stop], "/")
}

func gongfengMergeRequestIIDFromURL(rawURL string) int {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return 0
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for i, part := range parts {
		if part == "merge_requests" && i+1 < len(parts) {
			n, _ := strconv.Atoi(parts[i+1])
			return n
		}
	}
	return 0
}

type gongfengMergeRequestResponse struct {
	ID           int64  `json:"id"`
	IID          int32  `json:"iid"`
	NumberValue  int32  `json:"number"`
	WebURL       string `json:"web_url"`
	HTMLURL      string `json:"html_url"`
	Title        string `json:"title"`
	State        string `json:"state"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
	Author       struct {
		Username  string `json:"username"`
		Name      string `json:"name"`
		AvatarURL string `json:"avatar_url"`
	} `json:"author"`
}

func (mr gongfengMergeRequestResponse) Number() int32 {
	if mr.IID > 0 {
		return mr.IID
	}
	return mr.NumberValue
}

func (mr gongfengMergeRequestResponse) URL() string {
	return firstNonEmpty(mr.WebURL, mr.HTMLURL)
}

func createGongfengMergeRequest(ctx context.Context, token string, req CreateMergeRequestRequest) (gongfengMergeRequestResponse, error) {
	projectID, err := resolveGongfengProjectAPIID(ctx, token, req.ProjectPath)
	if err != nil {
		return gongfengMergeRequestResponse{}, err
	}
	endpoint := strings.TrimRight(gongfengAPIBase(), "/") + "/projects/" + url.PathEscape(projectID) + "/merge_requests"
	payload := map[string]any{
		"source_branch": req.SourceBranch,
		"target_branch": req.TargetBranch,
		"title":         req.Title,
	}
	if req.Description != "" {
		payload["description"] = req.Description
	}
	if req.RemoveSource {
		payload["remove_source_branch"] = true
	}
	if req.Squash {
		payload["squash"] = true
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return gongfengMergeRequestResponse{}, fmt.Errorf("marshal gongfeng merge request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return gongfengMergeRequestResponse{}, fmt.Errorf("build gongfeng merge request request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("PRIVATE-TOKEN", token)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return gongfengMergeRequestResponse{}, fmt.Errorf("create gongfeng merge request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return gongfengMergeRequestResponse{}, fmt.Errorf("gongfeng create merge request returned %d: %s", resp.StatusCode, redactGongfengError(respBody))
	}
	var out gongfengMergeRequestResponse
	if len(strings.TrimSpace(string(respBody))) > 0 {
		if err := json.Unmarshal(respBody, &out); err != nil {
			return gongfengMergeRequestResponse{}, fmt.Errorf("decode gongfeng merge request response: %w", err)
		}
	}
	return out, nil
}

func resolveGongfengProjectAPIID(ctx context.Context, token, projectPath string) (string, error) {
	projectPath = strings.Trim(projectPath, "/")
	if projectPath == "" {
		return "", errors.New("gongfeng project path is required")
	}
	if _, err := strconv.ParseInt(projectPath, 10, 64); err == nil {
		return projectPath, nil
	}
	if project, ok, err := fetchGongfengProjectByID(ctx, token, url.PathEscape(projectPath)); err != nil {
		return "", err
	} else if ok {
		return strconv.FormatInt(project.ID, 10), nil
	}
	parts := strings.Split(projectPath, "/")
	query := projectPath
	if len(parts) > 0 {
		query = parts[len(parts)-1]
	}
	endpoint := strings.TrimRight(gongfengAPIBase(), "/") + "/projects?search=" + url.QueryEscape(query) + "&per_page=100"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("build gongfeng project search request: %w", err)
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("PRIVATE-TOKEN", token)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("search gongfeng project: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("gongfeng project search returned %d: %s", resp.StatusCode, redactGongfengError(body))
	}
	var projects []gongfengProjectResponse
	if err := json.Unmarshal(body, &projects); err != nil {
		return "", fmt.Errorf("decode gongfeng project search response: %w", err)
	}
	for _, project := range projects {
		if strings.EqualFold(strings.Trim(project.PathWithNamespace, "/"), projectPath) && project.ID > 0 {
			return strconv.FormatInt(project.ID, 10), nil
		}
	}
	return "", fmt.Errorf("gongfeng project %q not found by path or search", projectPath)
}

type gongfengProjectResponse struct {
	ID                int64  `json:"id"`
	PathWithNamespace string `json:"path_with_namespace"`
}

func fetchGongfengProjectByID(ctx context.Context, token, projectID string) (gongfengProjectResponse, bool, error) {
	endpoint := strings.TrimRight(gongfengAPIBase(), "/") + "/projects/" + projectID
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return gongfengProjectResponse{}, false, fmt.Errorf("build gongfeng project request: %w", err)
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("PRIVATE-TOKEN", token)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(httpReq)
	if err != nil {
		return gongfengProjectResponse{}, false, fmt.Errorf("fetch gongfeng project: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		var project gongfengProjectResponse
		if err := json.Unmarshal(body, &project); err != nil {
			return gongfengProjectResponse{}, false, fmt.Errorf("decode gongfeng project response: %w", err)
		}
		return project, project.ID > 0, nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound:
		return gongfengProjectResponse{}, false, nil
	default:
		return gongfengProjectResponse{}, false, fmt.Errorf("gongfeng project lookup returned %d: %s", resp.StatusCode, redactGongfengError(body))
	}
}

func redactGongfengError(body []byte) string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return ""
	}
	text = regexp.MustCompile(`(?i)(private[-_]?token|access[-_]?token|authorization|token)["'\s:=]+[^"',\s}]+`).ReplaceAllString(text, "$1=<redacted>")
	if len(text) > 1000 {
		text = text[:1000] + "..."
	}
	return text
}

func normalizeGongfengMergeRequestState(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "merged":
		return "merged"
	case "closed", "close":
		return "closed"
	case "draft":
		return "draft"
	default:
		return "open"
	}
}

func textFromNonEmpty(value string) pgtype.Text {
	value = strings.TrimSpace(value)
	return pgtype.Text{String: value, Valid: value != ""}
}

func textFromOptional(value *string) pgtype.Text {
	if value == nil {
		return pgtype.Text{}
	}
	return textFromNonEmpty(*value)
}

// ── Webhook ─────────────────────────────────────────────────────────────────

// HandleGitHubWebhook (POST /api/webhooks/github) is GitHub's destination for
// every event from a connected installation. We verify HMAC signature, route
// on X-GitHub-Event, and either mirror PR/check-suite rows or remove the
// installation on uninstall.
