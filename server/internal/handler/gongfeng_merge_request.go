// 职责定位：工蜂（gongfeng，腾讯自建 GitLab）合并请求（MR / Pull Request）与 issue 关联的
// HTTP 处理器。两条主路径：
//  1. LinkPullRequestToIssue —— 由外部推送/前端主动上报一条已存在的 MR，把它记录进库并挂到
//     issue 上（复用 GitHub PR 的存储表，Provider 标成 gongfeng）；
//  2. CreateMergeRequestForIssue —— 用当前用户的工蜂凭据在工蜂远端真正创建 MR，再落库关联。
//
// 第三方依赖说明：真正的工蜂 HTTP 调用都收敛在本文件的 gongfengJSONRequest 及其包装函数里，
// 出站 base 地址来自 gongfengAPIBase()（服务端配置）。recordIssuePullRequest 把「upsert PR +
// 关联 issue」包在同一个事务里，保证两条写库要么都成要么都撤。
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

// LinkPullRequestRequest 是「把一条已存在的 MR 关联到 issue」的请求体：描述 MR 的快照
// （项目、标题、状态、分支、作者、改动行数等）。Provider 缺省按 gongfeng 处理。
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

// createMergeRequestRequest 是「在工蜂远端创建 MR」的请求体（未导出：仅本项目内使用）。
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

// LinkPullRequestToIssue 把一条已存在的 MR 关联到 issue。关联成功的前提是 issue 在当前
// workspace 且调用方有权限打开它；写入后广播 EventPullRequestLinked 供前端刷新。
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

// CreateMergeRequestForIssue 在工蜂远端给 issue 建一条 MR 再关联回来。需要当前用户有可用的
// 工蜂凭据档案（profile），并解析出真实 token。远端建好后复用 PR 存储落库关联，返回响应中
// 附上 iid / url / 分支等 MR 元信息。远端异常（5xx/网络）一律回 502。
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
		// 工蜂响应里没给 URL 时拼一个标准的 git.code.tencent.com 地址兜底。
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

// recordIssuePullRequest 在同一个事务里完成「upsert PR 记录 + 关联到 issue」两步。
// 工蜂 MR 复用了 GitHub PR 的表（所以参数类型叫 UpsertGitHubPullRequestParams），
// 通过 InstallationID=0 与真实 GitHub 安装区分。
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

// normalizeIssuePullRequestLinkRequest 对「关联已存在 MR」的请求做规范化与宽松校验：
// provider 大小写归一（缺省 gongfeng）、project_path/html_url 去空白；字段缺失时尽量从
// 别的字段推导（project_path 可从 html_url/repo_url 反推，number 可从 iid 或 URL 反推，
// title 缺省成 "MR !N"）。工蜂的 html_url 会被重写成规范的 git.code.tencent.com 地址。
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

// normalizeGongfengPullRequestURL 尽力把任意形态的工蜂 MR 链接整理成标准形态
// https://git.code.tencent.com/{project_path}/merge_requests/{number}。识别不出项目路径或
// 编号、或链接根本不是 git.code.tencent.com 域时，原样返回不动。
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

// normalizeCreateMergeRequestRequest 对「创建 MR」的请求做规范化与必填/长度校验。目前只支持
// gongfeng 一个 provider。
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

// normalizePullRequestState 把各种写法统一成内部状态：draft / closed / merged / open（其余
// 一律 open）。
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

// splitRepositoryPath 把 "group/subgroup/repo" 拆成 owner 段（前 N-1 节）与仓库名（最后一节）；
// 只有一段时视为没有 owner。
func splitRepositoryPath(projectPath string) (string, string) {
	parts := strings.Split(strings.Trim(projectPath, "/"), "/")
	if len(parts) <= 1 {
		return "", projectPath
	}
	return strings.Join(parts[:len(parts)-1], "/"), parts[len(parts)-1]
}

// gongfengProjectPathFromURL 从工蜂网页 URL 里抽出项目路径（如 group/repo）。遇到
// tree/commit/merge_requests 等路径段就截断；不是 git.code.tencent.com 域返回空串。
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

// gongfengMergeRequestIIDFromURL 从工蜂 MR 链接里抽出 MR 编号（merge_requests 段后一位）；
// 解析不出返回 0。
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

// gongfengMergeRequestResponse 是工蜂 MR 相关接口返回的 JSON 形态。工蜂不同接口给编号时
// 字段名不一致（iid / number），Number() 负责统一取。
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

// Number 返回 MR 编号，优先取 iid（GitLab 系习惯），再退回 number。
func (mr gongfengMergeRequestResponse) Number() int32 {
	if mr.IID > 0 {
		return mr.IID
	}
	return mr.NumberValue
}

// URL 返回 MR 页面地址，web_url 与 html_url 取先到者。
func (mr gongfengMergeRequestResponse) URL() string {
	return firstNonEmpty(mr.WebURL, mr.HTMLURL)
}

// gongfengJSONRequest 是发给工蜂的出站 HTTP 统一入口：JSON 编码、带 PRIVATE-TOKEN 头、
// 30s 超时，响应体最多读 1MB。返回状态码与原始 body（上层负责解码与错误归类）。
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

// createGongfengMergeRequestForProject 向工蜂 projects/{id}/merge_requests POST 建一条 MR。
// 失败时把状态码与脱敏后的响应体包进错误（token 绝不外泄）。
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

// ensureGongfengMergeRequest 在创建前后都做一次「对账」，保证调用是幂等的。
//
// 背景：工蜂 API 不接受调用方传的幂等键，所以一次「创建超时」既可能是「根本没建上」，也
// 可能是「建上了但响应丢了」。按仓库 + 分支对（source→target）找当前 open 的 MR，就能让
// 重试时把远端那次已成功的创建「认回来」，而不是再建一条重复的 MR。
//
// 顺序：先 preflight 查一次（避免每次重复建）→ 创建 → 若创建成功且带回编号就直接返回；
// 否则创建后再 recovery 查一次（覆盖超时丢响应的情况）。两边都失败才把错误原样上抛。
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

// findOpenGongfengMergeRequest 按 (source_branch, target_branch) 在项目里找处于 opened 的 MR，
// 用于幂等对账。
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

// resolveGongfengProjectAPIID 把「人类可读的项目路径」（group/repo）换算成工蜂 API 用的项目
// ID。已传数字就直接当 ID；否则先按 ID 拉一次项目、再退化为 search 按路径匹配。
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

// gongfengProjectResponse 是工蜂项目查询返回的最小形态。
type gongfengProjectResponse struct {
	ID                int64  `json:"id"`
	PathWithNamespace string `json:"path_with_namespace"`
}

// fetchGongfengProjectByID 按 ID 拉取项目；401/403/404 视为「没拿到」（not found），其余
// 非 2xx 才是真错误。用于解析「传入的 project_path 其实是个数字 ID」的快捷路径。
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

// redactGongfengError 把工蜂错误响应里的 token/凭据等敏感字段替换成 <redacted>，并截断到
// 1000 字符，防止错误信息把密钥带进日志或响应。
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

// normalizeGongfengMergeRequestState 把工蜂侧的状态统一成内部枚举：merged / closed / draft /
// open（默认 open）。
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

// textFromNonEmpty 把去掉首尾空白的字符串包成 pgtype.Text：空串给无效值（NULL）。
func textFromNonEmpty(value string) pgtype.Text {
	value = strings.TrimSpace(value)
	return pgtype.Text{String: value, Valid: value != ""}
}

// textFromOptional 把指针字符串转 pgtype.Text；nil 指针 → 无效值（NULL）。
func textFromOptional(value *string) pgtype.Text {
	if value == nil {
		return pgtype.Text{}
	}
	return textFromNonEmpty(*value)
}
