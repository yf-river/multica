// forgejo.go：Forgejo / Gitea（其上游，线上格式完全一致）的 Provider 适配实现。
// 两者同走 /api/v1 REST 面、同一套 X-Gitea-Signature HMAC-SHA256 webhook、同样的
// pull_request / status 事件结构，因此一个结构体服务两边，仅按 Kind 分别注册，用户可见
// 的唯一差别是提供方标签。文件末尾还有 Forgejo/GitLab 共用的 HTTP 客户端与若干归一化
// 小工具。
package vcs

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// forgejoProvider 同时服务 Forgejo 与 Gitea 两个 Kind，结构体上带 kind 区分。差异细节
// 见本文件顶部导读。
type forgejoProvider struct{ kind Kind }

func init() {
	register(forgejoProvider{kind: KindForgejo})
	register(forgejoProvider{kind: KindGitea})
}

func (p forgejoProvider) Kind() Kind { return p.kind }

// EventKind 按 X-Gitea-Event 头归类事件；Forgejo 也会镜像一份 X-GitHub-Event 头，所以
// 两个头都认。
func (p forgejoProvider) EventKind(h http.Header) EventKind {
	event := h.Get("X-Gitea-Event")
	if event == "" {
		event = h.Get("X-GitHub-Event") // Gitea 也会镜像这个头
	}
	switch event {
	case "pull_request":
		return EventPullRequest
	case "status":
		return EventCIStatus
	default:
		return EventOther
	}
}

// VerifySignature 校验 X-Gitea-Signature——body 的裸 hex HMAC-SHA256（没有 "sha256="
// 前缀，那是 GitHub 的约定；这里仍顺带容忍它）。
func (p forgejoProvider) VerifySignature(secret string, h http.Header, body []byte) bool {
	// 空 key 的 HMAC 可被伪造，所以空 secret 直接拒绝（与 GitLab 校验器口径一致）。
	// 今天到不了这里——secret 恒为 32 个随机字节——但无论如何守住认证边界。
	if secret == "" {
		return false
	}
	sig := strings.TrimSpace(h.Get("X-Gitea-Signature"))
	sig = strings.TrimPrefix(sig, "sha256=")
	want, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), want)
}

// fjPullRequestPayload 是 Forgejo/Gitea pull_request webhook 载荷的 JSON 结构。
type fjPullRequestPayload struct {
	Action      string `json:"action"`
	PullRequest struct {
		Number       int32  `json:"number"`
		Title        string `json:"title"`
		Body         string `json:"body"`
		State        string `json:"state"`
		Merged       bool   `json:"merged"`
		Draft        bool   `json:"draft"`
		HTMLURL      string `json:"html_url"`
		Additions    int32  `json:"additions"`
		Deletions    int32  `json:"deletions"`
		ChangedFiles int32  `json:"changed_files"`
		MergedAt     string `json:"merged_at"`
		ClosedAt     string `json:"closed_at"`
		CreatedAt    string `json:"created_at"`
		UpdatedAt    string `json:"updated_at"`
		User         struct {
			Login     string `json:"login"`
			UserName  string `json:"username"`
			AvatarURL string `json:"avatar_url"`
		} `json:"user"`
		Head struct {
			Ref string `json:"ref"`
			Sha string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
	Repository struct {
		Name     string `json:"name"`
		FullName string `json:"full_name"`
		Owner    struct {
			Login    string `json:"login"`
			UserName string `json:"username"`
		} `json:"owner"`
	} `json:"repository"`
}

// ParsePullRequest 把 Forgejo/Gitea 的 PR webhook 解码成归一化 PullRequestEvent。
func (p forgejoProvider) ParsePullRequest(body []byte) (PullRequestEvent, error) {
	var d fjPullRequestPayload
	if err := json.Unmarshal(body, &d); err != nil {
		return PullRequestEvent{}, err
	}
	owner := coalesce(d.Repository.Owner.UserName, d.Repository.Owner.Login)
	if owner == "" {
		// owner 两个字段都缺时，从 full_name（"owner/repo"）里切出 owner。
		if i := strings.Index(d.Repository.FullName, "/"); i > 0 {
			owner = d.Repository.FullName[:i]
		}
	}
	return PullRequestEvent{
		Action:          d.Action,
		RepoOwner:       owner,
		RepoName:        d.Repository.Name,
		Number:          d.PullRequest.Number,
		Title:           d.PullRequest.Title,
		Body:            d.PullRequest.Body,
		State:           derivePRState(d.PullRequest.State, d.PullRequest.Draft, d.PullRequest.Merged),
		HTMLURL:         d.PullRequest.HTMLURL,
		Branch:          d.PullRequest.Head.Ref,
		HeadSHA:         d.PullRequest.Head.Sha,
		AuthorLogin:     coalesce(d.PullRequest.User.UserName, d.PullRequest.User.Login),
		AuthorAvatarURL: d.PullRequest.User.AvatarURL,
		Additions:       d.PullRequest.Additions,
		Deletions:       d.PullRequest.Deletions,
		ChangedFiles:    d.PullRequest.ChangedFiles,
		MergedAt:        d.PullRequest.MergedAt,
		ClosedAt:        d.PullRequest.ClosedAt,
		CreatedAt:       d.PullRequest.CreatedAt,
		UpdatedAt:       d.PullRequest.UpdatedAt,
	}, nil
}

// fjStatusPayload 是 Forgejo/Gitea commit-status webhook 载荷的 JSON 结构。
type fjStatusPayload struct {
	SHA         string `json:"sha"`
	Context     string `json:"context"`
	State       string `json:"state"`
	TargetURL   string `json:"target_url"`
	Description string `json:"description"`
	// Forgejo/Gitea 在 commit-status 对象上以 RFC3339 下发这两个时间。
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// ParseCIStatus 把 Forgejo/Gitea 的 commit-status webhook 解码成归一化 CIStatusEvent。
func (p forgejoProvider) ParseCIStatus(body []byte) (CIStatusEvent, error) {
	var d fjStatusPayload
	if err := json.Unmarshal(body, &d); err != nil {
		return CIStatusEvent{}, err
	}
	// 优先用 status 自带的 updated_at（RFC3339），让单调守卫是真实有效的；退回
	// created_at，再退到空（handler 用摄取时间）。
	updatedAt := d.UpdatedAt
	if updatedAt == "" {
		updatedAt = d.CreatedAt
	}
	return CIStatusEvent{
		SHA:         d.SHA,
		Context:     d.Context,
		State:       normalizeForgejoState(d.State),
		TargetURL:   d.TargetURL,
		Description: d.Description,
		UpdatedAt:   updatedAt,
	}, nil
}

// normalizeForgejoState 把 Forgejo/Gitea 的 commit-status 状态映射到共享的
// passed/failed/pending 词汇表。"warning" 算通过（它不阻塞），与 GitHub 把
// neutral/skipped 算作通过的做法对齐。
func normalizeForgejoState(s string) string {
	switch s {
	case "success", "warning":
		return "passed"
	case "failure", "error":
		return "failed"
	default: // pending，以及任何未知状态
		return "pending"
	}
}

// ValidateToken 用 GET /api/v1/user 验证 token 是否可用，并取回账户登录名。
func (p forgejoProvider) ValidateToken(ctx context.Context, instanceURL, token string) (Account, error) {
	endpoint := NormalizeInstanceURL(instanceURL) + "/api/v1/user"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Account{}, fmt.Errorf("forgejo: build request: %w", err)
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return Account{}, fmt.Errorf("forgejo: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		// 记录上游状态码 + body 片段，让坏 token（401）与权限不足的 token（403）可区分，
		// 又不把 secret 泄漏进 HTTP 响应。
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		slog.Warn("forgejo: token validation rejected",
			"endpoint", endpoint,
			"status", resp.StatusCode,
			"body", strings.TrimSpace(string(b)))
		return Account{}, ErrUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Account{}, fmt.Errorf("forgejo: GET /user: status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var u struct {
		Login    string `json:"login"`
		UserName string `json:"username"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return Account{}, fmt.Errorf("forgejo: decode user: %w", err)
	}
	login := coalesce(u.Login, u.UserName)
	if login == "" {
		return Account{}, errors.New("forgejo: user response missing login")
	}
	return Account{Login: login}, nil
}

// ── shared helpers ──────────────────────────────────────────────────────────

// httpClient 是 vcs 包两个提供方做 token 校验时共用的 HTTP 客户端。
var httpClient = &http.Client{Timeout: 15 * time.Second}

// NormalizeInstanceURL 去掉首尾空白与尾部斜杠，让存储的实例 URL 与派生出的 webhook URL
// 不随输入写法漂移。
func NormalizeInstanceURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

// derivePRState 把 (state, draft, merged) 收敛成归一化 PR 状态：merged 最优先，其次
// closed，再其次 draft，其余为 open。
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

// coalesce 返回第一个非空字符串——Forgejo 的载荷常同时带 login 与 username 两个字段，
// 这里统一取「先出现且非空」的那个。
func coalesce(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
