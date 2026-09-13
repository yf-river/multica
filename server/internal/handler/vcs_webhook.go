// 本文件处理「通用 VCS webhook」入站：HandleVCSWebhook 接收基于 token 的各类 Git
// 提供方（GitHub / GitLab / 工蜂等，凡实现 vcs.Provider 接口的）推来的 webhook，
// 验签后按事件类型解析，并把 PR / CI 状态「镜像」进自己的表。
//
// 镜像（mirror）语义：把上游的 PR 元数据与 commit status 存成行（upsert），按议题
// 编号自动把 PR 关联到对应 issue（标题/分支/正文的关键词），并在 PR 合并/关闭时
// 重算该 issue 的 PR 关闭聚合、条件成立则推进 issue 到 done。所有下游通知走
// EventPullRequestUpdated 事件广播。处理带乱序保护（旧事件重投不能回滚新状态）。
package handler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/vcs"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ── 响应映射 ────────────────────────────────────────────────────────────────

// vcsPullRequestToResponse 把存储的 VCS PR 行映射到共享的 PR 响应结构，供单条 PR 的
// webhook 广播用（不带聚合的检查计数——前端会重新拉取该 issue 的 PR 列表拿最新计数）。
func vcsPullRequestToResponse(p db.VcsPullRequest) GitHubPullRequestResponse {
	return GitHubPullRequestResponse{
		ID:               uuidToString(p.ID),
		Provider:         p.Provider,
		WorkspaceID:      uuidToString(p.WorkspaceID),
		RepoOwner:        p.RepoOwner,
		RepoName:         p.RepoName,
		Number:           p.PrNumber,
		Title:            p.Title,
		State:            p.State,
		HtmlURL:          p.HtmlUrl,
		Branch:           textToPtr(p.Branch),
		AuthorLogin:      textToPtr(p.AuthorLogin),
		AuthorAvatarURL:  textToPtr(p.AuthorAvatarUrl),
		MergedAt:         timestampToPtr(p.MergedAt),
		ClosedAt:         timestampToPtr(p.ClosedAt),
		PRCreatedAt:      timestampToString(p.PrCreatedAt),
		PRUpdatedAt:      timestampToString(p.PrUpdatedAt),
		MergeableState:   nil,
		ChecksConclusion: nil,
		Additions:        p.Additions,
		Deletions:        p.Deletions,
		ChangedFiles:     p.ChangedFiles,
	}
}

// vcsPullRequestRowToResponse 把 issue 的 PR 列表行（带聚合的 commit-status 计数）
// 映射到共享的响应结构。
func vcsPullRequestRowToResponse(p db.ListVCSPullRequestsByIssueRow) GitHubPullRequestResponse {
	return GitHubPullRequestResponse{
		ID:               uuidToString(p.ID),
		Provider:         p.Provider,
		WorkspaceID:      uuidToString(p.WorkspaceID),
		RepoOwner:        p.RepoOwner,
		RepoName:         p.RepoName,
		Number:           p.PrNumber,
		Title:            p.Title,
		State:            p.State,
		HtmlURL:          p.HtmlUrl,
		Branch:           textToPtr(p.Branch),
		AuthorLogin:      textToPtr(p.AuthorLogin),
		AuthorAvatarURL:  textToPtr(p.AuthorAvatarUrl),
		MergedAt:         timestampToPtr(p.MergedAt),
		ClosedAt:         timestampToPtr(p.ClosedAt),
		PRCreatedAt:      timestampToString(p.PrCreatedAt),
		PRUpdatedAt:      timestampToString(p.PrUpdatedAt),
		MergeableState:   nil,
		ChecksConclusion: aggregateChecksConclusion(p.ChecksFailed, p.ChecksPassed, p.ChecksPending, p.ChecksTotal),
		ChecksTotal:      p.ChecksTotal,
		ChecksPassed:     p.ChecksPassed,
		ChecksFailed:     p.ChecksFailed,
		ChecksPending:    p.ChecksPending,
		ChecksRunning:    p.ChecksPending,
		FailedCheckNames: []string{},
		Additions:        p.Additions,
		Deletions:        p.Deletions,
		ChangedFiles:     p.ChangedFiles,
	}
}

// ── Webhook 入站 ────────────────────────────────────────────────────────────

// HandleVCSWebhook（POST /api/webhooks/vcs/{connectionId}）鉴权并接收任意基于 token
// 的 Git 提供方的 webhook。路径里的 connection id 选出 workspace、提供方与解密密钥；
// 提供方适配器处理各自的签名方案、事件头与载荷形状，把规范化事件交给下面共享的
// 镜像逻辑。
func (h *Handler) HandleVCSWebhook(w http.ResponseWriter, r *http.Request) {
	// 集成关闭时（托管云环境）本端点表现得像不存在一样——裸 404 不透任何配置信息，
	// 与下方「connection id 真不存在」的响应一致。
	if !h.isVCSAvailable() {
		writeError(w, http.StatusNotFound, "unknown connection")
		return
	}
	if !h.isVCSConfigured() {
		writeError(w, http.StatusServiceUnavailable, "vcs webhooks not configured")
		return
	}
	connUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "connectionId"), "connection id")
	if !ok {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20)) // 10 MiB 上限
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body failed")
		return
	}

	conn, err := h.Queries.GetVCSConnectionByID(r.Context(), connUUID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("vcs: lookup connection failed", "err", err)
		}
		writeError(w, http.StatusNotFound, "unknown connection")
		return
	}
	provider, ok := vcs.For(conn.Provider)
	if !ok {
		slog.Error("vcs: connection has unknown provider", "provider", conn.Provider)
		writeError(w, http.StatusInternalServerError, "unknown provider")
		return
	}

	secret, err := h.openVCSSecret(conn.WebhookSecretEncrypted)
	if err != nil {
		slog.Error("vcs: decrypt webhook secret failed", "err", err)
		writeError(w, http.StatusInternalServerError, "secret error")
		return
	}
	if !provider.VerifySignature(secret, r.Header, body) {
		writeError(w, http.StatusUnauthorized, "invalid signature")
		return
	}

	switch provider.EventKind(r.Header) {
	case vcs.EventPullRequest:
		if pr, err := provider.ParsePullRequest(body); err != nil {
			slog.Warn("vcs: bad pull_request payload", "provider", conn.Provider, "err", err)
		} else {
			h.mirrorVCSPullRequest(r.Context(), conn, pr)
		}
	case vcs.EventCIStatus:
		if st, err := provider.ParseCIStatus(body); err != nil {
			slog.Warn("vcs: bad status payload", "provider", conn.Provider, "err", err)
		} else {
			h.mirrorVCSCIStatus(r.Context(), conn, st)
		}
	default:
		// 未建模的事件照单全收地确认，避免提供方把 hook 标记为失败。
	}
	w.WriteHeader(http.StatusAccepted)
}

// mirrorVCSPullRequest 把一条 PR 事件镜像进本地表并维护与 issue 的关联。流程：upsert
// PR 行 → 乱序保护（若库中行严格更新则直接放弃）→ 从标题/正文/分支抽议题编号建立
// 关联 → 合并/关闭事件到来时重算关闭聚合、必要时推进 issue。
func (h *Handler) mirrorVCSPullRequest(ctx context.Context, conn db.VcsConnection, ev vcs.PullRequestEvent) {
	if ev.RepoOwner == "" || ev.RepoName == "" || ev.Number == 0 {
		slog.Warn("vcs: pull_request missing repo identity", "provider", conn.Provider)
		return
	}

	pr, err := h.Queries.UpsertVCSPullRequest(ctx, db.UpsertVCSPullRequestParams{
		WorkspaceID:     conn.WorkspaceID,
		ConnectionID:    conn.ID,
		Provider:        conn.Provider,
		RepoOwner:       ev.RepoOwner,
		RepoName:        ev.RepoName,
		PrNumber:        ev.Number,
		Title:           ev.Title,
		State:           ev.State,
		HtmlUrl:         ev.HTMLURL,
		Branch:          ptrToText(strPtrOrNil(ev.Branch)),
		AuthorLogin:     ptrToText(strPtrOrNil(ev.AuthorLogin)),
		AuthorAvatarUrl: ptrToText(strPtrOrNil(ev.AuthorAvatarURL)),
		MergedAt:        parseGHTime(ev.MergedAt),
		ClosedAt:        parseGHTime(ev.ClosedAt),
		PrCreatedAt:     parseGHTimeRequired(ev.CreatedAt),
		PrUpdatedAt:     parseGHTimeRequired(ev.UpdatedAt),
		Additions:       ev.Additions,
		Deletions:       ev.Deletions,
		ChangedFiles:    ev.ChangedFiles,
		HeadSha:         ev.HeadSHA,
	})
	if err != nil {
		slog.Warn("vcs: upsert pr failed", "err", err)
		return
	}

	// 关联元数据的乱序守卫。UpsertVCSPullRequest 在「过期的重投」到来时保留更新的
	// 持久行，所以 pr 可能比本次 ev 更新。下面链接写入所派生的一切——close_intent、
	// reference_only、preserveCloseIntent——都来自 ev，用一条过期事件重写链接会破坏
	// 新事件已写好的状态（例如重投的旧 "opened" 事件会把已合并 PR 的链接翻回
	// reference_only，挡住自动推进）。若持久行严格新于本次事件，说明新事件已经完成
	// 链接与发布——就此打住。（无可用时间戳的事件回退到 now()，而 now() 永远不会
	// 严格晚于已存值，所以它照常继续。）
	evUpdatedAt := parseGHTimeRequired(ev.UpdatedAt)
	if pr.PrUpdatedAt.Valid && evUpdatedAt.Valid && pr.PrUpdatedAt.Time.After(evUpdatedAt.Time) {
		return
	}

	workspaceID := uuidToString(conn.WorkspaceID)
	resp := vcsPullRequestToResponse(pr)

	// 按标题/正文/分支里的议题编号自动把 PR 关联到 issue。连上提供方即视为选择加入，
	// 所以没有额外的每-workspace 开关。issue 侧机制与 GitHub 共用。
	linkedIssueIDs := make([]string, 0)
	idents := extractIdentifiers(ev.Title, ev.Body, ev.Branch)
	closingIdents := map[string]struct{}{}
	for _, c := range extractClosingIdentifiers(ev.Title, ev.Body) {
		closingIdents[c] = struct{}{}
	}
	// qualifyingIdents 是「真正把 PR 钉到 issue」的编号：标题前缀、分支名引用、或正文
	// 里的关闭关键词。只有正文里一次顺带 mention 才命中的编号是 reference_only——
	// 它会建立链接（PR 会出现在历史里），但不进 issue 的 PR 列表、也不参与关闭聚合，
	// 于是顺手的 "Related MUL-1" 既不会看起来像在干活的 PR，也不会挡着真正的
	// Closes 兄弟 PR 推进 issue。镜像 GitHub 路径（MUL-3739）；分支名在此与 GitHub
	// 一样刻意不参与关闭关键词扫描。
	qualifyingIdents := map[string]struct{}{}
	for _, id := range extractIdentifiers(ev.Title, ev.Branch) {
		qualifyingIdents[id] = struct{}{}
	}
	for c := range closingIdents {
		qualifyingIdents[c] = struct{}{}
	}
	// 终态的 merge/close 事件到来后冻结 close_intent。
	preserveCloseIntent := !ev.Terminal() && (ev.State == "merged" || ev.State == "closed")
	prefix := h.getIssuePrefix(ctx, conn.WorkspaceID)
	reevalIssues := make([]db.Issue, 0, len(idents))
	for _, id := range idents {
		issue, ok := h.lookupIssueByIdentifier(ctx, conn.WorkspaceID, prefix, id)
		if !ok {
			continue
		}
		_, declared := closingIdents[id]
		closeIntent := declared && !preserveCloseIntent
		_, qualifies := qualifyingIdents[id]
		referenceOnly := !qualifies
		if err := h.Queries.LinkIssueToVCSPullRequest(ctx, db.LinkIssueToVCSPullRequestParams{
			IssueID:             issue.ID,
			PullRequestID:       pr.ID,
			CloseIntent:         closeIntent,
			ReferenceOnly:       referenceOnly,
			PreserveCloseIntent: preserveCloseIntent,
			LinkedByType:        strToText("system"),
			LinkedByID:          pgtype.UUID{},
		}); err != nil {
			slog.Warn("vcs: link failed", "err", err)
			continue
		}
		linkedIssueIDs = append(linkedIssueIDs, uuidToString(issue.ID))
		reevalIssues = append(reevalIssues, issue)
	}

	if ev.State == "merged" || ev.State == "closed" {
		for _, issue := range reevalIssues {
			// 自定义终态在这里同样算终态。（MUL-6243）
			if s := issuestatus.Effective(ctx, h.Queries, issue.WorkspaceID, issue.Status); s == "done" || s == "cancelled" {
				continue
			}
			counts, err := h.Queries.GetIssueCombinedPullRequestCloseAggregate(ctx, issue.ID)
			if err != nil {
				slog.Warn("vcs: count linked pr states failed", "err", err, "issue_id", uuidToString(issue.ID))
				continue
			}
			if counts.OpenCount == 0 && counts.MergedWithCloseIntentCount > 0 {
				h.advanceIssueToDone(ctx, issue, workspaceID)
			}
		}
	}

	h.publish(protocol.EventPullRequestUpdated, workspaceID, "system", "", map[string]any{
		"pull_request":     resp,
		"linked_issue_ids": linkedIssueIDs,
	})
}

// mirrorVCSCIStatus 把一条 CI 状态事件写入本地 commit status 表，并向该 commit 所属
// PR 关联的 issue 广播刷新事件。
func (h *Handler) mirrorVCSCIStatus(ctx context.Context, conn db.VcsConnection, ev vcs.CIStatusEvent) {
	if ev.SHA == "" || ev.State == "" {
		return
	}
	// 用提供方自己的事件时间戳，让 UpsertVCSCommitStatus 的单调守卫有真实可比的依据
	// ——以前在这里写 time.Now()，守卫永远为真，乱序重投就能把状态回退。仅当载荷
	// 没带时间戳时才回退到 now()。
	if err := h.Queries.UpsertVCSCommitStatus(ctx, db.UpsertVCSCommitStatusParams{
		ConnectionID: conn.ID,
		Sha:          ev.SHA,
		Context:      ev.Context,
		State:        ev.State,
		TargetUrl:    ptrToText(strPtrOrNil(ev.TargetURL)),
		Description:  ptrToText(strPtrOrNil(ev.Description)),
		UpdatedAt:    parseGHTimeRequired(ev.UpdatedAt),
	}); err != nil {
		slog.Warn("vcs: upsert commit status failed", "err", err)
		return
	}

	issueIDs, err := h.Queries.ListIssueIDsForVCSPRHead(ctx, db.ListIssueIDsForVCSPRHeadParams{
		ConnectionID: conn.ID,
		HeadSha:      ev.SHA,
	})
	if err != nil {
		slog.Warn("vcs: lookup issues for status failed", "err", err)
		return
	}
	workspaceID := uuidToString(conn.WorkspaceID)
	for _, issueID := range issueIDs {
		h.publish(protocol.EventPullRequestUpdated, workspaceID, "system", "", map[string]any{
			"issue_id": uuidToString(issueID),
		})
	}
}
