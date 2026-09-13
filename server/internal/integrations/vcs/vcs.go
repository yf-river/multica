// Package vcs 是基于 token 的 Git 提供方抽象层——Multica 从这些提供方镜像 PR 与 CI
// 状态：Forgejo、Gitea（Forgejo 的上游，线上格式完全一致）与 GitLab。GitHub 刻意不是
// vcs 提供方——它的 App/installation 模型与 check_suite 形态的 CI 差异太大，因此有自己
// 的 handler（server/internal/handler/github.go）。
//
// 每个提供方只贡献真正「因提供方而异」的部分：webhook 如何鉴权、它的事件/载荷形状如何
// 映射到下面的归一化 PR/CI 结构、token 如何校验。共享的存储、issue 自动关联/自动关闭
// 与广播逻辑只在 handler 层写一份，消费这些归一化类型。
package vcs

import (
	"context"
	"errors"
	"net/http"
)

// Kind 标识一个提供方。字符串值持久化在 vcs_connection.provider 上，也用作注册表键。
type Kind string

const (
	KindForgejo Kind = "forgejo"
	KindGitea   Kind = "gitea"
	KindGitLab  Kind = "gitlab"
)

// Valid 报告 k 是否为已知提供方。
func (k Kind) Valid() bool {
	switch k {
	case KindForgejo, KindGitea, KindGitLab:
		return true
	}
	return false
}

// ErrUnauthorized 由 ValidateToken 在实例拒绝该 token（HTTP 401/403）时返回。调用方把
// 它当作一种「连接期校验失败」，与传输/实例错误区分开。
var ErrUnauthorized = errors.New("vcs: token unauthorized")

// EventKind 是归一化后的 webhook 事件类别。提供方没有建模的任何东西都归到 EventOther，
// 会被确认接收但直接忽略。
type EventKind int

const (
	EventOther EventKind = iota
	EventPullRequest
	EventCIStatus
)

// PullRequestEvent 是「与提供方无关」的 PR/MR webhook 形状。State 已归一化为
// open/closed/merged/draft 之一，handler 无需再推断。GitLab 的 "merge request" 映射到
// 同一个结构体。
type PullRequestEvent struct {
	// Action 是提供方的原始动作（如 "opened"、"closed"、"merge"）。handler 只关心它
	// 是否为终态动作；见 Terminal。
	Action          string
	RepoOwner       string
	RepoName        string
	Number          int32
	Title           string
	Body            string
	State           string // open | closed | merged | draft
	HTMLURL         string
	Branch          string
	HeadSHA         string
	AuthorLogin     string
	AuthorAvatarURL string
	Additions       int32
	Deletions       int32
	ChangedFiles    int32
	MergedAt        string // RFC3339 或空
	ClosedAt        string
	CreatedAt       string
	UpdatedAt       string
}

// Terminal 报告本事件是否是该 PR 的 merge/close 终态事件——此后「close-intent」决策必须
// 冻结。各提供方对终态动作的拼写不同（Forgejo 用 "closed"/"merged"，GitLab 用
// "merge"/"close"），所以匹配集合收敛在这里而不是 handler 层。
func (e PullRequestEvent) Terminal() bool {
	switch e.Action {
	case "closed", "merged", "merge", "close":
		return true
	}
	return false
}

// CIStatusEvent 是「与提供方无关」的 commit-status / pipeline webhook 形状。State 已
// 归一化为 passed/failed/pending，聚合查询因此与提供方无关。
type CIStatusEvent struct {
	SHA         string
	Context     string // status check / pipeline 名称；允许 ""
	State       string // passed | failed | pending
	TargetURL   string
	Description string
	// UpdatedAt 是提供方自己的事件时间戳（RFC3339 或空）。它喂给 commit-status 的
	// 单调守卫，让乱序重投不能把状态回退；空表示"未知"，handler 退回到摄取时间。
	UpdatedAt string
}

// Account 是 ValidateToken 返回的最小身份信息。
type Account struct {
	Login string
}

// Provider 是「每个提供方一份」的适配器。实现是无状态、构造便宜的；注册表按 Kind 各持
// 一个实例。
type Provider interface {
	Kind() Kind
	// EventKind 从请求头把一条入站 webhook 归类。
	EventKind(h http.Header) EventKind
	// VerifySignature 用连接的存储 secret 校验原始 body。Forgejo/Gitea 用
	// HMAC-SHA256（X-Gitea-Signature）；GitLab 用明文 token 比对（X-Gitlab-Token）。
	VerifySignature(secret string, h http.Header, body []byte) bool
	// ParsePullRequest 解码一条 pull/merge request webhook body。
	ParsePullRequest(body []byte) (PullRequestEvent, error)
	// ParseCIStatus 解码一条 commit-status / pipeline webhook body。
	ParseCIStatus(body []byte) (CIStatusEvent, error)
	// ValidateToken 确认 token 对 instanceURL 有效，并返回已认证的账户。401/403 映射成
	// ErrUnauthorized。
	ValidateToken(ctx context.Context, instanceURL, token string) (Account, error)
}

// registry 把 Kind 映射到其 Provider。由各适配文件（forgejo.go、gitlab.go）的包 init
// 填充。
var registry = map[Kind]Provider{}

// register 把一个 Provider 按其 Kind 登记进注册表。
func register(p Provider) { registry[p.Kind()] = p }

// For 返回 kind 对应的 Provider；未知时返回 (nil, false)。
func For(kind string) (Provider, bool) {
	p, ok := registry[Kind(kind)]
	return p, ok
}
