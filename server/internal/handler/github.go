// Package handler 是整个 HTTP API 的处理器集合。
//
// 本文件（github.go）实现 Multica 与 GitHub 的集成：通过 GitHub App 安装流程
// 连接 workspace（connect/setup callback/断开）、列出安装的仓库、mirror
// pull_request webhook 到 PR 行并自动关联 issue、按"关闭关键字"自动推进 issue
// 到 done，以及触发"权威 CI/可合并性快照"的异步刷新。同时含安装/PR/CI 事件的
// HMAC 校验与分发、安装级状态 token 签名与校验等辅助逻辑。
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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// githubAPIBase 是 GitHub REST API 的基地址。可变的，便于测试把 App 认证的请求
// 指向 httptest 服务器而不真打 GitHub。
var githubAPIBase = "https://api.github.com"

const (
	githubReturnToGitHub       = "github"
	githubReturnToRepositories = "repositories"
	githubAPIResponseLimit     = 4 << 20
)

// ── 响应结构 ────────────────────────────────────────────────────────────────

// GitHubInstallationResponse 是安装列表接口返回、以及安装相关 WS 事件广播的
// JSON 形态。
//
// InstallationID 仅管理员可见：数字版 GitHub installation_id 是
// Connect/Disconnect 流程使用的管理句柄，因此非管理员成员收到的响应省略该
// 字段。列表 handler 按角色收起它；实时广播则一律省略，因为 WS 扇出没有
// 按接收者的视图（管理员在失效后重查列表接口找回管理句柄）。
type GitHubInstallationResponse struct {
	ID               string  `json:"id"`
	WorkspaceID      string  `json:"workspace_id"`
	InstallationID   *int64  `json:"installation_id,omitempty"`
	AccountLogin     string  `json:"account_login"`
	AccountType      string  `json:"account_type"`
	AccountAvatarURL *string `json:"account_avatar_url"`
	CreatedAt        string  `json:"created_at"`
}

// GitHubPullRequestResponse 是单条 PR 在卡片/列表上的对外形态。
type GitHubPullRequestResponse struct {
	ID string `json:"id"`
	// Provider 是这条 PR 镜像自的 Git 提供方："github"、"forgejo"、"gitea" 或
	// "gitlab"。前端用它挑主机图标与文案（如 GitLab "merge request"）。
	Provider        string  `json:"provider"`
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
	// MergeableState 镜像 GitHub REST 的 mergeable_state 字段，为兼容保留。卡片
	// 现在读下面更丰富的 GraphQL 字段。
	MergeableState *string `json:"mergeable_state"`
	// ── GitHub API 快照（MUL-5265，方案 C）──────────────────────────────
	// 这些来自一次认证过的 GraphQL 查询，是唯一真源。快照未落地（或 GitHub App
	// 私钥没配）时它们全为 null / 空 / 0，卡片隐藏 CI / merge 区域并优雅降级。
	//
	// Mergeable 只回答"有没有冲突"："mergeable" | "conflicting" | "unknown" |
	// null。错误的 "conflicting" 绝不能上报成 "not mergeable"——那是
	// MergeStateStatus 的职责。
	Mergeable *string `json:"mergeable"`
	// MergeStateStatus 是 GitHub 的合并状态结论，小写："clean" | "dirty" |
	// "blocked" | "behind" | "unstable" | "draft" | "has_hooks" | "unknown" |
	// null。"可以合并"只由 "clean" 推导。
	MergeStateStatus *string `json:"merge_state_status"`
	// SnapshotAvailable 区分"当前 API 快照"与"功能未启用 / 还没抓过"和"快照在
	// 但 statusCheckRollup 为 null"。只有最后一种情况才能渲染"无检查"。非 GitHub
	// 提供方省略该字段，它们保留 webhook 派生的 checks_conclusion 兼容路径。
	SnapshotAvailable *bool `json:"snapshot_available,omitempty"`
	// ChecksRollup 是 GitHub 的总体 CI 结论，小写："success" | "failure" |
	// "pending" | "error" | "expected" | null。null 表示 statusCheckRollup 为
	// null（还没有检查），绝不能渲染成通过。
	ChecksRollup *string `json:"checks_rollup"`
	// ChecksConclusion 是由快照派生的粗略兼容别名："passed" | "failed" |
	// "pending" | null。
	ChecksConclusion *string `json:"checks_conclusion"`
	// 快照 head 上按 run 粒度的计数。ChecksPending 为仍读旧键的老客户端镜像
	// ChecksRunning。
	ChecksTotal   int64 `json:"checks_total"`
	ChecksPassed  int64 `json:"checks_passed"`
	ChecksFailed  int64 `json:"checks_failed"`
	ChecksRunning int64 `json:"checks_running"`
	ChecksPending int64 `json:"checks_pending"`
	// FailedCheckNames 列出失败的检查名，让卡片能点名（如 "✗ 2/7 · backend,
	// e2e"）。
	FailedCheckNames []string `json:"failed_check_names"`
	// SnapshotStale 为 true 表示一条 open PR 最近一次成功抓取已超过过期阈值
	// （GitHub 故障 / 密钥被吊销）：卡片把"最后已知数据"置灰展示而不是空白。
	SnapshotStale bool `json:"snapshot_stale"`
	// SnapshotFetchedAt 是快照最后抓取时间（RFC3339），或 null。
	SnapshotFetchedAt *string `json:"snapshot_fetched_at"`
	// Diff 统计（增删行数与文件数）来自 `pull_request` webhook 载荷。早于该
	// 字段的旧行默认 0；前端把 total == 0 视为"未知"并隐藏统计行。
	Additions    int32 `json:"additions"`
	Deletions    int32 `json:"deletions"`
	ChangedFiles int32 `json:"changed_files"`
}

// GitHubConnectResponse 是 Connect 接口的返回：URL 为让浏览器打开的安装地址，
// Configured 表示集成是否为本部署配置好。
type GitHubConnectResponse struct {
	URL        string `json:"url"`
	Configured bool   `json:"configured"`
}

// GitHubRepositoryResponse 是单个 GitHub 仓库的对外形态（安装可访问范围内）。
type GitHubRepositoryResponse struct {
	ID            int64   `json:"id"`
	FullName      string  `json:"full_name"`
	HTMLURL       string  `json:"html_url"`
	CloneURL      string  `json:"clone_url"`
	Description   *string `json:"description"`
	Private       bool    `json:"private"`
	Archived      bool    `json:"archived"`
	DefaultBranch string  `json:"default_branch"`
}

// GitHubRepositoriesResponse 是仓库分页列表的返回，TotalCount 为总数、NextPage
// 指示是否还有下一页。
type GitHubRepositoriesResponse struct {
	Repositories []GitHubRepositoryResponse `json:"repositories"`
	TotalCount   int64                      `json:"total_count"`
	NextPage     *int                       `json:"next_page"`
}

// githubInstallationToResponse 把安装库行转成列表接口用的响应（含管理句柄）。
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

// githubInstallationToBroadcast 返回与列表接口"按角色响应"相同的形态，只是去掉
// 数字版 installation_id。实时事件会扇出给订阅该 workspace 的每个 WS 客户端，
// 所以载荷必须匹配"最弱角色视图"——admin/owner 客户端重查列表接口找回管理句柄。
// 前端只用这些事件让 installations 查询失效，不从广播里读 installation_id。
func githubInstallationToBroadcast(i db.GithubInstallation) GitHubInstallationResponse {
	resp := githubInstallationToResponse(i)
	resp.InstallationID = nil
	return resp
}

// githubPullRequestToResponse 把 PR 库行转成对外响应（bare 行：无快照、无聚合
// 检查计数）。
func githubPullRequestToResponse(p db.GithubPullRequest, snapshotEnabled bool) GitHubPullRequestResponse {
	snapshotAvailable := currentGitHubSnapshotAvailable(
		snapshotEnabled, p.HeadSha, p.SnapshotHeadSha, p.SnapshotFetchedAt,
	)
	return GitHubPullRequestResponse{
		ID:                uuidToString(p.ID),
		Provider:          "github",
		WorkspaceID:       uuidToString(p.WorkspaceID),
		RepoOwner:         p.RepoOwner,
		RepoName:          p.RepoName,
		Number:            p.PrNumber,
		Title:             p.Title,
		State:             p.State,
		HtmlURL:           p.HtmlUrl,
		Branch:            textToPtr(p.Branch),
		AuthorLogin:       textToPtr(p.AuthorLogin),
		AuthorAvatarURL:   textToPtr(p.AuthorAvatarUrl),
		MergedAt:          timestampToPtr(p.MergedAt),
		ClosedAt:          timestampToPtr(p.ClosedAt),
		PRCreatedAt:       timestampToString(p.PrCreatedAt),
		PRUpdatedAt:       timestampToString(p.PrUpdatedAt),
		MergeableState:    textToPtr(p.MergeableState),
		SnapshotAvailable: &snapshotAvailable,
		// 一条裸 PR 行没有聚合的检查计数——单条 PR 的 webhook 广播落在这里，前端
		// 会重查列表拿完整快照（mergeable / rollup / 计数）。
		ChecksConclusion: nil,
		FailedCheckNames: []string{},
		Additions:        p.Additions,
		Deletions:        p.Deletions,
		ChangedFiles:     p.ChangedFiles,
	}
}

// prSnapshotStaleThreshold 是 open PR 最近一次成功抓取能老多久的阈值，超过后
// 卡片把它置灰成过期。健康的流水线至少每隔一轮清扫（约 10 分钟）刷新 open PR，
// 所以越过 30 分钟意味着刷新没在落地（GitHub 故障、密钥被吊销），展示的数据是
// "最后已知"。
const prSnapshotStaleThreshold = 30 * time.Minute

// issuePullRequestRowToResponse 把"按 issue 联查的 PR 行"转成对外响应；有可用
// 快照时填上 GraphQL 派生的可合并性/CI 结论与计数。
func issuePullRequestRowToResponse(p db.ListPullRequestsByIssueRow, snapshotEnabled bool) GitHubPullRequestResponse {
	snapshotAvailable := currentGitHubSnapshotAvailable(
		snapshotEnabled, p.HeadSha, p.SnapshotHeadSha, p.SnapshotFetchedAt,
	)
	stale := false
	if snapshotAvailable && (p.State == "open" || p.State == "draft") {
		stale = time.Since(p.SnapshotFetchedAt.Time) > prSnapshotStaleThreshold
	}
	failedNames := p.FailedCheckNames
	if failedNames == nil {
		failedNames = []string{}
	}
	resp := GitHubPullRequestResponse{
		ID:                uuidToString(p.ID),
		Provider:          "github",
		WorkspaceID:       uuidToString(p.WorkspaceID),
		RepoOwner:         p.RepoOwner,
		RepoName:          p.RepoName,
		Number:            p.PrNumber,
		Title:             p.Title,
		State:             p.State,
		HtmlURL:           p.HtmlUrl,
		Branch:            textToPtr(p.Branch),
		AuthorLogin:       textToPtr(p.AuthorLogin),
		AuthorAvatarURL:   textToPtr(p.AuthorAvatarUrl),
		MergedAt:          timestampToPtr(p.MergedAt),
		ClosedAt:          timestampToPtr(p.ClosedAt),
		PRCreatedAt:       timestampToString(p.PrCreatedAt),
		PRUpdatedAt:       timestampToString(p.PrUpdatedAt),
		MergeableState:    textToPtr(p.MergeableState),
		SnapshotAvailable: &snapshotAvailable,
		FailedCheckNames:  []string{},
		SnapshotStale:     stale,
		Additions:         p.Additions,
		Deletions:         p.Deletions,
		ChangedFiles:      p.ChangedFiles,
	}
	if snapshotAvailable {
		resp.Mergeable = lowerTextPtr(p.ApiMergeable)
		resp.MergeStateStatus = lowerTextPtr(p.ApiMergeStateStatus)
		resp.ChecksRollup = lowerTextPtr(p.ChecksRollupState)
		resp.ChecksConclusion = rollupToConclusion(p.ChecksRollupState, p.ChecksFailed, p.ChecksRunning, p.ChecksPassed)
		resp.ChecksTotal = p.ChecksTotal
		resp.ChecksPassed = p.ChecksPassed
		resp.ChecksFailed = p.ChecksFailed
		resp.ChecksRunning = p.ChecksRunning
		resp.ChecksPending = p.ChecksRunning
		resp.FailedCheckNames = failedNames
		resp.SnapshotFetchedAt = timestampToPtr(p.SnapshotFetchedAt)
	}
	return resp
}

// currentGitHubSnapshotAvailable 报告某 PR 行是否带着"当前"的 API 快照：快照
// 功能启用、抓取时间有效、快照的 head 与 PR 当前 head 一致才算。
func currentGitHubSnapshotAvailable(
	enabled bool,
	headSHA string,
	snapshotHeadSHA string,
	fetchedAt pgtype.Timestamptz,
) bool {
	return enabled && fetchedAt.Valid && snapshotHeadSHA != "" && snapshotHeadSHA == headSHA
}

// aggregateChecksConclusion 把单条 PR 的 commit-status 计数压成一个粗略状态。
// 自托管的 VCS 提供方路径（Forgejo / Gitea / GitLab）仍用它——它们经 webhook
// 镜像 commit status，而不是抓 GitHub 风格的 API 快照：
//   - 任何失败状态一票定输（"failed"）；
//   - 任何未完成状态让 PR 变 "pending"；
//   - 全部完成且通过是 "passed"；
//   - 一条状态都没有是 nil（渲染成"无检查"/隐藏）。
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

// lowerTextPtr 对非空 pgtype.Text 返回小写的 *string，否则 nil。用于把 GraphQL
// 枚举（MERGEABLE / CLEAN / SUCCESS …）按项目的小写约定暴露给 API。
func lowerTextPtr(t pgtype.Text) *string {
	if !t.Valid || t.String == "" {
		return nil
	}
	v := strings.ToLower(t.String)
	return &v
}

// rollupToConclusion 从 GraphQL rollup 推导粗略兼容的 "checks_conclusion"；
// rollup 枚举不认得时回退用 run 计数。rollup 为 null/空表示"还没有检查"→ nil
// （绝不判成 "passed"）。
func rollupToConclusion(rollup pgtype.Text, failed, running, passed int64) *string {
	if !rollup.Valid || rollup.String == "" {
		return nil
	}
	var v string
	switch strings.ToUpper(rollup.String) {
	case "FAILURE", "ERROR":
		v = "failed"
	case "PENDING", "EXPECTED":
		v = "pending"
	case "SUCCESS":
		v = "passed"
	default:
		switch {
		case failed > 0:
			v = "failed"
		case running > 0:
			v = "pending"
		case passed > 0:
			v = "passed"
		default:
			return nil
		}
	}
	return &v
}

// ── Connect / 状态 token ────────────────────────────────────────────────────

// githubAppSlug 返回用来拼安装 URL 的 GitHub App slug；集成未为本部署配置时
// 为空。
func githubAppSlug() string { return strings.TrimSpace(os.Getenv("GITHUB_APP_SLUG")) }

// githubWebhookSecret 同时用于 webhook 验签与状态 token 签名。这里刻意复用
// webhook secret 做 state 的 HMAC 密钥，让运营商只配一个值。
func githubWebhookSecret() string { return strings.TrimSpace(os.Getenv("GITHUB_WEBHOOK_SECRET")) }

// isGitHubConfigured 仅当"安装 slug 与 webhook secret 都配了"时才返回 true。
// Connect 按钮用这一个标志，前端就绝不会提供一个后端会拒掉的流程。
func isGitHubConfigured() bool { return githubAppSlug() != "" && githubWebhookSecret() != "" }

// isGitHubRepositoryBrowseConfigured 刻意与"安装流程"标志分开。App slug +
// webhook secret 足够连接一个安装，但要浏览它的仓库还必须有 App JWT 凭据，
// 让服务端能铸造短命的安装 token。
func isGitHubRepositoryBrowseConfigured() bool {
	return strings.TrimSpace(os.Getenv("GITHUB_APP_ID")) != "" &&
		strings.TrimSpace(os.Getenv("GITHUB_APP_PRIVATE_KEY")) != ""
}

// signState 生成一枚不透明 token，把 workspace ID 绑进安装流程，让 setup
// 回调能不只信任查询参数地找回 workspace。格式："<workspaceID>.<nonce>.
// <sigHex>"。
func signState(workspaceID string) (string, error) {
	return signStateForReturn(workspaceID, githubReturnToGitHub)
}

// signStateForReturn 与 signState 相同，但额外把"完成后跳回哪个页面"（returnTo）
// 一并签进 payload；returnTo 非默认值时格式为
// "<workspaceID>.<returnTo>.<nonce>.<sigHex>"。
func signStateForReturn(workspaceID, returnTo string) (string, error) {
	secret := githubWebhookSecret()
	if secret == "" {
		return "", errors.New("github integration is not configured")
	}
	if !isAllowedGitHubReturnTo(returnTo) {
		return "", errors.New("invalid github return target")
	}
	nonceBytes := make([]byte, 12)
	if _, err := rand.Read(nonceBytes); err != nil {
		return "", err
	}
	nonce := hex.EncodeToString(nonceBytes)
	payload := workspaceID + "." + nonce
	if returnTo != githubReturnToGitHub {
		payload = workspaceID + "." + returnTo + "." + nonce
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))
	return payload + "." + sig, nil
}

// verifyState 校验并解析一枚 state token，返回其中绑定的 workspaceID；无效
// 返回 ok=false。
func verifyState(token string) (string, bool) {
	workspaceID, _, ok := verifyStateWithReturn(token)
	return workspaceID, ok
}

// verifyStateWithReturn 校验并解析一枚 state token，返回绑定的 workspaceID 与
// returnTo；token 无效（长度不对、returnTo 不允许、HMAC 不匹配）返回 ok=false。
func verifyStateWithReturn(token string) (workspaceID, returnTo string, ok bool) {
	secret := githubWebhookSecret()
	if secret == "" {
		return "", "", false
	}
	// 无 returnTo 的 token 3 段、带 returnTo 的 4 段：workspaceID . [returnTo .]
	// nonce . sig
	parts := strings.Split(token, ".")
	if len(parts) != 3 && len(parts) != 4 {
		return "", "", false
	}
	workspaceID = parts[0]
	returnTo = githubReturnToGitHub
	nonceIndex := 1
	if len(parts) == 4 {
		returnTo = parts[1]
		nonceIndex = 2
		if !isAllowedGitHubReturnTo(returnTo) {
			return "", "", false
		}
	}
	sig := parts[nonceIndex+1]
	payload := strings.Join(parts[:nonceIndex+1], ".")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	expected := hex.EncodeToString(mac.Sum(nil))
	// 常量时间比较，防时序侧信道。
	if !hmac.Equal([]byte(expected), []byte(sig)) {
		return "", "", false
	}
	return workspaceID, returnTo, true
}

// isAllowedGitHubReturnTo 判断 returnTo 是否为白名单内的目标。
func isAllowedGitHubReturnTo(returnTo string) bool {
	return returnTo == githubReturnToGitHub || returnTo == githubReturnToRepositories
}

// githubSettingsURL 拼出设置页的 GitHub tab 地址，returnTo 非法时回落成默认。
func githubSettingsURL(frontend, returnTo string) string {
	if !isAllowedGitHubReturnTo(returnTo) {
		returnTo = githubReturnToGitHub
	}
	return strings.TrimRight(frontend, "/") + "/settings?tab=" + url.QueryEscape(returnTo)
}

// GitHubConnect（GET /api/workspaces/{id}/github/connect）返回浏览器应当打开、
// 用来对调用者的仓库安装 Multica GitHub App 的 URL。state token 把随后的 setup
// 回调绑定到这个 workspace。未配置集成时回 Configured:false。
func (h *Handler) GitHubConnect(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	if _, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id"); !ok {
		return
	}
	if !isGitHubConfigured() {
		writeJSON(w, http.StatusOK, GitHubConnectResponse{Configured: false})
		return
	}
	returnTo := strings.TrimSpace(r.URL.Query().Get("return_to"))
	if returnTo == "" {
		returnTo = githubReturnToGitHub
	}
	if !isAllowedGitHubReturnTo(returnTo) {
		writeError(w, http.StatusBadRequest, "invalid return target")
		return
	}
	slug := githubAppSlug()
	state, err := signStateForReturn(workspaceID, returnTo)
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

// GitHubSetupCallback（GET /api/github/setup）处理用户在 GitHub 上安装（或重新
// 授权）App 后 GitHub 发来的重定向。期望收到
// ?installation_id=<id>&state=<签名token>。它持久化安装行（workspace ↔
// installation_id 映射），然后把用户弹回 web app 里新的 Settings → GitHub tab
// （RFC MUL-2414 §4.1）。旧目的地是 catch-all 的设置页，拆出 GitHub tab 后会
// 把用户落在默认的 profile tab，而不是展示 TA 刚完成的连接的地方。
func (h *Handler) GitHubSetupCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	installationIDStr := q.Get("installation_id")
	state := q.Get("state")
	frontend := strings.TrimSpace(os.Getenv("FRONTEND_ORIGIN"))
	if frontend == "" {
		frontend = "http://localhost:3000"
	}
	settingsURL := githubSettingsURL(frontend, githubReturnToGitHub)

	if state == "" {
		http.Redirect(w, r, settingsURL+"&github_error=missing_params", http.StatusFound)
		return
	}
	workspaceID, returnTo, ok := verifyStateWithReturn(state)
	if !ok {
		http.Redirect(w, r, settingsURL+"&github_error=invalid_state", http.StatusFound)
		return
	}
	settingsURL = githubSettingsURL(frontend, returnTo)
	if installationIDStr == "" {
		http.Redirect(w, r, settingsURL+"&github_error=missing_params", http.StatusFound)
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
	// 对照 GitHub API 解析安装，补全展示信息。若 App 认证没配，仍用最少已知信息
	// 建行；webhook 事件一旦触发就会刷新它。
	login, accountType, avatar := fetchInstallationAccount(r.Context(), installationID)

	// 尽力捕获"连接操作者"（可能为 nil——公开回调在无会话时被命中，比如用户
	// 完成 GitHub 安装时没登录 Multica）。无论哪种都保存行，让 workspace owner
	// 下次刷新就能看到连接。
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

// consumePendingGitHubInstallation 检查是否存在"待处理安装"（webhook 先于 setup
// 回调到达时缓存下的账号元数据）；有则用它刷新刚建的安装行并清掉 pending 记录。
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

// fetchInstallationAccount 尽力从 GitHub 把账号名 + 头像补充到安装行里。
//
// GitHub 的 `GET /app/installations/{id}` 接口要求 GitHub App 认证（用 App 的
// RSA 私钥签名的 JWT）。运营商配了 GITHUB_APP_ID 与 GITHUB_APP_PRIVATE_KEY 时，
// 签一个短命 JWT 去用；任何失败（env 没设、密钥畸形、GitHub 非 200）都退回
// "unknown" 占位。下一次来自 GitHub 的 `installation` webhook 会用真实账号信息
// upsert 该行——见 handleInstallationEvent。
//
// HTTP 调用是同步的（无独立超时——安装路径历史遗留的小毛病），但刻意不让失败
// 中断 setup 回调：这里网络抖一下只是留下 "unknown" 占位，前端会在 webhook
// handler 发出的实时广播上重查，UI 无需手动刷新即收敛。
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
		// 私钥配错是可让运营商处理的问题——打日志，别让安装路径无痕迹地永久
		// 退化成 "unknown"。
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

// signGitHubAppJWT 铸造 GitHub 对 App 认证的 REST 调用（见
// fetchInstallationAccount）要求的短命 RS256 JWT。运营商没配 App 身份时返回
// ("", nil)——这是"App 认证不可用"的软信号而非错误，调用方可落到自己的未认证
// 路径。GITHUB_APP_PRIVATE_KEY 畸形则以错误浮出，让运营商注意到。
//
// `now` 为确定性测试注入；生产调用方传 time.Now()。
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
	// GitHub 允许 JWT 最长有效 10 分钟。这里把 `iat` 回拨 60 秒，吸收我们与
	// GitHub 之间不大不小的时钟偏移（否则 GitHub 一句"iat 在未来"就让请求失败），
	// 并把 `exp` 封在 9 分钟后，即使同样加偏移也在上限内。
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

// ── 列表 / 断开 ─────────────────────────────────────────────────────────────

// ListGitHubInstallations 向任意 workspace 成员返回该 workspace 已连接的 GitHub
// 安装列表。Connect/disconnect 在路由层仍是 admin-only，所以响应带 can_manage
// 提示，并对非 admin 调用者剥掉数字版 installation_id——他们只获得"GitHub 是否
// 接好、是谁接的"的可见性，拿不到管理句柄。
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
		"installations":                out,
		"configured":                   isGitHubConfigured(),
		"repository_browse_configured": isGitHubRepositoryBrowseConfigured(),
		"can_manage":                   canManage,
	})
}

// ListGitHubInstallationRepositories 返回某 workspace 绑定的 GitHub App 安装
// 可访问的仓库。路由 admin-only，因为私有仓库名敏感。路径用我们自己的安装行
// UUID（而非 GitHub 的数字 installation id），workspace 归属校验在任何 GitHub
// API 调用之前完成。
func (h *Handler) ListGitHubInstallationRepositories(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	if _, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id"); !ok {
		return
	}
	installationRowID := chi.URLParam(r, "installationId")
	rowUUID, ok := parseUUIDOrBadRequest(w, installationRowID, "installation id")
	if !ok {
		return
	}
	row, err := h.Queries.GetGitHubInstallationByID(r.Context(), rowUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "github installation not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load github installation")
		return
	}
	if uuidToString(row.WorkspaceID) != workspaceID {
		writeError(w, http.StatusNotFound, "github installation not found")
		return
	}
	if !isGitHubRepositoryBrowseConfigured() {
		writeError(w, http.StatusServiceUnavailable, "github repository browsing is not configured")
		return
	}
	page, ok := parseGitHubPageParam(w, r, "page", 1, 1, 100000)
	if !ok {
		return
	}
	perPage, ok := parseGitHubPageParam(w, r, "per_page", 100, 1, 100)
	if !ok {
		return
	}

	repositories, err := fetchGitHubInstallationRepositories(
		r.Context(),
		row.InstallationID,
		page,
		perPage,
	)
	if err != nil {
		slog.Warn("github: list installation repositories failed", "err", err)
		writeError(w, http.StatusBadGateway, "failed to list github repositories")
		return
	}
	writeJSON(w, http.StatusOK, repositories)
}

// parseGitHubPageParam 解析查询里的分页参数：空则用默认值；非法或越界返回 400
// 与 ok=false。
func parseGitHubPageParam(
	w http.ResponseWriter,
	r *http.Request,
	name string,
	defaultValue, minValue, maxValue int,
) (int, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return defaultValue, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minValue || value > maxValue {
		writeError(w, http.StatusBadRequest, "invalid "+name)
		return 0, false
	}
	return value, true
}

// fetchGitHubInstallationRepositories 用 App JWT 换一条短命安装 token，再用它
// 分页列出安装可访问的仓库。token 用完即吊销。
func fetchGitHubInstallationRepositories(
	ctx context.Context,
	installationID int64,
	page, perPage int,
) (GitHubRepositoriesResponse, error) {
	token, client, err := createRepositoryInstallationToken(ctx, installationID, false)
	if err != nil {
		return GitHubRepositoriesResponse{}, err
	}
	defer revokeGitHubInstallationToken(client, token)

	repositoriesEndpoint := fmt.Sprintf(
		"%s/installation/repositories?page=%d&per_page=%d",
		strings.TrimRight(githubAPIBase, "/"),
		page,
		perPage,
	)
	repositoriesReq, err := http.NewRequestWithContext(ctx, http.MethodGet, repositoriesEndpoint, nil)
	if err != nil {
		return GitHubRepositoriesResponse{}, err
	}
	setGitHubAPIHeaders(repositoriesReq, token)
	repositoriesResp, err := client.Do(repositoriesReq)
	if err != nil {
		return GitHubRepositoriesResponse{}, fmt.Errorf("list installation repositories: %w", err)
	}
	defer repositoriesResp.Body.Close()
	if repositoriesResp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(repositoriesResp.Body, githubAPIResponseLimit))
		return GitHubRepositoriesResponse{}, fmt.Errorf("list installation repositories: github status %d", repositoriesResp.StatusCode)
	}
	var body struct {
		TotalCount   int64 `json:"total_count"`
		Repositories []struct {
			ID            int64   `json:"id"`
			FullName      string  `json:"full_name"`
			HTMLURL       string  `json:"html_url"`
			CloneURL      string  `json:"clone_url"`
			Description   *string `json:"description"`
			Private       bool    `json:"private"`
			Archived      bool    `json:"archived"`
			DefaultBranch string  `json:"default_branch"`
		} `json:"repositories"`
	}
	if err := json.NewDecoder(io.LimitReader(repositoriesResp.Body, githubAPIResponseLimit)).Decode(&body); err != nil {
		return GitHubRepositoriesResponse{}, fmt.Errorf("decode installation repositories: %w", err)
	}
	out := GitHubRepositoriesResponse{
		Repositories: make([]GitHubRepositoryResponse, 0, len(body.Repositories)),
		TotalCount:   body.TotalCount,
	}
	for _, repository := range body.Repositories {
		out.Repositories = append(out.Repositories, GitHubRepositoryResponse(repository))
	}
	if int64(page*perPage) < body.TotalCount {
		nextPage := page + 1
		out.NextPage = &nextPage
	}
	return out, nil
}

// setGitHubAPIHeaders 填上 GitHub REST API 调用所需的通用头。
func setGitHubAPIHeaders(req *http.Request, token string) {
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
}

// revokeGitHubInstallationToken 在 GitHub 侧吊销短命安装 token（尽力而为，5 秒
// 超时）。
func revokeGitHubInstallationToken(client *http.Client, token string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	endpoint := strings.TrimRight(githubAPIBase, "/") + "/installation/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return
	}
	setGitHubAPIHeaders(req, token)
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, githubAPIResponseLimit))
}

// DeleteGitHubInstallation 断开一个 GitHub 安装绑定（删除本 workspace 的行）。
// 管理员的显式断开动作；随后广播 installation:deleted 事件。
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

// ── 按 issue 列 PR ─────────────────────────────────────────────────────────

// ListPullRequestsForIssue 返回某 issue 关联的全部 PR（GitHub 镜像的 +
// token 类提供方 Forgejo/Gitea/GitLab 的，合并按创建时间新→旧排序）。同时做
// 页面访问触发的异步快照刷新。调用者需能访问该 issue。
func (h *Handler) ListPullRequestsForIssue(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	rows, err := h.Queries.ListPullRequestsByIssue(r.Context(), issue.ID)
	if err != nil {
		if writeClientClosedIfCanceled(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to list pull requests")
		return
	}
	out := make([]GitHubPullRequestResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, issuePullRequestRowToResponse(row, h.PRRefresh.Enabled()))
		// 页面访问触发（MUL-5265）：若这张卡片的快照缺失或比视图 TTL 旧，发起
		// 异步刷新。非阻塞——当前（可能过期）的响应立刻返回，新快照通过
		// pull_request:updated 实时事件到达。
		h.PRRefresh.MaybeEnqueueOnView(
			row.InstallationID, row.RepoOwner, row.RepoName, row.PrNumber,
			row.SnapshotFetchedAt.Time,
			row.SnapshotFetchedAt.Valid &&
				row.SnapshotHeadSha != "" &&
				row.SnapshotHeadSha == row.HeadSha,
		)
	}
	// token 类提供方（Forgejo / Gitea / GitLab）的 PR 共用同一卡片列表。它们活在
	// 各自打上提供方标签的表里，在这里映射成同一响应形态合并进来；合并列表按
	// 创建时间新→旧重排。
	vcsRows, err := h.Queries.ListVCSPullRequestsByIssue(r.Context(), issue.ID)
	if err != nil {
		if writeClientClosedIfCanceled(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to list pull requests")
		return
	}
	for _, row := range vcsRows {
		out = append(out, vcsPullRequestRowToResponse(row))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].PRCreatedAt > out[j].PRCreatedAt
	})
	writeJSON(w, http.StatusOK, map[string]any{"pull_requests": out})
}

// broadcastPRSnapshotApplied 是 ghsnapshot 流水线的 onApplied 回调：一旦 API
// 快照写进某条 PR 行，就把该 PR 重新广播，让每个打开的 issue 详情页重查 PR 列表、
// 拿到新鲜的 CI/可合并性状态。跑在后台流水线 goroutine 上。
func (h *Handler) broadcastPRSnapshotApplied(ctx context.Context, prID pgtype.UUID) {
	pr, err := h.Queries.GetGitHubPullRequestByID(ctx, prID)
	if err != nil {
		return
	}
	issueIDs, err := h.Queries.ListIssueIDsForPullRequest(ctx, prID)
	if err != nil {
		return
	}
	linked := make([]string, 0, len(issueIDs))
	for _, id := range issueIDs {
		linked = append(linked, uuidToString(id))
	}
	h.publish(protocol.EventPullRequestUpdated, uuidToString(pr.WorkspaceID), "system", "", map[string]any{
		"pull_request":     githubPullRequestToResponse(pr, h.PRRefresh.Enabled()),
		"linked_issue_ids": linked,
	})
}

// ── Webhook ─────────────────────────────────────────────────────────────────

// identifierRe 从文本里抽出 "MUL-1510" 这类标识符。大小写不敏感，因为分支名
// 习惯小写而 issue 前缀是大写。左端词边界防止命中 email 式字符串
// （如 "abc@MUL-1"），右端数字锚点排除版本号（如 "v1.2-3"）。
var identifierRe = regexp.MustCompile(`(?i)\b([a-z][a-z0-9]{0,9})-(\d+)\b`)

// closingIdentifierRe 抽出紧跟 GitHub 式关闭关键字（"close[sd]?"、"fix(e[sd])?"、
// "resolve[sd]?"）后面的标识符，关键字后可选地跟冒号与空白。匹配刻意在"紧邻"
// 上严格——"Fix MUL-1" 关闭 MUL-1，但 "Fix login MUL-1" 不关闭。这镜像
// GitHub 自己的关闭关键字语法，是 webhook 判断"PR 合并后是否把 issue 自动推进
// 到 done"的门。像 "Follow up in MUL-2" 这样的引用、以及 "MUL-1: ..." 这样纯
// 标题前缀的引用（经 identifierRe）只关联 PR，绝不自动关闭。
var closingIdentifierRe = regexp.MustCompile(
	`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)[:\s]+([a-z][a-z0-9]{0,9})-(\d+)\b`,
)

// HandleGitHubWebhook（POST /api/webhooks/github）是 GitHub 投递已连接安装的
// 每个事件的目的地。它校验 HMAC 签名、按 X-GitHub-Event 分流，upsert PR 行并
// 自动关联 issue，或在卸载时删除安装。任何事件都以 202 确认（让 GitHub 别把
// 接口标记为失败）。
func (h *Handler) HandleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20)) // 10 MiB 上限
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body failed")
		return
	}
	secret := githubWebhookSecret()
	if secret == "" {
		// 干脆拒绝处理 webhook 比把未配置部署当"所有签名都有效"更安全。
		writeError(w, http.StatusServiceUnavailable, "github webhooks not configured")
		return
	}
	sigHeader := r.Header.Get("X-Hub-Signature-256")
	if !verifyWebhookSignature(secret, sigHeader, body) {
		writeError(w, http.StatusUnauthorized, "invalid signature")
		return
	}
	event := r.Header.Get("X-GitHub-Event")
	ctx := r.Context()
	switch event {
	case "ping":
		writeJSON(w, http.StatusOK, map[string]string{"ok": "pong"})
		return
	case "installation":
		h.handleInstallationEvent(ctx, body)
	case "pull_request":
		h.handlePullRequestEvent(ctx, body)
	case "check_suite", "check_run", "status":
		// CI 事件在方案 C（MUL-5265）下是纯触发器：它们的载荷从不用于展示。每个
		// 只是请 API 流水线为涉及的 PR 重新抓权威快照。
		h.triggerPRRefreshFromCIEvent(ctx, body)
	default:
		// 确认每个事件，避免 GitHub 标记接口失败；但不建模的类型直接忽略。
	}
	w.WriteHeader(http.StatusAccepted)
}

// verifyWebhookSignature 用 HMAC-SHA256 校验 X-Hub-Signature-256 头是否与请求
// body 匹配（常量时间比较）。
func verifyWebhookSignature(secret, header string, body []byte) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	want, err := hex.DecodeString(strings.TrimPrefix(header, prefix))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), want)
}

// ghInstallationPayload 是 installation webhook 事件里我们要读的部分。
type ghInstallationPayload struct {
	Action       string `json:"action"`
	Installation struct {
		ID      int64 `json:"id"`
		Account struct {
			Login     string `json:"login"`
			Type      string `json:"type"`
			AvatarURL string `json:"avatar_url"`
		} `json:"account"`
	} `json:"installation"`
}

// githubInstallationAccountFromPayload 从安装事件载荷抽出账号展示信息；缺 login
// 时返回 ok=false。
func githubInstallationAccountFromPayload(p ghInstallationPayload) (login, accountType string, avatar *string, ok bool) {
	login = strings.TrimSpace(p.Installation.Account.Login)
	if login == "" {
		return "", "", nil, false
	}
	accountType = coalesce(p.Installation.Account.Type, "User")
	avatar = strPtrOrNil(p.Installation.Account.AvatarURL)
	return login, accountType, avatar, true
}

// handleInstallationEvent 处理 installation webhook：deleted/suspend 删除全部
// workspace 绑定并逐个广播；created/新权限/unsuspend 则刷新或暂存账号元数据。
func (h *Handler) handleInstallationEvent(ctx context.Context, body []byte) {
	var p ghInstallationPayload
	if err := json.Unmarshal(body, &p); err != nil {
		slog.Warn("github: bad installation payload", "err", err)
		return
	}
	switch p.Action {
	case "deleted", "suspend":
		// 用户在 GitHub 删掉/挂起了 App——对这个 installation_id 的信任彻底
		// 没了，所以丢弃每个 workspace 绑定。用 DELETE … RETURNING 让每条广播
		// 都能作用到它自己的 workspace；没有 WorkspaceID 的事件会被实时监听器
		// 丢弃，让已打开的 Settings tab 保持陈旧。
		deleted, err := h.Queries.DeleteGitHubInstallationByInstallationID(ctx, p.Installation.ID)
		if err != nil {
			slog.Warn("github: delete installation failed", "err", err, "installation_id", p.Installation.ID)
			return
		}
		if err := h.Queries.DeletePendingGitHubInstallation(ctx, p.Installation.ID); err != nil {
			slog.Warn("github: delete pending installation failed", "err", err, "installation_id", p.Installation.ID)
		}
		// 只广播内部行 id——数字版 installation_id 是非管理员不可见的管理句柄。
		// 前端收到此事件后让 installations 查询失效，不直接读广播载荷。每条被删
		// 绑定一条广播，让每个受影响 workspace 的 Settings tab 都刷新。
		for _, row := range deleted {
			h.publish(protocol.EventGitHubInstallationDeleted, uuidToString(row.WorkspaceID), "system", "", map[string]any{
				"id": uuidToString(row.ID),
			})
		}
	case "created", "new_permissions_accepted", "unsuspend":
		login, accountType, avatar, ok := githubInstallationAccountFromPayload(p)
		if !ok {
			slog.Warn("github: installation payload missing account login", "installation_id", p.Installation.ID)
			return
		}

		// 单靠 webhook 不知道它映射到哪些 workspace。若还没有 setup 回调建过
		// workspace 绑定，就把账号元数据暂存起来，让回调在创建 github_installation
		// 后消费。
		existing, err := h.Queries.ListGitHubInstallationsByInstallationID(ctx, p.Installation.ID)
		if err != nil {
			slog.Warn("github: lookup installation failed", "err", err, "installation_id", p.Installation.ID)
			return
		}
		if len(existing) == 0 {
			if _, err := h.Queries.UpsertPendingGitHubInstallation(ctx, db.UpsertPendingGitHubInstallationParams{
				InstallationID:   p.Installation.ID,
				AccountLogin:     login,
				AccountType:      accountType,
				AccountAvatarUrl: ptrToText(avatar),
			}); err != nil {
				slog.Warn("github: store pending installation failed", "err", err, "installation_id", p.Installation.ID)
			}
			return
		}
		// 刷新每个 workspace 绑定的账号展示元数据；workspace_id 与 connected_by_id
		// 不动。
		refreshed, err := h.Queries.UpdateGitHubInstallationAccountByInstallationID(ctx, db.UpdateGitHubInstallationAccountByInstallationIDParams{
			InstallationID:   p.Installation.ID,
			AccountLogin:     login,
			AccountType:      accountType,
			AccountAvatarUrl: ptrToText(avatar),
		})
		if err != nil {
			slog.Warn("github: refresh installation failed", "err", err)
			return
		}
		if err := h.Queries.DeletePendingGitHubInstallation(ctx, p.Installation.ID); err != nil {
			slog.Warn("github: delete pending installation failed", "err", err, "installation_id", p.Installation.ID)
		}
		// 广播让任何打开的 Settings → GitHub tab 重查安装列表。不广播的话，setup
		// 回调以 "unknown" 占位建的行（例如 GitHub App JWT 认证没配，或这个
		// webhook 在用户已加载页面后才到）会一直看着过期，直到用户手动刷新。
		// 每个绑定的 workspace 一条广播。
		for _, inst := range refreshed {
			h.publish(protocol.EventGitHubInstallationCreated, uuidToString(inst.WorkspaceID), "system", "", map[string]any{
				"installation": githubInstallationToBroadcast(inst),
			})
		}
	}
}

// ghPullRequestPayload 是 pull_request webhook 事件里我们要读的部分。
type ghPullRequestPayload struct {
	Action      string `json:"action"`
	PullRequest struct {
		Number         int32  `json:"number"`
		HTMLURL        string `json:"html_url"`
		Title          string `json:"title"`
		Body           string `json:"body"`
		State          string `json:"state"`
		Draft          bool   `json:"draft"`
		Merged         bool   `json:"merged"`
		MergedAt       string `json:"merged_at"`
		ClosedAt       string `json:"closed_at"`
		CreatedAt      string `json:"created_at"`
		UpdatedAt      string `json:"updated_at"`
		MergeableState string `json:"mergeable_state"`
		Additions      int32  `json:"additions"`
		Deletions      int32  `json:"deletions"`
		ChangedFiles   int32  `json:"changed_files"`
		Head           struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
		User struct {
			Login     string `json:"login"`
			AvatarURL string `json:"avatar_url"`
		} `json:"user"`
	} `json:"pull_request"`
	Changes    *ghPRChanges `json:"changes"`
	Repository struct {
		Name  string `json:"name"`
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

// handlePullRequestEvent 把一条 pull_request webhook 镜像进所有绑定了该安装的
// workspace：先统一裁定"关闭意图归谁"（closePolicy），再对每个 workspace 独立
// mirror，最后把这次 head 变化交给 API 快照流水线。
func (h *Handler) handlePullRequestEvent(ctx context.Context, body []byte) {
	var p ghPullRequestPayload
	if err := json.Unmarshal(body, &p); err != nil {
		slog.Warn("github: bad pull_request payload", "err", err)
		return
	}
	if p.Installation.ID == 0 {
		return
	}
	insts, err := h.Queries.ListGitHubInstallationsByInstallationID(ctx, p.Installation.ID)
	if err != nil {
		slog.Warn("github: lookup installation failed", "err", err)
		return
	}
	if len(insts) == 0 {
		// 来自从未接线的安装的 webhook——无法归因到任何 workspace，静默丢弃。
		return
	}
	// #4855 允许一个 GitHub App 安装绑定多个 workspace。某仓库的事件属于每个
	// 绑定 workspace，所以把投递扇出：每个 workspace 独立镜像该 PR 并对照它自己
	// 的 issue（自己的前缀 + github 开关）自动关联。仓库范围由 GitHub 授权给该
	// 安装的东西决定；这里刻意不 gate 在 workspace.repos 注册表上——那份名单是
	// "agent 要克隆的代码"，不是 webhook 订阅（MUL-4343）。
	//
	// 扇出意味着一个标识符可能同时在多个 workspace 解析（issue 前缀不是全局唯一，
	// issue 号在每个 workspace 从 1 重新开始，两个绑定 workspace 可以各有一个真实
	// 的 "ABC-100"）。在任一 workspace 关联之前就裁定"谁——若有——能对关闭关键字
	// 动手"，并把该结论当作整次投递的权威，mirror 过程就不能各自推导出不同答案。
	// 见 closeIntentPolicy。
	closePolicy := h.resolveCloseIntentPolicy(ctx, insts, &p)
	for _, inst := range insts {
		h.mirrorPullRequestForWorkspace(ctx, inst.WorkspaceID, inst.InstallationID, &p, closePolicy)
	}
	// PR 行现在带着新 head；请 API 流水线为该 head 抓权威 CI + 可合并性快照。
	// webhook 只是门铃——它自带的 mergeable/checks 载荷不再用于展示（MUL-5265）。
	h.PRRefresh.Enqueue(p.Installation.ID, p.Repository.Owner.Login, p.Repository.Name, p.PullRequest.Number)
}

// closeIntentPolicy 决定本次投递允许对哪些 (关闭标识符, workspace) 对动手，
// 并且是该决定的唯一权威：per-workspace 的 mirror 过程只查询它，而不从自己的
// 读取里重新推导。
//
// 它是允许名单而非拒绝名单，因此构造上就 fail closed——无法被正面归因到恰好
// 一个 workspace 的标识符就直接缺席，缺席即拒绝。零值因此什么都不允许，任何
// 不确定的读取都返回它。
type closeIntentPolicy struct {
	// unrestricted 标记"单绑定"情形：只有一个绑定 workspace 时不存在跨
	// workspace 歧义，每个关闭标识符保持 #6804 之前的行为，扫描不读任何库。
	unrestricted bool
	// owner 把某个关闭标识符映射到"被证明能解析它"的唯一 workspace。记录赢家
	// （而不是光记"允许"）也封住了扫描与 mirror 之间的窗口：若中间另一个
	// workspace 长出同号 issue，它不是记录的 owner，仍不能动手。
	owner map[string]string
}

// permits 报告 workspaceID 是否可对 identifier 携带关闭意图。
func (c closeIntentPolicy) permits(identifier, workspaceID string) bool {
	if c.unrestricted {
		return true
	}
	owner, ok := c.owner[identifier]
	return ok && owner == workspaceID
}

// resolveCloseIntentPolicy 在任一 workspace 关联之前裁定：这条 PR 上哪些关闭
// 标识符可以推进 issue、在哪个 workspace 里推进。
//
// issue 前缀刻意不做全局唯一（#2797），issue 号在每个 workspace 从 1 重新开始，
// 所以 #5183 把 PR webhook 扇出到每个绑定 workspace 后，"Closes ABC-100" 可能
// 在它们各自解析到真实——却不同——的 issue。两边都关联是可恢复的噪音；把两边
// 都自动推进到 done 则不可恢复，因为那会悄悄改写一个与本 PR 毫无关系的
// workspace 里 issue 的状态（#6804）。
//
// 一个标识符只有在"恰好一个绑定 workspace 被证明能解析它"时才允许。任何其他
// 情形——两个能解析、或一次读不完整——都把它排除，让没有 workspace 对它动手。
// 这种不对称是刻意的：把"唯一"误判成"有歧义"的代价是少一次人工可补的自动
// 关闭，而把"有歧义"误判成"唯一"会悄悄关掉别人的 issue。
//
// 只检查关闭标识符，且只在安装有多个绑定时检查——没有关闭关键字的 PR，或占
// 绝大多数的单 workspace 安装，不做任何额外工作。
func (h *Handler) resolveCloseIntentPolicy(ctx context.Context, insts []db.GithubInstallation, p *ghPullRequestPayload) closeIntentPolicy {
	if len(insts) < 2 {
		return closeIntentPolicy{unrestricted: true}
	}
	idents := extractClosingIdentifiers(p.PullRequest.Title, p.PullRequest.Body)
	if len(idents) == 0 {
		return closeIntentPolicy{}
	}
	prNumber := p.PullRequest.Number
	repo := p.Repository.Owner.Login + "/" + p.Repository.Name

	// 收集能解析每个标识符的所有 workspace。任何读不完整都放弃整个事件：没查到
	// 的那个 workspace 可能是第二个能解析的，不排除它就不能把别的 workspace 称
	// 为唯一。
	resolvers := make(map[string][]string, len(idents))
	for _, inst := range insts {
		ws, err := h.Queries.GetWorkspace(ctx, inst.WorkspaceID)
		if err != nil {
			slog.Warn("github: cannot load bound workspace, withholding close intent for this delivery",
				"err", err, "installation_id", p.Installation.ID, "repo", repo, "pr_number", prNumber)
			return closeIntentPolicy{}
		}
		// 关闭自动关联的 workspace 从不写关联行，所以它不是竞争者，不该压制本可
		// 合法动手的 workspace。解不开的 settings blob 不构成任何一方的证据，按
		// fail closed 处理而不是默认"已启用"。
		autoLink, err := autoLinkPRsEnabledForWorkspace(ws)
		if err != nil {
			slog.Warn("github: cannot read workspace auto-link setting, withholding close intent for this delivery",
				"err", err, "workspace_id", uuidToString(inst.WorkspaceID),
				"installation_id", p.Installation.ID, "repo", repo, "pr_number", prNumber)
			return closeIntentPolicy{}
		}
		if !autoLink {
			continue
		}
		prefix := issuePrefixForWorkspace(ws)
		for _, id := range idents {
			number, ok := issueNumberForPrefix(id, prefix)
			if !ok {
				continue
			}
			if _, err := h.Queries.GetIssueByNumber(ctx, db.GetIssueByNumberParams{
				WorkspaceID: inst.WorkspaceID,
				Number:      number,
			}); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					// 已证明没有：这个 workspace 不存在该 issue。
					continue
				}
				slog.Warn("github: cannot resolve identifier in bound workspace, withholding close intent for this delivery",
					"err", err, "identifier", id, "workspace_id", uuidToString(inst.WorkspaceID),
					"installation_id", p.Installation.ID, "repo", repo, "pr_number", prNumber)
				return closeIntentPolicy{}
			}
			resolvers[id] = append(resolvers[id], uuidToString(inst.WorkspaceID))
		}
	}

	policy := closeIntentPolicy{owner: make(map[string]string, len(resolvers))}
	for id, wss := range resolvers {
		if len(wss) == 1 {
			policy.owner[id] = wss[0]
			continue
		}
		// 这个要防的症状（issue 在无可见原因下推进到 done）否则无法溯源回
		// GitHub，所以留下面包屑，点出运营商必须靠改前缀解决的冲突。
		slog.Warn("github: ambiguous closing identifier across bound workspaces, withholding close intent",
			"identifier", id,
			"workspaces", len(wss),
			"installation_id", p.Installation.ID,
			"repo", repo,
			"pr_number", prNumber,
		)
	}
	return policy
}

// ghCIEventPayload 捕获 check_suite / check_run / status webhook 共有的形状——
// 足以解析该刷新哪条 PR。方案 C 下这些事件是纯触发器：载荷数据从不用于展示。
type ghCIEventPayload struct {
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Repository struct {
		Name  string `json:"name"`
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
	// status 事件：顶层 commit SHA，没有 PR 号。
	SHA        string `json:"sha"`
	CheckSuite struct {
		HeadSHA      string `json:"head_sha"`
		PullRequests []struct {
			Number int32 `json:"number"`
		} `json:"pull_requests"`
	} `json:"check_suite"`
	CheckRun struct {
		PullRequests []struct {
			Number int32 `json:"number"`
		} `json:"pull_requests"`
		CheckSuite struct {
			HeadSHA string `json:"head_sha"`
		} `json:"check_suite"`
	} `json:"check_run"`
}

// triggerPRRefreshFromCIEvent 为 check_suite / check_run / status webhook 涉及的
// PR 入队一次 API 刷新。check_suite/check_run 直接带 PR 号；status 事件只带一个
// commit SHA，所以用 head_sha 反查镜像的 PR。
func (h *Handler) triggerPRRefreshFromCIEvent(ctx context.Context, body []byte) {
	if !h.PRRefresh.Enabled() {
		return
	}
	var p ghCIEventPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return
	}
	if p.Installation.ID == 0 || p.Repository.Name == "" {
		return
	}
	owner, repo := p.Repository.Owner.Login, p.Repository.Name

	seen := map[int32]struct{}{}
	enqueue := func(number int32) {
		if number == 0 {
			return
		}
		if _, ok := seen[number]; ok {
			return
		}
		seen[number] = struct{}{}
		h.PRRefresh.Enqueue(p.Installation.ID, owner, repo, number)
	}
	for _, pr := range p.CheckSuite.PullRequests {
		enqueue(pr.Number)
	}
	for _, pr := range p.CheckRun.PullRequests {
		enqueue(pr.Number)
	}
	if len(seen) > 0 {
		return
	}
	// 载荷里没有 PR 号（status 事件，或 pull_requests 数组为空的 check 事件）——
	// 按 head SHA 解析。
	sha := p.SHA
	if sha == "" {
		sha = coalesce(p.CheckSuite.HeadSHA, p.CheckRun.CheckSuite.HeadSHA)
	}
	if sha == "" {
		return
	}
	numbers, err := h.Queries.ListGitHubPRNumbersByHeadSHA(ctx, db.ListGitHubPRNumbersByHeadSHAParams{
		InstallationID: p.Installation.ID,
		RepoOwner:      owner,
		RepoName:       repo,
		HeadSha:        sha,
	})
	if err != nil {
		return
	}
	for _, number := range numbers {
		enqueue(number)
	}
}

// mirrorPullRequestForWorkspace 把一条 pull_request webhook 镜像进单个
// workspace：upsert PR 行、重放镜像前就已到达的 check_suite 事件、自动关联被引用
// 的 issue（由该 workspace 的 github 开关把关）、在终态事件上推进 issue，并广播
// 变更。对投递安装绑定的每个 workspace 调用一次。
//
// closePolicy 是投递级的裁定：哪些关闭标识符允许本 workspace 动手。它不允许的
// 标识符仍可关联，但绝不带 close_intent，因此永远不可能把 issue 推进到 done。
// 本函数只会收窄该裁定——它不能授予策略扣下的关闭意图。
func (h *Handler) mirrorPullRequestForWorkspace(ctx context.Context, wsID pgtype.UUID, installationID int64, p *ghPullRequestPayload, closePolicy closeIntentPolicy) {
	state := derivePRState(p.PullRequest.State, p.PullRequest.Draft, p.PullRequest.Merged)
	mergeable, clearMergeable := derivePRMergeableState(p.Action, p.PullRequest.MergeableState, baseRefChanged(p.Changes))
	pr, err := h.Queries.UpsertGitHubPullRequest(ctx, db.UpsertGitHubPullRequestParams{
		WorkspaceID:         wsID,
		InstallationID:      installationID,
		RepoOwner:           p.Repository.Owner.Login,
		RepoName:            p.Repository.Name,
		PrNumber:            p.PullRequest.Number,
		Title:               p.PullRequest.Title,
		State:               state,
		HtmlUrl:             p.PullRequest.HTMLURL,
		Branch:              ptrToText(strPtrOrNil(p.PullRequest.Head.Ref)),
		AuthorLogin:         ptrToText(strPtrOrNil(p.PullRequest.User.Login)),
		AuthorAvatarUrl:     ptrToText(strPtrOrNil(p.PullRequest.User.AvatarURL)),
		MergedAt:            parseGHTime(p.PullRequest.MergedAt),
		ClosedAt:            parseGHTime(p.PullRequest.ClosedAt),
		PrCreatedAt:         parseGHTimeRequired(p.PullRequest.CreatedAt),
		PrUpdatedAt:         parseGHTimeRequired(p.PullRequest.UpdatedAt),
		HeadSha:             p.PullRequest.Head.SHA,
		MergeableState:      mergeable,
		ClearMergeableState: pgtype.Bool{Bool: clearMergeable, Valid: true},
		Additions:           p.PullRequest.Additions,
		Deletions:           p.PullRequest.Deletions,
		ChangedFiles:        p.PullRequest.ChangedFiles,
	})
	if err != nil {
		slog.Warn("github: upsert pr failed", "err", err)
		return
	}

	workspaceID := uuidToString(wsID)
	resp := githubPullRequestToResponse(pr, h.PRRefresh.Enabled())

	// 自动关联：扫标题/正文/分支里的 issue 标识符，在本 workspace 查出来后挂上
	// 关联行。幂等（ON CONFLICT upsert close_intent 标志——见
	// LinkIssueToPullRequest），重复触发 webhook 不会复制。
	//
	// RFC MUL-2414 §4.8：上面的 PR mirror upsert 总是执行（重启用 GitHub 功能时
	// 无需回填就能恢复历史），但关联行是"新的副作用"，必须由 workspace 的
	// auto-link 开关把关（开关本身在 master 的 `github_enabled` 关闭时短路）。
	linkedIssueIDs := make([]string, 0)
	if h.workspaceAutoLinkPRsEnabled(ctx, wsID) {
		idents := extractIdentifiers(p.PullRequest.Title, p.PullRequest.Body, p.PullRequest.Head.Ref)
		// closingIdents 是这条 PR 通过关闭关键字（"Closes/Fixes/Resolves MUL-X"）
		// 显式声明的标识符子集。每个提及（上面的 idents）仍会关联，但关联行的
		// close_intent 列——以及随后自动推进的闸是否触发——只对关键字声明的
		// 标识符置位。纯标题前缀与分支名引用只关联。
		closingIdents := map[string]struct{}{}
		for _, c := range extractClosingIdentifiers(p.PullRequest.Title, p.PullRequest.Body) {
			closingIdents[c] = struct{}{}
		}
		// qualifyingIdents 是真正把这条 PR 与某 issue 绑定的标识符：标题前缀、
		// 分支名引用、或正文关闭关键字。已关联但不在此集合里的标识符只是正文里
		// 的顺带提及（"Related MUL-1"、"Follow up in MUL-1"）。这些关联仍会记录
		// （auto-link 保持慷慨，便于跨编辑追踪 close_intent），但打上
		// reference_only 标志并从 issue 的 PR 列表隐藏——顺带一提不应让 PR 以
		// "进行中的 PR"身份出现在该 issue 下（MUL-3739）。
		qualifyingIdents := map[string]struct{}{}
		for _, id := range extractIdentifiers(p.PullRequest.Title, p.PullRequest.Head.Ref) {
			qualifyingIdents[id] = struct{}{}
		}
		for c := range closingIdents {
			qualifyingIdents[c] = struct{}{}
		}
		// close_intent 应跟随 PR 终态关闭事件前的标题/正文——那时 PR 仍可编辑。
		// 一旦 GitHub 投递了终态事件，之后的 edit/synchronize webhook 绝不能
		// 改写合并时刻的关闭决定。
		preserveCloseIntent := p.Action != "closed" && (state == "merged" || state == "closed")
		prefix := h.getIssuePrefix(ctx, wsID)
		// reevalIssues 收集每个关联行刚被我们动过的 issue，以便本事件里每次关联
		// upsert 之后，都能对照持久化的聚合重跑自动推进闸。用持久化状态驱动闸
		// （而不是"本次 webhook 有没有声明关闭意图"）正是修多 PR 兄弟情形的
		// 办法：带 `Closes MUL-1` 的 PR 先合并，而仅关联的兄弟还开着，兄弟稍后
		// 再关——它的 webhook 没有关闭关键字，但早先的关联行带着
		// close_intent=true，MUL-1 仍会推进。
		reevalIssues := make([]db.Issue, 0, len(idents))
		for _, id := range idents {
			issue, ok := h.lookupIssueByIdentifier(ctx, wsID, prefix, id)
			if !ok {
				continue
			}
			_, declared := closingIdents[id]
			if declared && !closePolicy.permits(id, workspaceID) {
				// 投递级扫描没证明本 workspace 是这个关闭关键字所指的 workspace，
				// 所以它不动手。带着 declared=false 继续，也会清掉 #6804 之前某次
				// 投递在行上已设的 close_intent——重发的 webhook 会治愈存下的决定。
				declared = false
			}
			closeIntent := declared && !preserveCloseIntent
			_, qualifies := qualifyingIdents[id]
			referenceOnly := !qualifies
			if err := h.Queries.LinkIssueToPullRequest(ctx, db.LinkIssueToPullRequestParams{
				IssueID:             issue.ID,
				PullRequestID:       pr.ID,
				CloseIntent:         closeIntent,
				ReferenceOnly:       referenceOnly,
				PreserveCloseIntent: preserveCloseIntent,
				LinkedByType:        strToText("system"),
				LinkedByID:          pgtype.UUID{},
			}); err != nil {
				slog.Warn("github: link failed", "err", err)
				continue
			}
			linkedIssueIDs = append(linkedIssueIDs, uuidToString(issue.ID))
			reevalIssues = append(reevalIssues, issue)
		}

		// PR 的终态事件（merged 或 closed）可能是最后一个在途兄弟定局的时刻。
		// PR 行与关联行都持久化后，对刚关联的每个 issue 重估一次，让聚合查询
		// 看到最新状态。满足以下条件才把 issue 推进到 done：
		//   1. issue 还不是终态（`done` / `cancelled`）；
		//   2. 没有关联 PR 仍是 `open` / `draft`；
		//   3. 至少一条已合并的关联 PR 声明过 close_intent（其关联行上有
		//      "Closes/Fixes/Resolves" 关键字）。
		// 规则 (3) 既防止 "Follow up in MUL-2" / "Unblocks MUL-3" 被当作
		// "Closes MUL-1"，也防止"全部关闭但没合并"的序列悄悄自动关闭 issue——
		// 从没投递过携带关闭意图的东西时，应该由用户手动决定。
		if state == "merged" || state == "closed" {
			for _, issue := range reevalIssues {
				// 自定义的终态状态在此也算终态。（MUL-6243）
				if s := issuestatus.Effective(ctx, h.Queries, issue.WorkspaceID, issue.Status); s == "done" || s == "cancelled" {
					continue
				}
				// 跨提供方合并：issue 可能还挂着一条仍 open 的自托管 VCS PR，它
				// 在这里必须像 VCS webhook 路径里 open 的 GitHub PR 挡住自动推进
				// 一样拦住它。
				counts, err := h.Queries.GetIssueCombinedPullRequestCloseAggregate(ctx, issue.ID)
				if err != nil {
					slog.Warn("github: count linked pr states failed", "err", err, "issue_id", uuidToString(issue.ID))
					continue
				}
				if counts.OpenCount == 0 && counts.MergedWithCloseIntentCount > 0 {
					h.advanceIssueToDone(ctx, issue, workspaceID)
				}
			}
		}
	}

	// 把 PR 变更广播给 workspace，让任何打开的 issue 详情页重查它的 PR 列表。
	h.publish(protocol.EventPullRequestUpdated, workspaceID, "system", "", map[string]any{
		"pull_request":     resp,
		"linked_issue_ids": linkedIssueIDs,
	})
}

// derivePRMergeableState 决定一条 pull_request webhook 对 PR 行 mergeable_state
// 列的 upsert 行为。它返回三态，编码为 (value, clear)：
//
//   - clear=true → 把该列强制置 NULL。状态变更动作（opened、synchronize、
//     reopened、或换 base 分支）必须清空该值，因为 GitHub 是异步重算可合并性的；
//     载荷仍可能带着上一 head 的 clean/dirty 答案，信任它等于把过期结论对着
//     新 head 展示。
//   - clear=false 且 value 有效 → 写入该值。事件携带了应持久化的具体结论。
//   - clear=false 且 value 无效 → 保留现有列。元数据事件（labeled/assigned/
//     edited-未换base）即使上次结论仍准确也常把 mergeable_state 留空，若用
//     NULL 静默覆盖 clean/dirty，会丢掉 GitHub 只懒刷新一次的信息。
func derivePRMergeableState(action, payload string, baseRefChanged bool) (pgtype.Text, bool) {
	if action == "opened" || action == "synchronize" || action == "reopened" {
		return pgtype.Text{}, true
	}
	if action == "edited" && baseRefChanged {
		return pgtype.Text{}, true
	}
	if payload == "" {
		return pgtype.Text{}, false
	}
	return pgtype.Text{String: payload, Valid: true}, false
}

// ghPRChanges 只捕获 pull_request.edited 载荷 changes 里我们在意的那个字段：
// 换 base 分支。其余（标题、正文）不破坏可合并性。
type ghPRChanges struct {
	Base *struct {
		Ref *struct {
			From string `json:"from"`
		} `json:"ref"`
	} `json:"base"`
}

// baseRefChanged 判断 pull_request.edited 事件是否表示 PR 的 base 分支被换掉。
// 只有这类编辑会使现有的 mergeable_state 失效。
func baseRefChanged(c *ghPRChanges) bool {
	return c != nil && c.Base != nil && c.Base.Ref != nil && c.Base.Ref.From != ""
}

// derivePRState 把 webhook 里的 state/draft/merged 归一到产品内的 PR 状态：
// merged > closed > draft > open。
func derivePRState(state string, draft, merged bool) string {
	if merged {
		return "merged"
	}
	if state == "closed" {
		return "closed"
	}
	if draft {
		return "draft"
	}
	return "open"
}

// parseGHTime 解析 RFC3339 时间串；空串或非法返回未设值的 pgtype.Timestamptz。
func parseGHTime(s string) pgtype.Timestamptz {
	if s == "" {
		return pgtype.Timestamptz{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// parseGHTimeRequired 与 parseGHTime 相同，但解析失败时不返回零值，而用当前
// UTC 时间兜底——用于 PR 行上必填的创建/更新时间。
func parseGHTimeRequired(s string) pgtype.Timestamptz {
	t := parseGHTime(s)
	if !t.Valid {
		return pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	}
	return t
}

// extractIdentifiers 从给定字段里抽出全部 "PREFIX-NUMBER" 匹配，按输入顺序
// 去重。
func extractIdentifiers(parts ...string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, src := range parts {
		for _, m := range identifierRe.FindAllStringSubmatch(src, -1) {
			ident := strings.ToUpper(m[1]) + "-" + m[2]
			if _, dup := seen[ident]; dup {
				continue
			}
			seen[ident] = struct{}{}
			out = append(out, ident)
		}
	}
	return out
}

// extractClosingIdentifiers 抽出紧跟 GitHub 式关闭关键字的全部
// "PREFIX-NUMBER" 标识符，按输入顺序去重。分支名里的标识符刻意排除——调用方只
// 传标题与正文——因为分支名不是自然语言字段，把 "mul-1/fix-login" 当关闭声明
// 会悄悄让这个闸本来要修的 bug 复发。
func extractClosingIdentifiers(parts ...string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, src := range parts {
		for _, m := range closingIdentifierRe.FindAllStringSubmatch(src, -1) {
			ident := strings.ToUpper(m[1]) + "-" + m[2]
			if _, dup := seen[ident]; dup {
				continue
			}
			seen[ident] = struct{}{}
			out = append(out, ident)
		}
	}
	return out
}

// autoLinkPRsEnabledForWorkspace 报告该 workspace 是否允许 GitHub webhook 创建
// issue ↔ PR 关联行。默认 true，让早于 RFC MUL-2414 的 workspace 保持历史
// "auto-link 开启"行为；master 的 GitHub 总开关显式关闭时短路为 false——与
// 客户端侧使用的优先级一致。
//
// 解不开的 settings blob 以错误浮出，而不是折进"宽松默认"。决定是否写关联行的
// 调用方继续用默认值，但关闭意图扫描必须区分"auto-link 开"与"我们没能查出来"
// ——见 closeIntentPolicy。
func autoLinkPRsEnabledForWorkspace(ws db.Workspace) (bool, error) {
	if len(ws.Settings) == 0 {
		return true, nil
	}
	var s struct {
		GitHubEnabled            *bool `json:"github_enabled"`
		GitHubAutoLinkPRsEnabled *bool `json:"github_auto_link_prs_enabled"`
	}
	if err := json.Unmarshal(ws.Settings, &s); err != nil {
		return true, err
	}
	if s.GitHubEnabled != nil && !*s.GitHubEnabled {
		return false, nil
	}
	if s.GitHubAutoLinkPRsEnabled == nil {
		return true, nil
	}
	return *s.GitHubAutoLinkPRsEnabled, nil
}

func (h *Handler) workspaceAutoLinkPRsEnabled(ctx context.Context, workspaceID pgtype.UUID) bool {
	ws, err := h.Queries.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return true
	}
	enabled, _ := autoLinkPRsEnabledForWorkspace(ws)
	return enabled
}

// issueNumberForPrefix 返回 "PREFIX-NUMBER" 标识符里编码的 issue 号；标识符
// 畸形或带的前缀不是该 workspace 的时返回 false。大小写不敏感：分支名习惯
// 小写而 issue 前缀大写。
func issueNumberForPrefix(identifier, prefix string) (int32, bool) {
	idx := strings.LastIndex(identifier, "-")
	if idx < 0 {
		return 0, false
	}
	gotPrefix, numStr := identifier[:idx], identifier[idx+1:]
	if !strings.EqualFold(gotPrefix, prefix) {
		return 0, false
	}
	n, err := strconv.Atoi(numStr)
	if err != nil {
		return 0, false
	}
	return int32(n), true
}

// lookupIssueByIdentifier 在给定 workspace 里按 "PREFIX-NUMBER" 标识符查 issue。
// 前缀匹配该 workspace 配置的前缀且号能落到真实 issue 时返回行 + true。
func (h *Handler) lookupIssueByIdentifier(ctx context.Context, workspaceID pgtype.UUID, prefix, identifier string) (db.Issue, bool) {
	number, ok := issueNumberForPrefix(identifier, prefix)
	if !ok {
		return db.Issue{}, false
	}
	issue, err := h.Queries.GetIssueByNumber(ctx, db.GetIssueByNumberParams{
		WorkspaceID: workspaceID,
		Number:      number,
	})
	if err != nil {
		return db.Issue{}, false
	}
	return issue, true
}

// advanceIssueToDone 在 PR 合并自动推进场景里把 issue 状态置为 done，并沿平台
// 的"子 issue 完成通知父 issue"路径发事件。只在本文件关闭意图闸通过后调用。
func (h *Handler) advanceIssueToDone(ctx context.Context, issue db.Issue, workspaceID string) {
	updated, err := h.Queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
		ID:          issue.ID,
		Status:      "done",
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		slog.Warn("github: advance issue to done failed", "err", err)
		return
	}

	// 走与 HTTP UpdateIssue / BatchUpdateIssues 相同的平台"父通知"路径。合并 PR
	// 是子 issue 真正到达 `done` 最常见的途径之一，跳过这里会让父 issue 在这个
	// 主要完成路径上保持沉默。notifyParentOfChildDone 会重查每个守卫（prev !=
	// done、父存在、父非终态），所以无条件调用是安全的。
	h.notifyParentOfChildDone(ctx, issue, updated)
	// 4. PR 合并是任务完成的主要路径之一，与 HTTP 更新一样开启监督完成轮次。
	h.afterIssueStatusChangeForSupervision(ctx, issue, updated)

	prefix := h.getIssuePrefix(ctx, issue.WorkspaceID)
	resp := issueToResponse(updated, prefix)
	h.fillStatusCategory(ctx, updated.WorkspaceID, &resp)
	h.publish(protocol.EventIssueUpdated, workspaceID, "system", "", map[string]any{
		"issue":          resp,
		"status_changed": true,
		"prev_status":    issue.Status,
		"creator_type":   issue.CreatorType,
		"creator_id":     uuidToString(issue.CreatorID),
		"source":         "github_pr_merged",
	})
}

// ── 辅助 ────────────────────────────────────────────────────────────────────

// parseStrictUUID 用 pgtype.UUID 的严格扫描器解析 UUID 串。
func parseStrictUUID(s string) (pgtype.UUID, error) {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		return pgtype.UUID{}, err
	}
	return u, nil
}

// coalesce 返回 a（去空格后非空时）；否则回退 fallback。
func coalesce(a, fallback string) string {
	if strings.TrimSpace(a) == "" {
		return fallback
	}
	return a
}

// strPtrOrNil 空串返回 nil，否则返回指向该串的指针。
func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	v := s
	return &v
}

func createRepositoryInstallationToken(ctx context.Context, installationID int64, contents bool) (string, *http.Client, error) {
	permissions := `{"permissions":{"metadata":"read"}}`
	if contents {
		permissions = `{"permissions":{"metadata":"read","contents":"read"}}`
	}
	appJWT, err := signGitHubAppJWT(time.Now())
	if err != nil {
		return "", nil, err
	}
	if appJWT == "" {
		return "", nil, errors.New("github App JWT credentials unavailable")
	}

	client := &http.Client{Timeout: 15 * time.Second}
	tokenEndpoint := fmt.Sprintf(
		"%s/app/installations/%d/access_tokens",
		strings.TrimRight(githubAPIBase, "/"),
		installationID,
	)
	tokenReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		tokenEndpoint,
		strings.NewReader(permissions),
	)
	if err != nil {
		return "", nil, err
	}
	setGitHubAPIHeaders(tokenReq, appJWT)
	tokenReq.Header.Set("Content-Type", "application/json")
	tokenResp, err := client.Do(tokenReq)
	if err != nil {
		return "", nil, fmt.Errorf("create installation token: %w", err)
	}
	defer tokenResp.Body.Close()
	if tokenResp.StatusCode != http.StatusCreated {
		_, _ = io.Copy(io.Discard, io.LimitReader(tokenResp.Body, githubAPIResponseLimit))
		return "", nil, fmt.Errorf("create installation token: github status %d", tokenResp.StatusCode)
	}
	var tokenBody struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(tokenResp.Body, githubAPIResponseLimit)).Decode(&tokenBody); err != nil {
		return "", nil, fmt.Errorf("decode installation token: %w", err)
	}
	if tokenBody.Token == "" {
		return "", nil, errors.New("github returned an empty installation token")
	}
	return tokenBody.Token, client, nil

}
