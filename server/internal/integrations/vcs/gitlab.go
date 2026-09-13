// gitlab.go：GitLab 的 Provider 适配实现。GitLab 与 Forgejo/Gitea 在每条轴上都不同：
// /api/v4 + PRIVATE-TOKEN 头、webhook 用明文 X-Gitlab-Token 比对鉴权（无 HMAC）、
// X-Gitlab-Event 头、merge request 术语、以及用 pipeline 事件表示 CI。归一化的
// PullRequestEvent/CIStatusEvent 把这些差异全部对 handler 隐藏。
package vcs

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// gitlabProvider 为 GitLab 实现 Provider。差异细节见本文件顶部导读。
type gitlabProvider struct{}

func init() { register(gitlabProvider{}) }

func (gitlabProvider) Kind() Kind { return KindGitLab }

// EventKind 按 X-Gitlab-Event 头归类事件：MR 钩子归 PR，pipeline 钩子归 CI 状态。
func (gitlabProvider) EventKind(h http.Header) EventKind {
	switch h.Get("X-Gitlab-Event") {
	case "Merge Request Hook":
		return EventPullRequest
	case "Pipeline Hook":
		return EventCIStatus
	default:
		return EventOther
	}
}

// VerifySignature 常量时间比对 X-Gitlab-Token 头与存储的 secret。GitLab 不为 webhook
// body 做 HMAC 签名；共享 token 就是全部认证，所以空的存储 secret 永远校验不过。
func (gitlabProvider) VerifySignature(secret string, h http.Header, _ []byte) bool {
	if secret == "" {
		return false
	}
	got := h.Get("X-Gitlab-Token")
	return subtle.ConstantTimeCompare([]byte(got), []byte(secret)) == 1
}

// glMergeRequestPayload 是 GitLab "Merge Request Hook" webhook 载荷的 JSON 结构。
type glMergeRequestPayload struct {
	ObjectKind string `json:"object_kind"`
	User       struct {
		Username  string `json:"username"`
		AvatarURL string `json:"avatar_url"`
	} `json:"user"`
	Project struct {
		PathWithNamespace string `json:"path_with_namespace"`
	} `json:"project"`
	ObjectAttributes struct {
		IID            int32  `json:"iid"`
		Title          string `json:"title"`
		Description    string `json:"description"`
		State          string `json:"state"` // opened|closed|merged|locked
		Action         string `json:"action"`
		SourceBranch   string `json:"source_branch"`
		URL            string `json:"url"`
		Draft          bool   `json:"draft"`
		WorkInProgress bool   `json:"work_in_progress"`
		CreatedAt      string `json:"created_at"`
		UpdatedAt      string `json:"updated_at"`
		LastCommit     struct {
			ID string `json:"id"`
		} `json:"last_commit"`
	} `json:"object_attributes"`
}

// ParsePullRequest 把 GitLab 的 MR webhook 解码成归一化 PullRequestEvent。
func (gitlabProvider) ParsePullRequest(body []byte) (PullRequestEvent, error) {
	var d glMergeRequestPayload
	if err := json.Unmarshal(body, &d); err != nil {
		return PullRequestEvent{}, err
	}
	owner, name := splitNamespace(d.Project.PathWithNamespace)
	// GitLab 的 draft 信号散在三个地方：draft 布尔、work_in_progress 布尔，或标题前缀
	// "Draft:"，三个都认。
	draft := d.ObjectAttributes.Draft || d.ObjectAttributes.WorkInProgress ||
		strings.HasPrefix(strings.ToLower(d.ObjectAttributes.Title), "draft:")
	return PullRequestEvent{
		Action:          d.ObjectAttributes.Action,
		RepoOwner:       owner,
		RepoName:        name,
		Number:          d.ObjectAttributes.IID,
		Title:           d.ObjectAttributes.Title,
		Body:            d.ObjectAttributes.Description,
		State:           normalizeGitLabMRState(d.ObjectAttributes.State, draft),
		HTMLURL:         d.ObjectAttributes.URL,
		Branch:          d.ObjectAttributes.SourceBranch,
		HeadSHA:         d.ObjectAttributes.LastCommit.ID,
		AuthorLogin:     d.User.Username,
		AuthorAvatarURL: d.User.AvatarURL,
		CreatedAt:       normalizeGitLabTime(d.ObjectAttributes.CreatedAt),
		UpdatedAt:       normalizeGitLabTime(d.ObjectAttributes.UpdatedAt),
	}, nil
}

// normalizeGitLabTime 把 GitLab webhook 的时间戳格式（"2017-09-20 08:31:45 UTC"）转成
// 流水线其余部分期望的 RFC3339（PullRequestEvent/CIStatusEvent 把时间字段文档化为
// "RFC3339 或空"，共享解析器也只认 RFC3339）。没有这层转换，每个 GitLab 事件的时间戳
// 都会解析失败、被静默替换成摄取时间，从而破坏 GitLab 的 PR-upsert 与 commit-status
// 单调守卫、扭曲 PR 列表排序。在这里归一化，把修复留在 provider 适配器内，符合本包
// 「提供方只贡献真正不同之处」的设计，而不是教共享解析器认 GitLab 方言。输出用
// RFC3339Nano 保留 GitLab 可能下发的亚秒精度，让同一墙钟秒内的两个事件在单调守卫下仍
// 能正确排序（共享的 time.RFC3339 解析器接受小数部分）。无法识别的输入返回 ""，
// handler 因此退回摄取时间。
func normalizeGitLabTime(s string) string {
	if s == "" {
		return ""
	}
	for _, layout := range []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02 15:04:05 MST",
		"2006-01-02 15:04:05 -0700",
		"2006-01-02 15:04:05.999999 MST",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format(time.RFC3339Nano)
		}
	}
	return ""
}

// normalizeGitLabMRState 把 GitLab 的 MR 状态映射到 open/closed/merged/draft。
// "locked" 是 open 的一个瞬时子状态，按 open 处理。
func normalizeGitLabMRState(state string, draft bool) string {
	switch state {
	case "merged":
		return "merged"
	case "closed":
		return "closed"
	default: // opened, locked
		if draft {
			return "draft"
		}
		return "open"
	}
}

// glPipelinePayload 是 GitLab "Pipeline Hook" webhook 载荷的 JSON 结构。
type glPipelinePayload struct {
	ObjectKind       string `json:"object_kind"`
	ObjectAttributes struct {
		SHA        string `json:"sha"`
		Status     string `json:"status"`
		URL        string `json:"url"`
		CreatedAt  string `json:"created_at"`
		FinishedAt string `json:"finished_at"`
	} `json:"object_attributes"`
}

// ParseCIStatus 把 GitLab 的 pipeline webhook 解码成归一化 CIStatusEvent。
func (gitlabProvider) ParseCIStatus(body []byte) (CIStatusEvent, error) {
	var d glPipelinePayload
	if err := json.Unmarshal(body, &d); err != nil {
		return CIStatusEvent{}, err
	}
	// 优先用 pipeline 的 finished_at（即我们要记录的那次状态跃迁）；退回 created_at。
	// 归一化成 RFC3339，让 commit-status 单调守卫有真实可比的时间戳，而非摄取时间。
	updatedAt := d.ObjectAttributes.FinishedAt
	if updatedAt == "" {
		updatedAt = d.ObjectAttributes.CreatedAt
	}
	return CIStatusEvent{
		SHA: d.ObjectAttributes.SHA,
		// GitLab 的 pipeline 按「每个 commit 一条状态」建模，而非按具名 check，所以用
		// 一个稳定的合成 context 去键唯一的状态行。该简化的已知局限（对默认分支 pipeline
		// 配置可接受；有需要再回头改）：
		//   - merge-train / merged-results pipeline 跑在一个合成 merge commit 上，其 SHA
		//     与 MR head（last_commit.id）不同，head_sha 关联不上，卡片会显示无检查。
		//   - 同一 commit 上的多条 pipeline（如一条定时 pipeline + 一条 MR pipeline）会
		//     塌缩进这同一个 context，每个 commit 最后触发的那条胜出。
		Context:   "gitlab/pipeline",
		State:     normalizeGitLabPipelineState(d.ObjectAttributes.Status),
		TargetURL: d.ObjectAttributes.URL,
		UpdatedAt: normalizeGitLabTime(updatedAt),
	}, nil
}

// normalizeGitLabPipelineState 把 pipeline 状态映射到 passed/failed/pending。
// skipped 算通过（什么都没失败）；canceled 是失败类的终态，与 GitHub 对待 cancelled
// 的方式一致。
func normalizeGitLabPipelineState(s string) string {
	switch s {
	case "success", "skipped":
		return "passed"
	case "failed", "canceled":
		return "failed"
	default: // created, waiting_for_resource, preparing, pending, running, manual, scheduled
		return "pending"
	}
}

// ValidateToken 用 GET /api/v4/user 验证 token 是否可用，并取回账户登录名。GitLab 把
// token 放在 PRIVATE-TOKEN 头里。
func (gitlabProvider) ValidateToken(ctx context.Context, instanceURL, token string) (Account, error) {
	endpoint := NormalizeInstanceURL(instanceURL) + "/api/v4/user"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Account{}, fmt.Errorf("gitlab: build request: %w", err)
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return Account{}, fmt.Errorf("gitlab: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return Account{}, ErrUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Account{}, fmt.Errorf("gitlab: GET /user: status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var u struct {
		Username string `json:"username"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return Account{}, fmt.Errorf("gitlab: decode user: %w", err)
	}
	if u.Username == "" {
		return Account{}, errors.New("gitlab: user response missing username")
	}
	return Account{Login: u.Username}, nil
}

// splitNamespace 把 GitLab 的 path_with_namespace（"group/subgroup/repo"）拆成 owner
// （"group/subgroup"）与仓库名（"repo"）。子组保留在 owner 里，保证身份唯一。
func splitNamespace(path string) (owner, name string) {
	path = strings.Trim(path, "/")
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[:i], path[i+1:]
	}
	return "", path
}
