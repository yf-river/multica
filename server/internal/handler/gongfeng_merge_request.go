package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

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

type createMergeRequestRequest struct {
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
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	normalized, ok := normalizeIssuePullRequestLinkRequest(w, req)
	if !ok {
		return
	}
	repoOwner, repoName := splitRepositoryPath(normalized.ProjectPath)
	now := time.Now().UTC()
	pr, err := h.recordIssuePullRequest(r.Context(), db.UpsertGitHubPullRequestParams{
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
	}, db.LinkIssueToPullRequestParams{
		IssueID:             issue.ID,
		CloseIntent:         normalized.CloseIntent,
		LinkedByType:        pgtype.Text{String: "member", Valid: true},
		LinkedByID:          parseUUID(userID),
		PreserveCloseIntent: false,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to link pull request")
		return
	}
	pullRequestResponse := githubPullRequestToResponse(pr, false)
	pullRequestResponse.Provider = "gongfeng"
	h.publish(protocol.EventPullRequestLinked, uuidToString(issue.WorkspaceID), "member", userID, map[string]any{
		"issue_id":     uuidToString(issue.ID),
		"pull_request": pullRequestResponse,
	})
	writeJSON(w, http.StatusCreated, map[string]any{
		"pull_request": pullRequestResponse,
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
	var req createMergeRequestRequest
	if !decodeRequiredJSON(w, r, &req) {
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
	token, err := h.resolveExternalCredentialToken(profile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve gongfeng credential token")
		return
	}
	if strings.TrimSpace(token) == "" {
		writeError(w, http.StatusBadRequest, "gongfeng credential token is unavailable")
		return
	}
	mr, err := ensureGongfengMergeRequest(r.Context(), token, normalized)
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
	pr, err := h.recordIssuePullRequest(r.Context(), db.UpsertGitHubPullRequestParams{
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
	}, db.LinkIssueToPullRequestParams{
		IssueID:             issue.ID,
		CloseIntent:         normalized.CloseIntent,
		LinkedByType:        pgtype.Text{String: "member", Valid: true},
		LinkedByID:          parseUUID(userID),
		PreserveCloseIntent: false,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to link merge request")
		return
	}
	pullRequestResponse := githubPullRequestToResponse(pr, false)
	pullRequestResponse.Provider = "gongfeng"
	h.publish(protocol.EventPullRequestLinked, uuidToString(issue.WorkspaceID), "member", userID, map[string]any{
		"issue_id":     uuidToString(issue.ID),
		"pull_request": pullRequestResponse,
	})
	writeJSON(w, http.StatusCreated, map[string]any{
		"pull_request": pullRequestResponse,
		"linked":       true,
		"merge_request": map[string]any{
			"iid":           number,
			"url":           htmlURL,
			"source_branch": firstNonEmpty(mr.SourceBranch, normalized.SourceBranch),
			"target_branch": firstNonEmpty(mr.TargetBranch, normalized.TargetBranch),
		},
	})
}

func (h *Handler) recordIssuePullRequest(
	ctx context.Context,
	pullRequest db.UpsertGitHubPullRequestParams,
	link db.LinkIssueToPullRequestParams,
) (db.GithubPullRequest, error) {
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return db.GithubPullRequest{}, fmt.Errorf("begin pull request link transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	queries := h.Queries.WithTx(tx)
	created, err := queries.UpsertGitHubPullRequest(ctx, pullRequest)
	if err != nil {
		return db.GithubPullRequest{}, fmt.Errorf("upsert pull request: %w", err)
	}
	link.PullRequestID = created.ID
	if err := queries.LinkIssueToPullRequest(ctx, link); err != nil {
		return db.GithubPullRequest{}, fmt.Errorf("link issue to pull request: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return db.GithubPullRequest{}, fmt.Errorf("commit pull request link: %w", err)
	}
	return created, nil
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

func normalizeCreateMergeRequestRequest(w http.ResponseWriter, req createMergeRequestRequest) (createMergeRequestRequest, bool) {
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
	parts[stop-1] = strings.TrimSuffix(parts[stop-1], ".git")
	if parts[stop-1] == "" {
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

func gongfengJSONRequest(ctx context.Context, method, endpoint, token string, body []byte) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, payload, nil
}

func createGongfengMergeRequestForProject(ctx context.Context, token, projectID string, req createMergeRequestRequest) (gongfengMergeRequestResponse, error) {
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
	status, respBody, err := gongfengJSONRequest(ctx, http.MethodPost, endpoint, token, body)
	if err != nil {
		return gongfengMergeRequestResponse{}, fmt.Errorf("create gongfeng merge request: %w", err)
	}
	if status < 200 || status >= 300 {
		return gongfengMergeRequestResponse{}, fmt.Errorf("gongfeng create merge request returned %d: %s", status, redactGongfengError(respBody))
	}
	var out gongfengMergeRequestResponse
	if len(strings.TrimSpace(string(respBody))) > 0 {
		if err := json.Unmarshal(respBody, &out); err != nil {
			return gongfengMergeRequestResponse{}, fmt.Errorf("decode gongfeng merge request response: %w", err)
		}
	}
	return out, nil
}

// ensureGongfengMergeRequest reconciles the provider before and after create.
// The Gongfeng API does not accept a caller idempotency key, so a timeout can
// mean either "not created" or "created but the response was lost". Matching
// the current open MR by its repository and branch pair makes a retry recover
// that remote success instead of creating a second MR.
func ensureGongfengMergeRequest(ctx context.Context, token string, req createMergeRequestRequest) (gongfengMergeRequestResponse, error) {
	projectID, err := resolveGongfengProjectAPIID(ctx, token, req.ProjectPath)
	if err != nil {
		return gongfengMergeRequestResponse{}, err
	}
	existing, found, preflightErr := findOpenGongfengMergeRequest(ctx, token, projectID, req.SourceBranch, req.TargetBranch)
	if preflightErr == nil && found {
		return existing, nil
	}

	created, createErr := createGongfengMergeRequestForProject(ctx, token, projectID, req)
	if createErr == nil && created.Number() > 0 {
		if preflightErr != nil {
			slog.Warn("created Gongfeng merge request while preflight reconciliation was unavailable",
				"project_path", req.ProjectPath,
				"source_branch", req.SourceBranch,
				"target_branch", req.TargetBranch,
				"error", preflightErr,
			)
		}
		return created, nil
	}
	existing, found, recoveryErr := findOpenGongfengMergeRequest(ctx, token, projectID, req.SourceBranch, req.TargetBranch)
	if recoveryErr == nil && found {
		return existing, nil
	}
	if createErr != nil {
		if recoveryErr != nil {
			return gongfengMergeRequestResponse{}, fmt.Errorf("%w; provider reconciliation failed: %v", createErr, recoveryErr)
		}
		return gongfengMergeRequestResponse{}, createErr
	}
	if recoveryErr != nil {
		return gongfengMergeRequestResponse{}, fmt.Errorf("gongfeng create response missing iid and provider reconciliation failed: %w", recoveryErr)
	}
	return created, nil
}

func findOpenGongfengMergeRequest(
	ctx context.Context,
	token string,
	projectID string,
	sourceBranch string,
	targetBranch string,
) (gongfengMergeRequestResponse, bool, error) {
	values := url.Values{
		"state":         {"opened"},
		"source_branch": {sourceBranch},
		"target_branch": {targetBranch},
		"per_page":      {"100"},
	}
	endpoint := strings.TrimRight(gongfengAPIBase(), "/") + "/projects/" + url.PathEscape(projectID) + "/merge_requests?" + values.Encode()
	status, body, err := gongfengJSONRequest(ctx, http.MethodGet, endpoint, token, nil)
	if err != nil {
		return gongfengMergeRequestResponse{}, false, fmt.Errorf("lookup gongfeng merge request: %w", err)
	}
	if status < 200 || status >= 300 {
		return gongfengMergeRequestResponse{}, false, fmt.Errorf("gongfeng merge request lookup returned %d: %s", status, redactGongfengError(body))
	}
	var items []gongfengMergeRequestResponse
	if err := json.Unmarshal(body, &items); err != nil {
		return gongfengMergeRequestResponse{}, false, fmt.Errorf("decode gongfeng merge request lookup: %w", err)
	}
	for _, item := range items {
		if item.Number() > 0 &&
			strings.EqualFold(strings.TrimSpace(item.State), "opened") &&
			item.SourceBranch == sourceBranch && item.TargetBranch == targetBranch {
			return item, true, nil
		}
	}
	return gongfengMergeRequestResponse{}, false, nil
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
	status, body, err := gongfengJSONRequest(ctx, http.MethodGet, endpoint, token, nil)
	if err != nil {
		return "", fmt.Errorf("search gongfeng project: %w", err)
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("gongfeng project search returned %d: %s", status, redactGongfengError(body))
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
	status, body, err := gongfengJSONRequest(ctx, http.MethodGet, endpoint, token, nil)
	if err != nil {
		return gongfengProjectResponse{}, false, fmt.Errorf("fetch gongfeng project: %w", err)
	}
	switch {
	case status >= 200 && status < 300:
		var project gongfengProjectResponse
		if err := json.Unmarshal(body, &project); err != nil {
			return gongfengProjectResponse{}, false, fmt.Errorf("decode gongfeng project response: %w", err)
		}
		return project, project.ID > 0, nil
	case status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusNotFound:
		return gongfengProjectResponse{}, false, nil
	default:
		return gongfengProjectResponse{}, false, fmt.Errorf("gongfeng project lookup returned %d: %s", status, redactGongfengError(body))
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
