// 职责定位：本文件实现 issue 下 comment（评论）的 HTTP 处理：列表（含 roots_only / since /
// thread / tail / recent / before 游标 / summary / fold 等面向 AI 读者的投影模式）、创建、
// 编辑、删除、解决/取消解决，以及评论触发 agent 的整套路由与去重入队逻辑。

// 关键约定：评论是 agent 运行的触发源——@agent/@squad 提及、回复父评论作者、回复所属线程
// 的 agent 会话、issue 受理人回退等都会派生出「要触发谁」。触发计算必须与预览一致、与保存
// 一致，多条评论合并进同一任务时靠完成时的 reconcile 保证「每条评论都有人回应」。/note 前缀
// 的评论只留档、绝不触发。

// 内部组织：先是最新的评论响应模型与 fold/摘要等只读投影辅助，然后是 ListComments 及其
// 各模式的数据获取；接着是评论触发（triggers/targets/合并入队/兜底升级）相关的大量辅助，
// 最后是 CreateComment / UpdateComment / DeleteComment / ResolveComment / UnresolveComment。
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/logger"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// CommentResponse 是单条评论的 JSON 响应体。除基础字段外，只有对应读取模式（roots_only /
// summary / fold）才会填充扩展字段，默认形态对老客户端保持字节不变。
type CommentResponse struct {
	ID             string  `json:"id"`
	IssueID        string  `json:"issue_id"`
	AuthorType     string  `json:"author_type"`
	AuthorID       string  `json:"author_id"`
	Content        string  `json:"content"`
	Type           string  `json:"type"`
	ParentID       *string `json:"parent_id"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
	Revision       int64   `json:"revision"`
	IssueRevision  int64   `json:"issue_revision,omitempty"`
	ResolvedAt     *string `json:"resolved_at"`
	ResolvedByType *string `json:"resolved_by_type"`
	ResolvedByID   *string `json:"resolved_by_id"`
	SourceTaskID   *string `json:"source_task_id,omitempty"`
	// QuickActionID 标记这条评论由 quick action 运行产生（MUL-5465）。时间线把它渲染成折叠的
	// 单行卡片，而不是展示原始 prompt 正文。本字段不能通过该端点设置——请求里没有对应字段——
	// 这正是卡片以它为准而不是以客户端可控的 `type` 值为准的原因。
	QuickActionID *string              `json:"quick_action_id,omitempty"`
	Reactions     []ReactionResponse   `json:"reactions"`
	Attachments   []AttachmentResponse `json:"attachments"`
	// 方向性统计——只在 roots_only 路径填充、其余模式一律省略，让默认响应形态与老调用方
	// 字节一致。ReplyCount 是线程内后代评论数；LastActivityAt 是整棵子树的 MAX(created_at)。
	// 两者让 agent 不必抓取任何回复就能判断该钻进哪条线程。
	ReplyCount     *int    `json:"reply_count,omitempty"`
	LastActivityAt *string `json:"last_activity_at,omitempty"`
	// ContentTruncated 只在 summary=true 时设置：true 表示 Content 被裁到摘要预算内，
	// false 表示装得下；nil（省略）表示调用方没要求摘要投影，Content 是逐字原文。
	ContentTruncated *bool `json:"content_truncated,omitempty"`
	// 折叠投影字段——只在 fold=true 时填充，且只填在已解决线程的「根」评论上（镜像人类
	// 时间线的折叠，见 foldResolvedThreads）。其余情况两者都为 nil/省略：未解决线程下的每条
	// 评论、以及所有非根评论。ThreadResolved 标记线程讨论被折叠到结论；FoldedCount 是这条
	// 线程里从响应中裁掉的评论数（线程本来就只有根+结论时为 0）。读者可用 `comment list
	// --full` 把折叠的评论拉回来。
	ThreadResolved *bool `json:"thread_resolved,omitempty"`
	FoldedCount    *int  `json:"folded_count,omitempty"`
	// TriggerOutcomes 是这条评论里每个显式 @agent / @squad 提及的按目标结果（MUL-4525 §2）。
	// 它是增量字段，只在创建/编辑响应上填充：老客户端直接忽略。被拦截的提及（无调用权限、
	// 目标不可用、runtime 离线）在这里上报而不是被静默丢弃，客户端才能提示「评论已发，但有
	// N 个目标未被触发」。
	TriggerOutcomes []CommentTriggerOutcome `json:"trigger_outcomes,omitempty"`
	// 1. ReviewReply is the outcome of a reply addressed to the managed reviewer: who
	// actually received it and whether a run started (GONG-63). Additive field, only on
	// create/edit responses for reviewer threads.
	ReviewReply *CommentReviewReplyResponse `json:"review_reply,omitempty"`
}

// CommentTriggerOutcome 是一次显式 @agent / @squad 提及的按目标结果（MUL-4525 §2）。
// target_id 是用户提及时写的 id——agent 提及时是 agent id、squad 提及时是 SQUAD id——客户端
// 借此对应回它渲染的那条提及，而不必让服务端回显私密目标的名字/主人。reason_code 是稳定的、
// 枚举安全的准入原因。
type CommentTriggerOutcome struct {
	TargetType string             `json:"target_type"` // "agent" | "squad"
	TargetID   string             `json:"target_id"`
	Status     DispatchStatus     `json:"status"` // queued | coalesced | deferred | blocked
	ReasonCode DispatchReasonCode `json:"reason_code"`
}

// commentToResponse 把数据库行 db.Comment 转成对外 JSON 响应；reactions/attachments 传 nil
// 时归一为空数组，保证线上字段形态稳定。
func commentToResponse(c db.Comment, reactions []ReactionResponse, attachments []AttachmentResponse) CommentResponse {
	if reactions == nil {
		reactions = []ReactionResponse{}
	}
	if attachments == nil {
		attachments = []AttachmentResponse{}
	}
	return CommentResponse{
		ID:             uuidToString(c.ID),
		IssueID:        uuidToString(c.IssueID),
		AuthorType:     c.AuthorType,
		AuthorID:       uuidToString(c.AuthorID),
		Content:        c.Content,
		Type:           c.Type,
		ParentID:       uuidToPtr(c.ParentID),
		CreatedAt:      timestampToString(c.CreatedAt),
		UpdatedAt:      timestampToString(c.UpdatedAt),
		Revision:       c.Revision,
		ResolvedAt:     timestampToPtr(c.ResolvedAt),
		ResolvedByType: textToPtr(c.ResolvedByType),
		ResolvedByID:   uuidToPtr(c.ResolvedByID),
		SourceTaskID:   uuidToPtr(c.SourceTaskID),
		QuickActionID:  uuidToPtr(c.QuickActionID),
		Reactions:      reactions,
		Attachments:    attachments,
	}
}

// summaryContentRunes 是 summary=true 模式下评论内容的长度上限。200 个 rune 足够看出一条
// 评论在说什么（开头），又能把长正文大部分从 agent 的上下文预算里切掉。按 rune 而非字节计数，
// 多字节内容（如中日韩）不会在字符中间被切断。
const summaryContentRunes = 200

// summarizeContent 把内容裁到 summaryContentRunes 以内用于摘要投影。返回（可能被裁的）内容
// 以及是否被截断；被裁时补省略号提醒读者后面还有内容。
//
// 它按 rune 逐个扫描，在 (预算+1) 个 rune 处停下，而不是给整个正文分配 []rune——病态超长的
// 评论在摘要模式下只花预算那么多成本，而不是花它的完整长度。
func summarizeContent(content string) (string, bool) {
	count := 0
	for byteOffset := range content { // range over a string yields rune start offsets
		if count == summaryContentRunes {
			return content[:byteOffset] + "…", true
		}
		count++
	}
	return content, false
}

// foldStat 是 fold=true 时挂在已解决线程根上的折叠注解。FoldedCount 是从响应里裁掉的评论数。
// 见 CommentResponse.ThreadResolved / FoldedCount。
type foldStat struct {
	FoldedCount int
}

// foldResolvedThreads 把「完整线程」评论集合里每个已解决线程折叠成读者真正需要的评论，
// 镜像人类时间线的折叠（packages/views/issues/components/thread-utils.ts 里的
// deriveThreadResolution），让 agent 看到人类看到的东西：
//
//   - 未解决线程 → 全部保留、原样不动、无注解。
//   - 回复式解决 → 保留线程根 + 那条解决回复（结论），丢弃其余回复。是「根 + 结论」而不是
//     「只有结论」：结论常是引用式的（「按我们上面说的办」），没有根问题就读不懂。
//   - 根式解决 → 只保留根，丢弃所有回复（整场讨论是个已了结的死胡同，根就点明了话题）。
//
// 每个已解决线程的根会被注解（以根的 id 字符串为 key 的 foldStat），响应据此标记
// thread_resolved 并上报裁掉的数量；需要被裁讨论的读者用 `comment list --full` 拉回来。
//
// 解决判定与 UI 完全一致：根已解决则以根为准；否则 resolved_at 最新的那条回复就是「解决」。
// 「每线程最多一条解决」约束（ClearOtherThreadResolutions）通常让这无歧义；latest-wins 的
// 决胜规则保证即使某次旧/并发写留下了两条，投影也保持总数量正确。
//
// 调用方只能对「包含完整线程」的集合（默认列表、--recent、未加 tail 的 --thread 读）调用本
// 函数。部分线程读（--since、--tail）会在上游被拒，因为对部分线程做折叠可能把从未取到的解决
// 丢进讨论里。纵深防御：集合里缺线程根的评论被当作自己的根、原样保留，部分线程因此退化成
// 「原样」，而不是静默丢数据。
func foldResolvedThreads(comments []db.Comment) ([]db.Comment, map[string]foldStat) {
	if len(comments) == 0 {
		return comments, nil
	}

	byID := make(map[string]db.Comment, len(comments))
	for _, c := range comments {
		byID[uuidToString(c.ID)] = c
	}

	// rootOf 沿着 parent_id 向上走到本集合内的线程根。停在「父级不在集合里」的第一个评论
	//（完整线程集合里那是真根；部分集合里则是我们手里最高的祖先）。循环以 len(comments)
	// 为界，异常成环也不会挂死——主键禁止真环，但对存量数据做图遍历永远别假设不会坏。
	rootOf := func(c db.Comment) db.Comment {
		cur := c
		for i := 0; i < len(comments); i++ {
			if !cur.ParentID.Valid {
				return cur
			}
			parent, ok := byID[uuidToString(cur.ParentID)]
			if !ok {
				return cur
			}
			cur = parent
		}
		return cur
	}

	type thread struct {
		root    db.Comment
		replies []db.Comment // non-root, in input (chronological) order
	}
	threads := map[string]*thread{}
	for _, c := range comments {
		root := rootOf(c)
		rid := uuidToString(root.ID)
		th := threads[rid]
		if th == nil {
			th = &thread{root: root}
			threads[rid] = th
		}
		if uuidToString(c.ID) != rid {
			th.replies = append(th.replies, c)
		}
	}

	keep := make(map[string]bool, len(comments))
	stats := map[string]foldStat{}
	for rid, th := range threads {
		// 根已解决：只保留根。
		if th.root.ResolvedAt.Valid {
			keep[rid] = true
			stats[rid] = foldStat{FoldedCount: len(th.replies)}
			continue
		}
		// 回复式解决：最新 resolved 的回复就是结论。
		var resolution *db.Comment
		for i := range th.replies {
			r := &th.replies[i]
			if !r.ResolvedAt.Valid {
				continue
			}
			if resolution == nil || r.ResolvedAt.Time.After(resolution.ResolvedAt.Time) {
				resolution = r
			}
		}
		if resolution == nil {
			// 未解决线程：全部保留、无注解。
			keep[rid] = true
			for _, r := range th.replies {
				keep[uuidToString(r.ID)] = true
			}
			continue
		}
		keep[rid] = true
		keep[uuidToString(resolution.ID)] = true
		// 除结论外的每条回复都被折叠掉。
		stats[rid] = foldStat{FoldedCount: len(th.replies) - 1}
	}

	out := make([]db.Comment, 0, len(comments))
	for _, c := range comments {
		if keep[uuidToString(c.ID)] {
			out = append(out, c)
		}
	}
	return out, stats
}

// commentHardCap 限制每条 issue 最多返回的评论数。这是防御性安全网而不是 UX 分页窗口：
// 生产 p99 约 30 条、历史最大值约 1.1k，2000 留出约 2 倍余量，同时能防住某个用户在单条
// issue 上堆出海量行时响应失控。
const commentHardCap = 2000

// HeaderCommentsTruncated 通知浏览器与 CLI 调用方：防御性列表上限裁掉了行。它刻意与
// timeline 头分开：/comments 有若干投影（roots、单线程、since）其截断无法描述成某种时间线
// 活动类型。
const HeaderCommentsTruncated = "X-Comments-Truncated"

// commentProbeLimit 在超过上限后再多读一行，用于区分「真被截断」与「issue 恰好持有
// commentHardCap 条评论」——这区别要紧：恰好达上限的读是完整的，绝不能谎报数据丢失。
const commentProbeLimit = commentHardCap + 1

// 线程补全（completeCommentThreads）的预算。
//
// commentThreadContextBudget 限制「最新窗口之外」新增的全部上下文：先是祖先，再为任何需要
// 补根的线程补缺失的兄弟/后代。因此无论线程长什么样，响应都被限制在 commentHardCap +
// commentThreadContextBudget 条评论以内。commentThreadMaxDepth 限制每次行走的方向深度。
//
// 两个都要，因为存量数据里回复深度真的无上界：通用写路径保存的是被回复的那条确切评论，
// 链可以远比 UI 通常渲染的两层深。没有预算，补全这一步会击穿它本想守护的行数上限。
const (
	commentThreadContextBudget = 2000
	commentThreadMaxDepth      = 64
)

// ListComments 处理 GET /api/issues/:id/comments：默认按时间正序返回最新 commentHardCap 条
// 评论；若触发防御性上限，completeCommentThreads 会在有界预算内补全受影响的每条线程（或整条
// 丢弃），响应置 HeaderCommentsTruncated。可选查询参数为 agent 式读者提供有界视图，长 issue
// 不用把每条旧回复拖进上下文：
//
//   - roots_only=true —— 只返回顶层评论（parent_id IS NULL），每条带 reply_count +
//     last_activity_at，调用方可据此判断该钻进哪条线程。可与 since 组合做增量轮询新建根，
//     但与 thread/recent/tail/cursor 互斥（那些模式自带分组或分页语义）。
//
//   - summary=true —— 正交的内容投影：把每条评论内容裁到固定预算并设置 content_truncated，
//     agent 可以便宜地扫列表，再按需拉全文。可与所有模式组合。
//
//   - fold=true —— 感知解决的线程投影：把每条已解决线程折叠成「根+结论」（回复式解决）或
//     「仅根」（根式解决），复用人类时间线的折叠，agent 不必为已了结的讨论付 token。
//     已解决线程的根带 thread_resolved + folded_count；`--full`（不带 fold 参数）把被裁评论
//     拉回来。计算解决需要整条线程，因此与 since / tail / roots_only（部分线程或无回复读）
//     互斥，可与默认列表、recent、未 tail 的 thread、summary 组合。
//
//   - thread=<comment-uuid> —— 返回包含该评论的线程的根 + 所有后代。锚点可以是根或任意回复，
//     服务端用递归 CTE 向上走到根，调用方不必知道手里的 id 是不是根。
//
//   - tail=<N> —— 只能与 thread 一起用。把回复数限制在最近 N 条（按 (created_at, id)）。
//     线程根始终返回（哪怕 N=0），读者保住「这条线程在讲什么」的上下文。不带 tail 时
//     thread 返回整条线程（MUL-2421 之前的行为）。
//
//   - recent=<N> —— 返回最近 N 条最活跃的线程（每条线程含根 + 全部后代）。线程活跃度取整棵
//     子树的 MAX(created_at)，因此「旧但刚被回复」的线程排在「活跃但安静」的前面。刻意不暴露
//     按行数的「最新 N 条评论」——它会浮出无关的线程尾巴、藏起相关历史（#2340）。
//
//   - before=<RFC3339> + before-id=<uuid> —— 游标。其含义随上下文而变（保持参数面小）：
//
//   - 配 recent：是「线程游标」——(last_activity_at, root_id)，下一页返回严格更不活跃的线程。
//
//   - 配 thread + tail：是「回复游标」——(created_at, id)，下一页返回同一线程里严格更早的回复。
//
// 两者必须同时给出，游标才能在落在同一微秒时决胜。下一页游标经响应头
// X-Multica-Next-Before / X-Multica-Next-Before-Id 发出。
//
// 组合规则（刻意收紧——Elon 曾警示矩阵风险）：
//
//   - roots_only 与 thread/recent/tail/before-before-id 互斥，可组合 since。
//   - thread 与 recent 互斥。
//   - thread + before/before-id 必须配 tail，否则拒绝。
//   - tail 必须配 thread。
//   - thread 可与 since 组合，since 过滤在 tail/游标裁切之后应用：线程根仍发出、过期行被滤掉。
//   - recent 可与 before/before-id（翻更老的线程）及 since（窗口内的近期活动）组合。
//
// 响应体恒为时间正序（旧→新）：recent 模式下最不活跃的线程排头、最新鲜的线程在尾部，
// 靠近 agent prompt 里的「现在」。
func (h *Handler) ListComments(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}

	q := r.URL.Query()

	var sinceTime pgtype.Timestamptz
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			// 回退到 RFC3339，与原始 CLI 保持向后兼容。
			t, err = time.Parse(time.RFC3339, v)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid since parameter; expected RFC3339 format")
				return
			}
		}
		sinceTime = pgtype.Timestamptz{Time: t, Valid: true}
	}

	threadStr := q.Get("thread")
	recentStr := q.Get("recent")
	tailStr := q.Get("tail")
	beforeTimeStr := q.Get("before")
	beforeIDStr := q.Get("before_id")
	if beforeIDStr == "" {
		// 接受连字符别名以对齐 CLI flag 约定。
		beforeIDStr = q.Get("before-id")
	}

	rootsOnlyStr := q.Get("roots_only")
	if rootsOnlyStr == "" {
		// 接受连字符别名以对齐 CLI flag 约定。
		rootsOnlyStr = q.Get("roots-only")
	}

	rootsOnly := false
	if rootsOnlyStr != "" {
		switch rootsOnlyStr {
		case "true":
			rootsOnly = true
		case "false":
		default:
			writeError(w, http.StatusBadRequest, "invalid roots_only parameter; expected boolean")
			return
		}
	}

	// summary=true 是正交的内容投影：把每条评论内容裁到固定预算，agent 不拉全文就能扫列表。
	// 它刻意不与任何模式互斥——默认列表、since、thread、recent、roots_only 都能组合。
	summary := false
	if summaryStr := q.Get("summary"); summaryStr != "" {
		switch summaryStr {
		case "true":
			summary = true
		case "false":
		default:
			writeError(w, http.StatusBadRequest, "invalid summary parameter; expected boolean")
			return
		}
	}

	// fold=true 是线程级投影：把结果里每条已解决线程折叠成「根+结论」（回复式解决）或
	// 「仅根」（根式解决），复用人类时间线的折叠语义，读长 issue 的 agent 不必为已了结的讨论
	// 付 token。与 summary（逐评论裁剪、可与一切组合）不同，fold 需要「完整线程」才能判定
	// 解决，所以部分线程读（since / tail）与 roots_only（无回复可折）都拒绝；可与 summary、
	// 默认列表、recent、未加 tail 的 thread 读组合。
	fold := false
	if foldStr := q.Get("fold"); foldStr != "" {
		switch foldStr {
		case "true":
			fold = true
		case "false":
		default:
			writeError(w, http.StatusBadRequest, "invalid fold parameter; expected boolean")
			return
		}
	}

	// --- 组合校验 ----------------------------------------
	if fold && sinceTime.Valid {
		writeError(w, http.StatusBadRequest, "fold and since are mutually exclusive: since returns a partial thread, and a fold over a partial thread could hide a resolution that was not fetched")
		return
	}
	if fold && tailStr != "" {
		writeError(w, http.StatusBadRequest, "fold and tail are mutually exclusive: tail returns a partial thread, which cannot be folded safely")
		return
	}
	if fold && rootsOnly {
		writeError(w, http.StatusBadRequest, "fold and roots_only are mutually exclusive: roots_only returns no replies to fold")
		return
	}
	if rootsOnly && threadStr != "" {
		writeError(w, http.StatusBadRequest, "roots_only and thread are mutually exclusive")
		return
	}
	if rootsOnly && recentStr != "" {
		writeError(w, http.StatusBadRequest, "roots_only and recent are mutually exclusive")
		return
	}
	if rootsOnly && tailStr != "" {
		writeError(w, http.StatusBadRequest, "roots_only and tail are mutually exclusive")
		return
	}
	if rootsOnly && (beforeTimeStr != "" || beforeIDStr != "") {
		writeError(w, http.StatusBadRequest, "roots_only does not support before / before_id")
		return
	}
	if threadStr != "" && recentStr != "" {
		writeError(w, http.StatusBadRequest, "thread and recent are mutually exclusive")
		return
	}
	if tailStr != "" && threadStr == "" {
		writeError(w, http.StatusBadRequest, "tail requires thread (it is a thread-scoped limit)")
		return
	}
	if (beforeTimeStr == "") != (beforeIDStr == "") {
		writeError(w, http.StatusBadRequest, "before and before_id must be set together (composite cursor)")
		return
	}
	// 游标要么配 recent 窗口（线程游标），要么配 tail 过的线程（回复游标）。裸游标会落到
	// 默认/since 路径——返回一条调用方没要的完整时间线。响亮地拒绝，让 API 面与文档化语义
	// 一致。
	if beforeTimeStr != "" && recentStr == "" && (threadStr == "" || tailStr == "") {
		writeError(w, http.StatusBadRequest, "before / before_id require recent (thread cursor) or thread + tail (reply cursor)")
		return
	}

	// --- 解析游标 / recent ----------------------------------------
	var beforeCursor pgtype.Timestamptz
	var beforeUUID pgtype.UUID
	hasCursor := false
	if beforeTimeStr != "" {
		t, err := time.Parse(time.RFC3339Nano, beforeTimeStr)
		if err != nil {
			t, err = time.Parse(time.RFC3339, beforeTimeStr)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid before parameter; expected RFC3339 format")
				return
			}
		}
		beforeCursor = pgtype.Timestamptz{Time: t, Valid: true}
		uuid, perr := util.ParseUUID(beforeIDStr)
		if perr != nil {
			writeError(w, http.StatusBadRequest, "invalid before_id parameter; expected UUID")
			return
		}
		beforeUUID = uuid
		hasCursor = true
	}

	recentN := 0
	if recentStr != "" {
		n, err := strconv.Atoi(recentStr)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "invalid recent parameter; expected positive integer")
			return
		}
		if n > commentHardCap {
			n = commentHardCap
		}
		recentN = n
	}

	// tail=0 允许（只返回根——适合「这条线程在讲什么」的查询，不拖任何回复进上下文）。
	// 负值被拒绝，否则会 round-trip 成 LIMIT -N，PostgreSQL 会当语法错误。
	threadTail := -1
	threadTailSet := false
	if tailStr != "" {
		n, err := strconv.Atoi(tailStr)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "invalid tail parameter; expected non-negative integer")
			return
		}
		if n > commentHardCap {
			n = commentHardCap
		}
		threadTail = n
		threadTailSet = true
	}

	result, err := h.fetchCommentsForList(r.Context(), fetchCommentsArgs{
		Issue:         issue,
		Since:         sinceTime,
		ThreadAnchor:  threadStr,
		ThreadTail:    threadTail,
		ThreadTailSet: threadTailSet,
		RecentN:       recentN,
		HasCursor:     hasCursor,
		BeforeAt:      beforeCursor,
		BeforeID:      beforeUUID,
		RootsOnly:     rootsOnly,
	})
	if err != nil {
		switch err {
		case errCommentThreadNotFound:
			writeError(w, http.StatusNotFound, "thread anchor not found in this issue")
			return
		case errCommentThreadBadID:
			writeError(w, http.StatusBadRequest, "invalid thread parameter; expected UUID")
			return
		default:
			writeError(w, http.StatusInternalServerError, "failed to list comments")
			return
		}
	}

	// 在任何以评论集合为 key 的逻辑（reaction/attachment 分组、响应数组）之前应用
	// 感知解决的折叠：折叠会丢评论，被丢的评论不该付 reactions/attachments 的往返、
	// 也不该出现在响应里。
	//
	// fetchCommentsForList 只在返回的线程「已知不完整」时标记 FoldUnsafe。默认截断路径是
	// 安全的：completeCommentThreads 要么补全每条受影响的线程、要么把它整条移除。然而一条
	// 超大、未加 tail 的 --thread 读有意只保留最新回复，折叠这种部分线程会凭空造出解决状态
	// 与 folded_count。
	var foldInfo map[string]foldStat
	if fold && !result.FoldUnsafe {
		result.Comments, foldInfo = foldResolvedThreads(result.Comments)
	}

	commentIDs := make([]pgtype.UUID, len(result.Comments))
	for i, c := range result.Comments {
		commentIDs[i] = c.ID
	}
	grouped := h.groupReactions(r, commentIDs)
	groupedAtt := h.groupAttachments(r, commentIDs)

	resp := make([]CommentResponse, len(result.Comments))
	for i, c := range result.Comments {
		cid := uuidToString(c.ID)
		resp[i] = commentToResponse(c, grouped[cid], groupedAtt[cid])
		// roots_only 有数据时挂上方向统计（其余模式 map 为 nil）。
		if st, ok := result.RootStats[cid]; ok {
			rc := st.ReplyCount
			resp[i].ReplyCount = &rc
			if st.LastActivityAt.Valid {
				la := timestampToString(st.LastActivityAt)
				resp[i].LastActivityAt = &la
			}
		}
		// 在已解决线程的根上挂折叠注解（其余模式 map 为 nil；按根 id 为 key，只有根评论会命中）。
		if st, ok := foldInfo[cid]; ok {
			resolved := true
			resp[i].ThreadResolved = &resolved
			fc := st.FoldedCount
			resp[i].FoldedCount = &fc
		}
		// 摘要投影最后应用，让它在任意读取模式产出的内容上统一裁剪。
		if summary {
			clipped, truncated := summarizeContent(resp[i].Content)
			resp[i].Content = clipped
			resp[i].ContentTruncated = &truncated
		}
	}

	// 当这一页很可能不是最后一页时，把下一页游标放进响应头。游标含义随上下文变化：recent 下
	// 指向页内最不活跃的线程（下一页 = 更老的线程）；thread + tail 下指向页内最早的回复
	//（下一页 = 同一线程更早的回复）。游标放头不放 JSON body，让默认扁平数组的响应形态——
	// 桌面 UI 与老调用方依赖它——保持不变。
	if result.NextBefore != "" && result.NextBeforeID != "" {
		w.Header().Set("X-Multica-Next-Before", result.NextBefore)
		w.Header().Set("X-Multica-Next-Before-Id", result.NextBeforeID)
	}
	if result.CommentsTruncated {
		w.Header().Set(HeaderCommentsTruncated, "true")
	}

	writeJSON(w, http.StatusOK, resp)
}

// fetchCommentsArgs 打包解析好的查询参数，让 fetchCommentsForList 保持可读。下面的哨兵错误
// 让调用方把 DB 层的结果翻译成正确的 HTTP 状态而不泄露 SQL 细节。
//
// ThreadTail 拆成「值 + set 标记」是因为 tail=0 是有意义的需求（只要根）。裸 int 会把
// 「用户没传 --tail」和「用户传了 --tail 0」压成同一状态，后者会悄悄降级成整条线程路径。
type fetchCommentsArgs struct {
	Issue         db.Issue
	Since         pgtype.Timestamptz
	RootsOnly     bool
	ThreadAnchor  string
	ThreadTail    int
	ThreadTailSet bool
	RecentN       int
	HasCursor     bool
	BeforeAt      pgtype.Timestamptz
	BeforeID      pgtype.UUID
}

// fetchCommentsResult 同时携带（物化好的）评论集合，以及（recent/thread 分组路径的）下一页
// 游标。没有下一页或该路径不支持游标时游标字段为空字符串。
type fetchCommentsResult struct {
	Comments     []db.Comment
	NextBefore   string
	NextBeforeID string
	// RootStats 以评论 id 字符串为 key 存放每条根的方向统计。只在 roots_only 路径填充，
	// 其它模式为 nil。
	RootStats map[string]rootStat
	// CommentsTruncated 表示防御性上限裁掉了评论。由探针读导出，绝不从最终响应长度推导。
	CommentsTruncated bool
	// FoldUnsafe 表示响应里含已知不完整的线程。仅截断并不代表这一点：completeCommentThreads
	// 通过补全或整条移除让默认最新窗口可以安全折叠。
	FoldUnsafe bool
}

// rootStat 是 roots_only 路径上附着在每条根评论上的方向元数据。见
// CommentResponse.ReplyCount / LastActivityAt。
type rootStat struct {
	ReplyCount     int
	LastActivityAt pgtype.Timestamptz
}

var (
	errCommentThreadNotFound = &commentFetchError{"thread anchor not found"}
	errCommentThreadBadID    = &commentFetchError{"invalid thread anchor id"}
)

// commentFetchError 是带消息的错误类型，用于区分「线程锚点找不到」与「锚点 id 非法」。
type commentFetchError struct{ msg string }

func (e *commentFetchError) Error() string { return e.msg }

// fetchCommentsForList 按参数执行具体评论读取：thread/tailed-thread/recent/roots_only/since/
// 默认 等路径，统一返回按时间正序的评论集合（带可选的游标与截断标记）。
func (h *Handler) fetchCommentsForList(ctx context.Context, args fetchCommentsArgs) (fetchCommentsResult, error) {
	issue := args.Issue

	// 线程作用域读。服务端把锚点解析成根（递归 CTE），所以这里不必假设两层扁平线程。
	if args.ThreadAnchor != "" {
		anchor, err := util.ParseUUID(args.ThreadAnchor)
		if err != nil {
			return fetchCommentsResult{}, errCommentThreadBadID
		}
		// Tailed 路径：分页查询，返回根 + 最近的 @reply_limit 条回复（按 (created_at, id)）。
		// 线程根始终返回，读者落在长线程上也不必把几百条回复拖进上下文。（--thread + --tail
		// 下的 --before/--before-id）回复内游标在同一线程里翻更早的回复。
		if args.ThreadTailSet {
			// 向 SQL 多要一条回复来探测「还有没有更旧的」。返回 >tail 条回复说明盘上至少还有
			// 一条更旧的；≤tail 说明这页就是线程尾部、没有更旧的可翻（此时绝不能发游标——
			// 否则下一页是只返回根的空跑）。这就是 MUL-2421 review 点名的精确边界修复。
			rows, err := h.Queries.ListThreadCommentsForIssuePaged(ctx, db.ListThreadCommentsForIssuePagedParams{
				AnchorID:    anchor,
				IssueID:     issue.ID,
				WorkspaceID: issue.WorkspaceID,
				HasCursor:   args.HasCursor,
				BeforeAt:    args.BeforeAt,
				BeforeID:    args.BeforeID,
				ReplyLimit:  int32(args.ThreadTail) + 1,
			})
			if err != nil {
				return fetchCommentsResult{}, err
			}
			if len(rows) == 0 {
				return fetchCommentsResult{}, errCommentThreadNotFound
			}
			// 把结果拆成根 + 回复（保持 ASC 顺序）。根以 parent_id IS NULL 识别、在 SQL 输出里
			// 总是出现；把它排除在游标 / tail 裁切逻辑之外，保证用户永远看得到线程上下文。
			var rootComment *db.Comment
			replies := make([]db.Comment, 0, len(rows))
			for _, r := range rows {
				c := db.Comment{
					ID:             r.ID,
					IssueID:        r.IssueID,
					AuthorType:     r.AuthorType,
					AuthorID:       r.AuthorID,
					Content:        r.Content,
					Type:           r.Type,
					CreatedAt:      r.CreatedAt,
					UpdatedAt:      r.UpdatedAt,
					ParentID:       r.ParentID,
					WorkspaceID:    r.WorkspaceID,
					ResolvedAt:     r.ResolvedAt,
					ResolvedByType: r.ResolvedByType,
					ResolvedByID:   r.ResolvedByID,
					SourceTaskID:   r.SourceTaskID,
					QuickActionID:  r.QuickActionID,
					Revision:       r.Revision,
				}
				if !r.ParentID.Valid {
					root := c
					rootComment = &root
					continue
				}
				replies = append(replies, c)
			}
			// 把探针溢出的行裁回调用方要的 tail 数。SQL 按 ASC 输出，多出的那行是最旧回复——
			// 从头部丢掉它正好把「最新 N 条」对齐用户请求。
			hasMore := len(replies) > args.ThreadTail
			if hasMore {
				replies = replies[1:]
			}
			out := make([]db.Comment, 0, len(replies)+1)
			if rootComment != nil {
				out = append(out, *rootComment)
			}
			for _, r := range replies {
				// since 在 tail/游标裁切之后丢弃过期行。根是豁免的（上面已追加）：读者设了
				// --since 想跳过已看过的回复时，如果这页只有根，他仍需要根做上下文。
				if args.Since.Valid && !r.CreatedAt.Time.After(args.Since.Time) {
					continue
				}
				out = append(out, r)
			}
			// 只有在证明了「存在更旧的回复」（hasMore）时才发回复游标。精确边界页
			//（replyCount == tail 无溢出）hasMore 为 false，游标保持空。
			//
			// 此外，当设了 since 且本页保留的最旧回复已 <= since 时抑制游标：下一页会翻比这条
			// 严格更旧的回复，那么每条更旧回复的 created_at 都严格更小——若游标目标本身都满足
			// 不了 `> since`，更旧的回复也不可能满足，继续翻页只会一路返回「仅根」的页，直到
			// agent 翻完整段 pre-`since` 历史。镜像 recent + since 路径上的 head-thread 守卫。
			// Elon 在 MUL-2421 的第二次 review 里点出。
			res := fetchCommentsResult{Comments: out}
			emitCursor := hasMore && len(replies) > 0
			if emitCursor && args.Since.Valid && !replies[0].CreatedAt.Time.After(args.Since.Time) {
				emitCursor = false
			}
			if emitCursor {
				oldest := replies[0]
				res.NextBefore = oldest.CreatedAt.Time.UTC().Format(time.RFC3339Nano)
				res.NextBeforeID = uuidToString(oldest.ID)
			}
			return res, nil
		}
		// 未 tail 的读与分页路径用同一条「最新回复」查询。用 commentHardCap 条回复探测：因为
		// 根是无条件返回的，2000 条回复足以证明整条线程超过 2000 行的响应上限。随后丢掉最旧
		// 那条回复，保留根 + 最新的 1999 条。
		rows, err := h.Queries.ListThreadCommentsForIssuePaged(ctx, db.ListThreadCommentsForIssuePagedParams{
			AnchorID:    anchor,
			IssueID:     issue.ID,
			WorkspaceID: issue.WorkspaceID,
			ReplyLimit:  commentHardCap,
		})
		if err != nil {
			return fetchCommentsResult{}, err
		}
		if len(rows) == 0 {
			return fetchCommentsResult{}, errCommentThreadNotFound
		}
		var rootComment *db.Comment
		replies := make([]db.Comment, 0, len(rows))
		for _, r := range rows {
			c := db.Comment{
				ID:             r.ID,
				IssueID:        r.IssueID,
				AuthorType:     r.AuthorType,
				AuthorID:       r.AuthorID,
				Content:        r.Content,
				Type:           r.Type,
				CreatedAt:      r.CreatedAt,
				UpdatedAt:      r.UpdatedAt,
				ParentID:       r.ParentID,
				WorkspaceID:    r.WorkspaceID,
				ResolvedAt:     r.ResolvedAt,
				ResolvedByType: r.ResolvedByType,
				ResolvedByID:   r.ResolvedByID,
				SourceTaskID:   r.SourceTaskID,
				QuickActionID:  r.QuickActionID,
				Revision:       r.Revision,
			}
			if !r.ParentID.Valid {
				root := c
				rootComment = &root
				continue
			}
			replies = append(replies, c)
		}
		truncated := len(replies) >= commentHardCap
		if truncated {
			replies = replies[1:]
		}
		out := make([]db.Comment, 0, len(replies)+1)
		if rootComment != nil && (!args.Since.Valid || rootComment.CreatedAt.Time.After(args.Since.Time)) {
			out = append(out, *rootComment)
		}
		for _, reply := range replies {
			if args.Since.Valid && !reply.CreatedAt.Time.After(args.Since.Time) {
				continue
			}
			out = append(out, reply)
		}
		return fetchCommentsResult{
			Comments:          out,
			CommentsTruncated: truncated,
			FoldUnsafe:        truncated,
		}, nil
	}

	// 线程分组的 recent 读：N 条最活跃的线程。
	if args.RecentN > 0 {
		rows, err := h.Queries.ListRecentThreadCommentsForIssue(ctx, db.ListRecentThreadCommentsForIssueParams{
			IssueID:     issue.ID,
			WorkspaceID: issue.WorkspaceID,
			HasCursor:   args.HasCursor,
			BeforeAt:    args.BeforeAt,
			BeforeID:    args.BeforeID,
			ThreadLimit: int32(args.RecentN),
		})
		if err != nil {
			return fetchCommentsResult{}, err
		}

		// SQL 已经按 (last_activity_at ASC, root_id ASC, created_at ASC, id ASC) 排好，
		// 因此最不活跃的线程在头部、最新鲜的在尾部。单趟遍历：
		//   1. 把线程元数据列剥成统一的 db.Comment（各路径形态一致）。
		//   2. 数出本页不同线程数，判断「更老的下一页」是否可能存在。
		//   3. 抓取头部线程的 (last_activity_at, root_id)——那就是下一页的游标（下一页 =
		//      严格比这条不活跃的线程）。
		comments := make([]db.Comment, 0, len(rows))
		var headRoot pgtype.UUID
		var headLast pgtype.Timestamptz
		seenRoot := map[string]struct{}{}
		for _, r := range rows {
			if !headRoot.Valid {
				headRoot = r.ThreadRootID
				headLast = r.ThreadLastActivityAt
			}
			seenRoot[uuidToString(r.ThreadRootID)] = struct{}{}
			// recent 路径上的 since 过滤：丢掉早于 `since` 的评论。在内存里做，保住查询的
			// 线程分组语义（不要先按 MAX(created_at) 排名前就预过滤行——那会把「最近活跃落在
			// 窗口内」的线程悄悄降级掉）。
			if args.Since.Valid && !r.CreatedAt.Time.After(args.Since.Time) {
				continue
			}
			comments = append(comments, db.Comment{
				ID:             r.ID,
				IssueID:        r.IssueID,
				AuthorType:     r.AuthorType,
				AuthorID:       r.AuthorID,
				Content:        r.Content,
				Type:           r.Type,
				CreatedAt:      r.CreatedAt,
				UpdatedAt:      r.UpdatedAt,
				ParentID:       r.ParentID,
				WorkspaceID:    r.WorkspaceID,
				ResolvedAt:     r.ResolvedAt,
				ResolvedByType: r.ResolvedByType,
				ResolvedByID:   r.ResolvedByID,
				SourceTaskID:   r.SourceTaskID,
				QuickActionID:  r.QuickActionID,
				Revision:       r.Revision,
			})
		}

		// 只在页满时才发游标。线程数少于请求数说明 SELECT 已经把匹配线程取尽了，没有更老
		// 的页可翻。
		//
		// 此外，当设了 since 且头部线程的 last_activity_at 已 <= since 时抑制游标：翻页按
		// 严格递减的 last_activity_at 走，更老的每一页都严格小于头部——头部自己都不满足
		// `> since`，更老的线程也不可能。以头部（而不是以 comments 是否为空）为判定条件，
		// 还能接住「本页保留着更新线程的行、但头部线程已越过 since」的混合情况。
		// Elon 在 #2787 第二次 review（MUL-2340 nit）里点出。
		out := fetchCommentsResult{Comments: comments}
		emitCursor := len(seenRoot) >= args.RecentN && headRoot.Valid && headLast.Valid
		if emitCursor && args.Since.Valid && !headLast.Time.After(args.Since.Time) {
			emitCursor = false
		}
		if emitCursor {
			out.NextBefore = headLast.Time.UTC().Format(time.RFC3339Nano)
			out.NextBeforeID = uuidToString(headRoot)
		}
		return out, nil
	}

	if args.RootsOnly {
		// 只读根，用于 issue 级定向。刻意与 thread/recent 模式分开：调用方先拿到全局顶层
		// 讨论，再按需抓特定线程的回复。每条根带 reply_count + last_activity_at，读者据此
		// 判断该钻进哪条线程。
		stats := map[string]rootStat{}
		if args.Since.Valid {
			rows, err := h.Queries.ListRootCommentsSinceForIssue(ctx, db.ListRootCommentsSinceForIssueParams{
				IssueID:     issue.ID,
				WorkspaceID: issue.WorkspaceID,
				Since:       args.Since,
				RowLimit:    commentProbeLimit,
			})
			if err != nil {
				return fetchCommentsResult{}, err
			}
			truncated := len(rows) > commentHardCap
			if truncated {
				// since 是增量流：保留游标之后的第一页，调用方才能前进且不跳过缺口。
				rows = rows[:commentHardCap]
			}
			comments := make([]db.Comment, len(rows))
			for i, r := range rows {
				comments[i] = db.Comment{
					ID: r.ID, IssueID: r.IssueID, AuthorType: r.AuthorType, AuthorID: r.AuthorID,
					Content: r.Content, Type: r.Type, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
					ParentID: r.ParentID, WorkspaceID: r.WorkspaceID, ResolvedAt: r.ResolvedAt,
					ResolvedByType: r.ResolvedByType, ResolvedByID: r.ResolvedByID,
					SourceTaskID: r.SourceTaskID, QuickActionID: r.QuickActionID, Revision: r.Revision,
				}
				stats[uuidToString(r.ID)] = rootStat{ReplyCount: int(r.ReplyCount), LastActivityAt: r.LastActivityAt}
			}
			return fetchCommentsResult{
				Comments: comments, RootStats: stats, CommentsTruncated: truncated,
			}, nil
		}

		rows, err := h.Queries.ListRootCommentsForIssue(ctx, db.ListRootCommentsForIssueParams{
			IssueID:     issue.ID,
			WorkspaceID: issue.WorkspaceID,
			RowLimit:    commentProbeLimit,
		})
		if err != nil {
			return fetchCommentsResult{}, err
		}
		truncated := len(rows) > commentHardCap
		if truncated {
			// SQL 取最新的探测窗口后按时间返回选中的根，因此溢出的那行是最旧的。
			rows = rows[1:]
		}
		comments := make([]db.Comment, len(rows))
		for i, r := range rows {
			comments[i] = db.Comment{
				ID: r.ID, IssueID: r.IssueID, AuthorType: r.AuthorType, AuthorID: r.AuthorID,
				Content: r.Content, Type: r.Type, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
				ParentID: r.ParentID, WorkspaceID: r.WorkspaceID, ResolvedAt: r.ResolvedAt,
				ResolvedByType: r.ResolvedByType, ResolvedByID: r.ResolvedByID,
				SourceTaskID: r.SourceTaskID, QuickActionID: r.QuickActionID, Revision: r.Revision,
			}
			stats[uuidToString(r.ID)] = rootStat{ReplyCount: int(r.ReplyCount), LastActivityAt: r.LastActivityAt}
		}
		return fetchCommentsResult{
			Comments: comments, RootStats: stats, CommentsTruncated: truncated,
		}, nil
	}

	// Since 读保持其时间正序的页面形态。探针让上限可见，又不跳过增量流下一页。
	if args.Since.Valid {
		comments, err := h.Queries.ListCommentsSinceForIssue(ctx, db.ListCommentsSinceForIssueParams{
			IssueID:     issue.ID,
			WorkspaceID: issue.WorkspaceID,
			CreatedAt:   args.Since,
			Limit:       commentProbeLimit,
		})
		if err != nil {
			return fetchCommentsResult{}, err
		}
		truncated := len(comments) > commentHardCap
		if truncated {
			comments = comments[:commentHardCap]
		}
		return fetchCommentsResult{Comments: comments, CommentsTruncated: truncated}, nil
	}
	comments, err := h.Queries.ListCommentsForIssue(ctx, db.ListCommentsForIssueParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Limit:       commentProbeLimit,
	})
	if err != nil {
		return fetchCommentsResult{}, err
	}
	// 探针读：恰好持有 commentHardCap 条评论的 issue 是完整的，把它说成截断会不必要地
	// 抑制下面的线程折叠。
	truncated := len(comments) > commentHardCap
	if truncated {
		comments = comments[len(comments)-commentHardCap:]
		// 上限保留「最新」的评论（MUL-5492），因此一条线程可能被拦腰截断：窗口外的旧根或
		// 兄弟、窗口内的新回复。在共享上下文预算内补全每条受影响的线程。补不齐的线程整条丢弃。
		comments, err = h.completeCommentThreads(ctx, issue.ID, issue.WorkspaceID, comments)
		if err != nil {
			return fetchCommentsResult{}, err
		}
	}
	return fetchCommentsResult{Comments: comments, CommentsTruncated: truncated}, nil
}

// completeCommentThreads 围绕「最新 N 条」评论窗口，补全可渲染的完整线程。
//
// 为什么需要它：若用「最旧 n 条」封顶，永远不会让回复成为孤儿——回复总是比父级新，时间线
// 前缀在「父级」关系下封闭。但「最新 n 条」窗口是后缀，没有这个性质——老的线程根落在窗外，
// 而针对它的新回复留在窗内（MUL-5492）。
//
// 先做父级封闭，因为孤儿回复不只是嵌套错位，它是「看不见」：时间线把顶层建成「无 parent_id
// 的 activities+comments」，回复挂在它们父级下面渲染，因此孤儿在 map 里却没有卡片渲染它——
// 正是 MUL-1847 的形状（1 根 + 29 回复，根被丢，29 条全部从 UI 消失而 API 却返回了它们）。
//
// 对人类时间线而言，只有父级封闭还不够。Web/Desktop 从收到的评论推导解决条与折叠计数，
// 一条补回根却只带最新回复的线程会被当成完整线程。够到根之后，本函数再从每个补回的根向下走，
// 补上所有缺失的兄弟/后代。若共享预算或深度上限无法证明一条受影响线程完整，就整条移除——
// 老客户端因此永远不会折叠部分数据。
//
// 两条遍历共享 commentThreadContextBudget，返回集合可证明被 bound 在 commentHardCap +
// commentThreadContextBudget 以内。后代每次为所有受影响根各查一层、一次查询完成，避免每条
// 线程一次 N+1。每个查询都带 issue + workspace 谓词与探针上限。
//
// 预算耗尽、父级行缺失、或父级属于别的 issue/workspace 时，keepRootConnected 会丢弃受影响的
// 回复；后代溢出则丢弃所有受影响线程而不是猜测哪条根是完整的。少返回新回复是调用方已经会
// 声明的保守降级；泄露别的租户评论、返回无界响应或吐部分线程都更糟。
func (h *Handler) completeCommentThreads(ctx context.Context, issueID, workspaceID pgtype.UUID, window []db.Comment) ([]db.Comment, error) {
	if len(window) == 0 {
		return window, nil
	}

	initialIDs := make(map[string]struct{}, len(window))
	byID := make(map[string]db.Comment, len(window)+commentThreadContextBudget/8)
	through := window[0]
	for _, c := range window {
		id := uuidToString(c.ID)
		initialIDs[id] = struct{}{}
		byID[id] = c
		if c.CreatedAt.Time.After(through.CreatedAt.Time) ||
			(c.CreatedAt.Time.Equal(through.CreatedAt.Time) &&
				bytes.Compare(c.ID.Bytes[:], through.ID.Bytes[:]) > 0) {
			through = c
		}
	}

	added := 0
	for depth := 0; depth < commentThreadMaxDepth; depth++ {
		// 当前集合里被引用但不在集合里的不同父级。去重让多条共享一个祖先的回复不会把祖先
		// 取两遍。
		var missing []pgtype.UUID
		seen := make(map[string]struct{})
		for _, c := range byID {
			if !c.ParentID.Valid {
				continue
			}
			pid := uuidToString(c.ParentID)
			if _, have := byID[pid]; have {
				continue
			}
			if _, dup := seen[pid]; dup {
				continue
			}
			seen[pid] = struct{}{}
			missing = append(missing, c.ParentID)
		}
		if len(missing) == 0 {
			break
		}
		if added+len(missing) > commentThreadContextBudget {
			break
		}

		// 租户隔离：另一个 issue/workspace 里的父级直接取不到，keepRootConnected 随后会剪掉
		// 指向它的回复。每一层都过滤，不只是第一层。
		rows, err := h.Queries.ListCommentsByIDsForIssue(ctx, db.ListCommentsByIDsForIssueParams{
			Ids:         missing,
			IssueID:     issueID,
			WorkspaceID: workspaceID,
		})
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			id := uuidToString(r.ID)
			if _, have := byID[id]; have {
				continue
			}
			byID[id] = r
			added++
		}
	}

	// 在祖先预算/深度内没够到同租户根的链全部移除。重建 map，让后代阶段不会意外复活断开的链。
	connected := keepRootConnected(byID)
	byID = make(map[string]db.Comment, len(connected)+commentThreadContextBudget-added)
	for _, c := range connected {
		byID[uuidToString(c.ID)] = c
	}

	// 补回的根标识着「最新窗口截断过的线程」。根原本就在后缀里的线程在常规的「父先于回复」
	// 时间戳约定下已完整，无需额外查询。
	affectedRoots := make(map[string]struct{})
	for id, c := range byID {
		if c.ParentID.Valid {
			continue
		}
		if _, wasInWindow := initialIDs[id]; !wasInWindow {
			affectedRoots[id] = struct{}{}
		}
	}

	if len(affectedRoots) > 0 {
		complete, err := h.addMissingCommentDescendants(
			ctx,
			issueID,
			workspaceID,
			byID,
			affectedRoots,
			through,
			&added,
		)
		if err != nil {
			return nil, err
		}
		if !complete {
			dropCommentThreads(byID, affectedRoots)
		}
	}

	out := make([]db.Comment, 0, len(byID))
	for _, c := range byID {
		out = append(out, c)
	}
	sortCommentsChronologically(out)
	return out, nil
}

// addMissingCommentDescendants 广度优先遍历每条受影响线程。剩余上下文预算或深度上限无法证明
// 所有受影响线程完整时返回 false；调用方随后把这些线程整条丢弃。
func (h *Handler) addMissingCommentDescendants(
	ctx context.Context,
	issueID, workspaceID pgtype.UUID,
	byID map[string]db.Comment,
	affectedRoots map[string]struct{},
	through db.Comment,
	added *int,
) (bool, error) {
	frontier := make(map[string]string, len(affectedRoots)) // comment id -> root id
	for rootID := range affectedRoots {
		frontier[rootID] = rootID
	}

	// 树遍历里每个返回的孩子都唯一。扫描预算包含窗口里已有的行加上剩余可增预算，所有后代
	// 查询合计最多检查响应上限（+1 探针）。
	scanBudget := len(byID) + commentThreadContextBudget - *added
	for depth := 0; depth <= commentThreadMaxDepth; depth++ {
		parentKeys := make([]string, 0, len(frontier))
		for id := range frontier {
			parentKeys = append(parentKeys, id)
		}
		sort.Strings(parentKeys)

		parentIDs := make([]pgtype.UUID, len(parentKeys))
		for i, id := range parentKeys {
			parentIDs[i] = byID[id].ID
		}

		rows, err := h.Queries.ListChildCommentsForParents(ctx, db.ListChildCommentsForParentsParams{
			ParentIds:   parentIDs,
			IssueID:     issueID,
			WorkspaceID: workspaceID,
			ThroughAt:   through.CreatedAt,
			ThroughID:   through.ID,
			RowLimit:    int32(scanBudget + 1),
		})
		if err != nil {
			return false, err
		}
		if len(rows) == 0 {
			return true, nil
		}
		if len(rows) > scanBudget || depth == commentThreadMaxDepth {
			return false, nil
		}
		scanBudget -= len(rows)

		next := make(map[string]string, len(rows))
		for _, row := range rows {
			parentID := uuidToString(row.ParentID)
			rootID, ok := frontier[parentID]
			if !ok {
				// 查询被约束在 frontier 的父级 id 内，所以这里是对畸形查询输出的防御性
				// 失败关闭。
				return false, nil
			}
			id := uuidToString(row.ID)
			next[id] = rootID
			if _, exists := byID[id]; exists {
				continue
			}
			if *added >= commentThreadContextBudget {
				return false, nil
			}
			byID[id] = row
			*added = *added + 1
		}
		frontier = next
	}
	return false, nil
}

// dropCommentThreads 移除所有「根在 roots 里」的评论。只在有界后代遍历无法证明受影响线程
// 完整之后使用。
func dropCommentThreads(byID map[string]db.Comment, roots map[string]struct{}) {
	for id := range byID {
		rootID, ok := commentRootID(id, byID)
		if !ok {
			delete(byID, id)
			continue
		}
		if _, drop := roots[rootID]; drop {
			delete(byID, id)
		}
	}
}

// commentRootID 沿 parent 链向上找出 id 在 byID 里的根（parent_id IS NULL）；链离开集合或
// 成环时返回 (false)。
func commentRootID(id string, byID map[string]db.Comment) (string, bool) {
	seen := make(map[string]struct{})
	for current := id; ; {
		if _, loop := seen[current]; loop {
			return "", false
		}
		seen[current] = struct{}{}

		comment, ok := byID[current]
		if !ok {
			return "", false
		}
		if !comment.ParentID.Valid {
			return current, true
		}
		current = uuidToString(comment.ParentID)
	}
}

// sortCommentsChronologically 按 (created_at, id) 时间正序排序评论，保证并列时间也有稳定顺序。
func sortCommentsChronologically(comments []db.Comment) {
	sort.Slice(comments, func(i, j int) bool {
		if !comments[i].CreatedAt.Time.Equal(comments[j].CreatedAt.Time) {
			return comments[i].CreatedAt.Time.Before(comments[j].CreatedAt.Time)
		}
		return bytes.Compare(comments[i].ID.Bytes[:], comments[j].ID.Bytes[:]) < 0
	})
}

// keepRootConnected 只保留「父链在集合内终结于根（parent_id IS NULL）」的评论。
//
// 链离开集合、成环或断头的评论都不可渲染：UI 需要一张顶层卡片来挂线程。丢弃这种评论也就
// 顺带丢弃了它的后代——它们的链经过它、因此过不了同一道检验——无需再跑一次后代遍历。
//
// 遍历以已访问集合为界，存量数据成环也会终止而不是挂死。真实环经写路径不可能产生，但遍历
// 存量数据时永远不该做这种假设。
func keepRootConnected(byID map[string]db.Comment) []db.Comment {
	connected := make(map[string]bool, len(byID))

	for id := range byID {
		if _, decided := connected[id]; decided {
			continue
		}
		var path []string
		visited := make(map[string]struct{})
		verdict := false
		for cur := id; ; {
			if v, decided := connected[cur]; decided {
				verdict = v
				break
			}
			if _, loop := visited[cur]; loop {
				break // cycle: nothing on this path reaches a root
			}
			c, present := byID[cur]
			if !present {
				break // chain leaves the set
			}
			visited[cur] = struct{}{}
			path = append(path, cur)
			if !c.ParentID.Valid {
				verdict = true // a real root, inside the set
				break
			}
			cur = uuidToString(c.ParentID)
		}
		for _, p := range path {
			connected[p] = verdict
		}
	}

	out := make([]db.Comment, 0, len(byID))
	for id, c := range byID {
		if connected[id] {
			out = append(out, c)
		}
	}
	return out
}

// CreateCommentRequest 是 POST /api/issues/:id/comments 的请求体。
type CreateCommentRequest struct {
	Content          string   `json:"content"`
	Type             string   `json:"type"`
	ParentID         *string  `json:"parent_id"`
	AttachmentIDs    []string `json:"attachment_ids"`
	SuppressAgentIDs []string `json:"suppress_agent_ids"`
}

// CommentTriggerPreviewRequest 是触发预览端点的请求体：草稿内容 + 可选的父级与正在编辑的
// 评论（编辑预览时排除其自身触发的任务，防重复触发）。
type CommentTriggerPreviewRequest struct {
	Content          string  `json:"content"`
	ParentID         *string `json:"parent_id"`
	EditingCommentID *string `json:"editing_comment_id"`
}

// CommentTriggerPreviewResponse 是触发预览的响应：会触发的 agent 列表 + 会被拦截的显式提及。
type CommentTriggerPreviewResponse struct {
	Agents []CommentTriggerAgentResponse `json:"agents"`
	// Blocked 列出按原文发出后「不会触发」的显式 @agent / @squad 提及（MUL-4525 §2）。
	// 增量字段：老客户端忽略。让编辑器在发送前就能警示，而不是等用户事后才发现静默无操作。
	Blocked []CommentTriggerOutcome `json:"blocked,omitempty"`
	// 2. ReviewReply describes where a reply addressed to the managed reviewer goes
	// instead (GONG-63). Absent for ordinary comments and threads.
	ReviewReply *CommentReviewReplyResponse `json:"review_reply,omitempty"`
}

// CommentTriggerAgentResponse 是预览列表里一个会被触发的 agent 及其触发原因。
type CommentTriggerAgentResponse struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	AvatarURL *string `json:"avatar_url,omitempty"`
	Source    string  `json:"source"`
	Reason    string  `json:"reason"`
}

// commentAgentTriggerSource 描述一次 agent 触发的来源（为什么这个 agent 会被触发）。
type commentAgentTriggerSource string

const (
	commentTriggerSourceIssueAssignee      commentAgentTriggerSource = "issue_assignee"
	commentTriggerSourceMentionAgent       commentAgentTriggerSource = "mention_agent"
	commentTriggerSourceMentionSquadLeader commentAgentTriggerSource = "mention_squad_leader"
	commentTriggerSourceThreadParent       commentAgentTriggerSource = "thread_parent"
	commentTriggerSourceConversation       commentAgentTriggerSource = "conversation_continuation"
	// 3. A reply addressed to the managed reviewer is handed to the current issue
	// owner: the reviewer only accepts server-dispatched review rounds, so routing
	// the reply to it would drop it (GONG-63).
	commentTriggerSourceReviewAssignee commentAgentTriggerSource = "review_followup_assignee"
)

// defaultCommentRoutingEscalationDelay 是评论路由的默认升级延迟（没配 settings 时），
// 到期后若主触发 agent 没回应，会把评论升级兜底给受理人。
const defaultCommentRoutingEscalationDelay = 5 * time.Minute

// commentRoutingEscalationDelay 读工作区 settings 里 comment_routing.escalation_seconds
// 自定义升级延迟；未配置/非法时回落默认值，显式 <=0 表示关闭升级。
func (h *Handler) commentRoutingEscalationDelay(ctx context.Context, workspaceID pgtype.UUID) time.Duration {
	ws, err := h.Queries.GetWorkspace(ctx, workspaceID)
	if err != nil || len(ws.Settings) == 0 {
		return defaultCommentRoutingEscalationDelay
	}

	var settings struct {
		CommentRouting struct {
			EscalationSeconds *int `json:"escalation_seconds"`
		} `json:"comment_routing"`
	}
	if err := json.Unmarshal(ws.Settings, &settings); err != nil || settings.CommentRouting.EscalationSeconds == nil {
		return defaultCommentRoutingEscalationDelay
	}
	if *settings.CommentRouting.EscalationSeconds <= 0 {
		return 0
	}
	return time.Duration(*settings.CommentRouting.EscalationSeconds) * time.Second
}

// commentEscalationFallback 是一次路由的升级兜底目标：主 agent 超时未回应时把评论升级给
// issue 的受理 agent（或受理 squad 的 leader）。
type commentEscalationFallback struct {
	Agent db.Agent
	Squad *db.Squad
}

// commentAgentTrigger 描述一次已解析的 agent 触发：触发的 agent、来源、可选 squad 上下文、
// 升级兜底，以及是否已存在待处理任务（可折叠入队）。
type commentAgentTrigger struct {
	Agent              db.Agent
	Source             commentAgentTriggerSource
	Squad              *db.Squad
	EscalationFallback *commentEscalationFallback
	AlreadyPending     bool
}

// commentTriggerComputeOptions 携带触发计算所需的调用上下文。
type commentTriggerComputeOptions struct {
	ExcludeTriggerCommentID pgtype.UUID
	// OriginatorUserID 是这次触发顶层的人类用户 id（MUL-3963）。只在 AGENT 角色时起作用——
	// canInvokeAgent 按发起人而非直接 agent 主体验证 A2A。member 自己就是发起人，member 触发
	// 时可为空。
	OriginatorUserID string

	// AutopilotDelegationAuthorityUserID 是血缘验证过的 autopilot 创建者：无归属的 autopilot
	// 派发在 autopilot 创建的 issue 上链中委派时，借它的调用权过 A2A 闸门（MUL-4857）。
	// 与 OriginatorUserID 分开解析，在受信任的请求/评论边界、从服务端信任的发言任务解析
	//（见 autopilotDelegationAuthority）；血缘无法验证时为空，闸门保持失败关闭。
	// effectiveInvokerFor 只在 OriginatorUserID 为空时才看它。仅是鉴权输入——归属/审计读
	// OriginatorUserID，永远不读它，因此入队的运行保持无归属。
	AutopilotDelegationAuthorityUserID string

	// 修改 1. TaskDelegationAuthority 是任务级委托授权继承的解析器（GONG-83）：给定目标 Agent，返回
	// 该任务范围内已验证的原授权人类，或空串。它是「用户已授权的当前任务范围」内的续接，不是
	// 通用放权，所以按目标 Agent 惰性查询——证据本身绑定 workspace + 任务范围 + 委托者 +
	// 目标。仅在 OriginatorUserID 与 AutopilotDelegationAuthorityUserID 都为空时才追问，且与
	// 两者一样只管鉴权输入，不写归因。
	TaskDelegationAuthority func(db.Agent) string
}

// 修改 2. effectiveInvokerFor 返回本次评论 A2A 调用闸门（canInvokeAgent）所依据的人类主体，按优先级：
// 解析出的顶层人类发起人；血缘验证过的 autopilot 委派授权者（MUL-4857）；以及任务级委托授权
// 继承（GONG-83，按目标 Agent 解析）。OriginatorUserID 保持不动以保归属准确；后两者只是闸门
// 专用回退。member 角色两者都是 member 本身（或回退未设置），canInvokeAgent 对 member 反正
// 忽略这个值。
func (o commentTriggerComputeOptions) effectiveInvokerFor(agent db.Agent) string {
	if o.OriginatorUserID != "" {
		return o.OriginatorUserID
	}
	if o.AutopilotDelegationAuthorityUserID != "" {
		return o.AutopilotDelegationAuthorityUserID
	}
	if o.TaskDelegationAuthority != nil {
		return o.TaskDelegationAuthority(agent)
	}
	return ""
}

// commentAgentTriggerReason 生成预览 UI 给每个触发 agent 看的人类可读原因。
func commentAgentTriggerReason(trigger commentAgentTrigger) string {
	switch trigger.Source {
	case commentTriggerSourceIssueAssignee:
		return "Current issue assignment will trigger this agent."
	case commentTriggerSourceMentionAgent:
		return "This agent was mentioned in the comment."
	case commentTriggerSourceMentionSquadLeader:
		return "A mentioned squad will trigger its leader."
	case commentTriggerSourceThreadParent:
		return "This reply will trigger the parent comment's author."
	case commentTriggerSourceConversation:
		return "This follow-up will continue the recent agent conversation."
	case commentTriggerSourceReviewAssignee:
		// 4. Say who receives it: the composer must not imply the reviewer will answer.
		return "Replies to the reviewer go to the current issue owner."
	default:
		return "This comment will trigger this agent."
	}
}

// commentAgentTriggerToResponse 把一次触发转成预览响应条目。
func (h *Handler) commentAgentTriggerToResponse(trigger commentAgentTrigger) CommentTriggerAgentResponse {
	return CommentTriggerAgentResponse{
		ID:        uuidToString(trigger.Agent.ID),
		Name:      trigger.Agent.Name,
		AvatarURL: h.resolveAvatarURLPtr(textToPtr(trigger.Agent.AvatarUrl)),
		Source:    string(trigger.Source),
		Reason:    commentAgentTriggerReason(trigger),
	}
}

// PreviewCommentTriggers 处理 GET/POST /api/issues/:id/comments/trigger-preview：对一段草稿
// 内容（可带 parent_id / editing_comment_id）做与保存完全相同的触发解析，返回「会触发的
// agent」与「被拦截的提及」，让编辑器在真正发送前展示后果。
func (h *Handler) PreviewCommentTriggers(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req CommentTriggerPreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	var editingComment *db.Comment
	var opts commentTriggerComputeOptions
	if req.EditingCommentID != nil {
		editingID, ok := parseUUIDOrBadRequest(w, *req.EditingCommentID, "editing_comment_id")
		if !ok {
			return
		}
		comment, err := h.Queries.GetCommentInWorkspace(r.Context(), db.GetCommentInWorkspaceParams{
			ID:          editingID,
			WorkspaceID: issue.WorkspaceID,
		})
		if err != nil || uuidToString(comment.IssueID) != uuidToString(issue.ID) {
			writeError(w, http.StatusBadRequest, "invalid editing comment")
			return
		}
		editingComment = &comment
		opts.ExcludeTriggerCommentID = editingID
	}

	var parentID pgtype.UUID
	if req.ParentID != nil {
		parentID, ok = parseUUIDOrBadRequest(w, *req.ParentID, "parent_id")
		if !ok {
			return
		}

		if editingComment != nil && uuidToString(parentID) != uuidToString(editingComment.ParentID) {
			writeError(w, http.StatusBadRequest, "parent_id does not match editing comment")
			return
		}
	} else if editingComment != nil && editingComment.ParentID.Valid {
		parentID = editingComment.ParentID
	}

	var parentComment *db.Comment
	if parentID.Valid {
		parent, err := h.Queries.GetComment(r.Context(), parentID)
		if err != nil || uuidToString(parent.IssueID) != uuidToString(issue.ID) {
			writeError(w, http.StatusBadRequest, "invalid parent comment")
			return
		}
		parentComment = &parent
	}

	// 与 CreateComment/UpdateComment 持久化前用的「同一入口」归一化（sanitizeNullBytes），
	// 预览才能在被 DB 剥掉字节（如 mention://agent/<uuid> 里的 NUL）后，对着将要入库和入队
	// 的确切内容算触发。否则预览里被藏起来的提及看着是惰性的，提交时却把 agent 排进队列——
	// 预览/副作用不一致（GH #5388 review）。
	content := sanitizeNullBytes(req.Content)
	if content == "" {
		writeJSON(w, http.StatusOK, CommentTriggerPreviewResponse{Agents: []CommentTriggerAgentResponse{}})
		return
	}

	actorType, actorID := h.resolveActor(r, userID, uuidToString(issue.WorkspaceID))
	opts.OriginatorUserID = h.invokeOriginatorFromRequest(r, actorType, actorID)
	opts.AutopilotDelegationAuthorityUserID = h.autopilotDelegationAuthorityFromRequest(r, issue, actorType, actorID)
	// 修改 3. 预览必须与真实派发用同一个任务级委托判定（GONG-83），否则「预览说会触发、保存后却被拒」
	// （或反之）会让人照着错的结果决定要不要发。
	opts.TaskDelegationAuthority = h.requestTaskDelegationResolver(r, scopeExistingIssue(&issue), actorType, actorID, uuidToString(issue.WorkspaceID))
	triggers, targets, reviewReply := h.computeCommentAgentTriggers(r.Context(), issue, content, parentComment, actorType, actorID, opts)
	resp := CommentTriggerPreviewResponse{
		Agents:  make([]CommentTriggerAgentResponse, 0, len(triggers)),
		Blocked: commentBlockedTargetOutcomes(targets),
		// 5. Preview and save share this resolution, so the composer's recipient
		// sentence cannot differ from what the save path actually does.
		ReviewReply: reviewReplyToResponse(reviewReply),
	}
	for _, trigger := range triggers {
		resp.Agents = append(resp.Agents, h.commentAgentTriggerToResponse(trigger))
	}
	writeJSON(w, http.StatusOK, resp)
}

// taskCoversReplyParent 判断 parentID 是不是该任务被授权回复其下的评论。评论触发的任务可以
// 回复它的触发评论，也可以回复排队期间被折进同一运行的更早评论（coalesced_comment_ids）。
// 合并的运行会在它覆盖的每条根线程里作答，所以它的回复合理地指向那些线程的评论——不只是
// 触发那条（MUL-4348 的逐线程 fan-out）。
//
// 任务自己 issue 上的任何其它父级仍被拒绝：这是对「恢复会话时 --parent 漂移」与跨线程错放
// 的防线。白名单恰好是运行被指定要回答的集合，因此它够不到任意评论。
func taskCoversReplyParent(task db.AgentTaskQueue, parentID pgtype.UUID) bool {
	if !parentID.Valid {
		return false
	}
	target := uuidToString(parentID)
	if task.TriggerCommentID.Valid && uuidToString(task.TriggerCommentID) == target {
		return true
	}
	for _, id := range task.CoalescedCommentIds {
		if id.Valid && uuidToString(id) == target {
			return true
		}
	}
	return false
}

// CreateComment 处理 POST /api/issues/:id/comments：校验后创建评论并派发 agent 触发。
// 权限：调用方须为工作区成员（中间件）；agent 也可经 X-Agent-ID 身份发评论。入库前剥离
// PostgreSQL TEXT 列拒绝的字节；type 仅接受客户端可写的种类（拒绝伪造系统生成的 type）。
// 评论创建、附件绑定、线程重开事件、评论创建事件在同一事务内提交。评论保存后若有
// @agent/@squad 提及或路由触发，逐个入队并把结果放进响应的 trigger_outcomes（拦截不使整
// 个请求失败）。
// commentIDContextKey 允许服务器内部重试复用同一评论编号，HTTP 请求不能注入此值。
type commentIDContextKey struct{}

func (h *Handler) CreateComment(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req CreateCommentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// 在空检查之前剥掉 PostgreSQL TEXT 列拒绝的字节。JSON API 可触达的是内嵌 NUL（0x00，
	// SQLSTATE 22021）——它能穿越 JSON round trip；sanitizeNullBytes 也顺带丢弃非法 UTF-8
	// 作为纵深防御。agent 写入内容（尤其经 --content-file）里混进这种字节，否则会让 INSERT
	// 以不透明的 500 失败、CLI 渲染成「服务器不可用」然后无限重试——很可能是 GH #5388 的
	// 根因。镜像 skill-import 的清理；先归一化意味着全 NUL 内容会被正确地当成空。
	req.Content = sanitizeNullBytes(req.Content)
	if req.Content == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}
	if req.Type == "" {
		req.Type = "comment"
	}
	// 就地校验而不是让 DB CHECK 拒绝：未知 type 过去会以约束违规的 500 呈现，明明是坏输入
	// 却像服务端故障。这也是对「客户端想伪造机器生成种类」的显式拒绝（MUL-5465 review）：
	// 成员不能发一条渲染成系统生成样子的评论。
	if !isClientAuthorableCommentType(req.Type) {
		writeError(w, http.StatusBadRequest, "invalid comment type")
		return
	}

	var parentID pgtype.UUID
	var parentComment *db.Comment
	if req.ParentID != nil {
		var parsed pgtype.UUID
		parsed, ok = parseUUIDOrBadRequest(w, *req.ParentID, "parent_id")
		if !ok {
			return
		}
		parentID = parsed
		parent, err := h.Queries.GetComment(r.Context(), parentID)
		if err != nil || uuidToString(parent.IssueID) != uuidToString(issue.ID) {
			writeError(w, http.StatusBadRequest, "invalid parent comment")
			return
		}
		parentComment = &parent
	}

	attachmentIDs, ok := parseUUIDSliceOrBadRequest(w, req.AttachmentIDs, "attachment_ids")
	if !ok {
		return
	}
	suppressAgentIDs, ok := parseUUIDSliceOrBadRequest(w, req.SuppressAgentIDs, "suppress_agent_ids")
	if !ok {
		return
	}

	// 确定作者身份：agent（经 X-Agent-ID 头）或 member。
	authorType, authorID := h.resolveActor(r, userID, uuidToString(issue.WorkspaceID))

	// sourceTaskID 在 agent 经 CLI 发评论（X-Task-ID 头）时捕获它当前执行的任务。把它盖在
	// 评论行上，保住跨 agent→agent 跳转的发起人继承链（resolveOriginatorFromTriggerComment →
	// comment.source_task_id → 父任务的 originator_user_id）。没有这个章，私密 squad leader 的
	// worker-agent——它的完成经 routeAssignedSquadLeaderFallback 唤醒 leader——会过不了
	// canInvokeAgent：worker 的任务发起人无归属、effectiveUser 解析成 ""、私密 agent 闸门拒绝
	// 这次唤醒（MUL-4015）。
	//
	// 该头不是客户端自选的：task token 用自己的行强推 X-Task-ID 与 X-Agent-ID，resolveActor
	// 除非命名任务属于命名 agent 否则拒绝 JWT 路径。因此这个章记录的一定是作者 agent 自己的运行。
	//
	// 刻意不把范围限定到该运行自己的 issue（MUL-6490 / GH #7328）：一条运行合法地在「另一条」
	// issue 上评论——平常的「agent 建 issue Y、再到那里协调」流程——过去会持久化 NULL 血缘，
	// 让那条评论唤醒的运行解析成无归属，里面每个 @提及/分配/子任务都撞
	// invocation_not_allowed，尽管同一委派在发起 issue 上刚刚成功过。传播链是单调的：它只能
	// 携带这条运行「已经在代表」的人类，且每一步都对同一个人重跑 canInvokeAgent，可准入集合
	// 始终是该人类可直接触发的子集。绝不能做的是「替换」一个人——owner 回退，或采用目标
	// issue 自己的发起人——因为那是借别人的权力；这些仍被拒绝。
	//
	// 真正需要「作者运行正在做这条 issue」的消费方，把检查放在自己规则旁边：回复父级/
	// no_action 守卫，以及 autopilotDelegationAuthority 的血缘验证（MUL-4857）。
	var sourceTaskID pgtype.UUID
	if authorType == "agent" {
		if task, ok := h.taskFromRequestHeader(r); ok {
			// 恢复会话漂移的防线：agent 在评论触发的任务里发评论、且评论发在同一条 issue 上时，
			// parent_id 必须精确等于任务的触发评论。恢复的 Claude 会话否则会把上一轮的
			// --parent UUID 带过来、悄悄放错回复位置。
			//
			// task.IssueID 范围很重要：CLI 在每次请求上都盖 X-Task-ID，合法地在另一条 issue 上
			// 评论的 agent 不能被当前任务的触发评论拦住。分配触发的任务（无 TriggerCommentID）
			// 也不受影响。
			if task.IssueID.Valid && uuidToString(task.IssueID) == uuidToString(issue.ID) {
				if task.TriggerCommentID.Valid {
					if !taskCoversReplyParent(task, parentID) {
						// 给 agent 可执行的报错（MUL-4417 / GH #5266）。两种拒绝需要不同的话术：
						// 恢复会话把上一轮的 --parent 带过来（GH #6264）并没有要求一条顶层评论，
						// 告诉它「你要求了顶层评论」会误导它去找新线程的 opt-in，而不是去纠正它
						// 已经传的 parent。
						fix := "set parent_id (--parent) to " + uuidToString(task.TriggerCommentID) + " or a coalesced comment id"
						msg := "comment-triggered tasks cannot create top-level comments; " + fix
						if parentID.Valid {
							msg = "parent_id " + uuidToString(parentID) + " is not a comment this task may reply under; " + fix
						}
						writeError(w, http.StatusConflict, msg)
						return
					}
				}
				noAction, checkErr := service.HasSquadLeaderNoActionEvaluationForTask(r.Context(), h.Queries, task)
				if checkErr != nil {
					slog.Warn("checking squad leader no_action evaluation failed", append(logger.RequestAttrs(r),
						"error", checkErr,
						"task_id", uuidToString(task.ID),
						"issue_id", issueID,
					)...)
				} else if noAction {
					writeError(w, http.StatusConflict, "squad leader recorded no_action; comments are not allowed for this task")
					return
				}
			}
			sourceTaskID = task.ID
		}
	}

	// 说明：评论内容以 Markdown 源存储。XSS 在渲染层（rehype-sanitize）与编辑器层
	//（html:false 的 @tiptap/markdown）处理。在这里跑 HTML sanitizer 会把 Markdown 语法字符
	//（>、"、&、<）实体编码、破坏源文本。见 issue #1303 / MUL-1119、MUL-1125 的讨论。

	// parent_id 存的是被回复的那条确切评论。线程级行为（例如自动取消已解决线程）另行解析根，
	// 存「回复对回复」不会破坏触发决策用的直接父级信号。
	var rootComment *db.Comment
	if parentID.Valid {
		if root, err := h.Queries.GetThreadRoot(r.Context(), db.GetThreadRootParams{
			CommentID:   parentID,
			WorkspaceID: issue.WorkspaceID,
		}); err == nil {
			rootComment = &root
		}
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		slog.Warn("begin create comment transaction failed", append(logger.RequestAttrs(r), "error", err, "issue_id", issueID)...)
		writeError(w, http.StatusInternalServerError, "failed to create comment")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	commentID := dbid.NewV7()
	if supplied, ok := r.Context().Value(commentIDContextKey{}).(pgtype.UUID); ok && supplied.Valid {
		commentID = supplied
	}
	created, err := qtx.CreateComment(r.Context(), db.CreateCommentParams{
		ID:           commentID,
		IssueID:      issue.ID,
		WorkspaceID:  issue.WorkspaceID,
		AuthorType:   authorType,
		AuthorID:     parseUUID(authorID),
		Content:      req.Content,
		Type:         req.Type,
		ParentID:     parentID,
		SourceTaskID: sourceTaskID,
	})
	if err != nil {
		slog.Warn("create comment failed", append(logger.RequestAttrs(r), "error", err, "issue_id", issueID)...)
		writeError(w, http.StatusInternalServerError, "failed to create comment")
		return
	}
	comment := created.Comment()

	// 在评论事务仍开着时绑定上传的附件并核验完整请求集合。部分认领必须把评论与它的 issue
	// activity 一起回滚。
	attachments, err := linkAttachmentsToNewComment(r.Context(), qtx, comment, attachmentIDs)
	if errors.Is(err, errCommentAttachmentsUnavailable) {
		writeError(w, http.StatusBadRequest, "one or more attachments are unavailable for this comment")
		return
	}
	if err != nil {
		slog.Warn("link comment attachments failed", append(logger.RequestAttrs(r), "error", err, "issue_id", issueID)...)
		writeError(w, http.StatusInternalServerError, "failed to create comment")
		return
	}

	resp := commentToResponse(comment, nil, h.attachmentsToResponses(attachments))
	resp.IssueRevision = created.IssueRevision
	var unresolvedEvent events.Event
	var threadReopened bool
	unresolvedEvent, threadReopened, err = service.UnresolveThreadOnReply(
		r.Context(), qtx, rootComment, uuidToString(issue.WorkspaceID), authorType, authorID,
	)
	if err != nil {
		slog.Warn("reopen resolved comment thread failed", append(logger.RequestAttrs(r), "error", err, "issue_id", issueID)...)
		writeError(w, http.StatusInternalServerError, "failed to create comment")
		return
	}
	createdEvent := buildCommentCreatedEvent(issue, resp, authorType, authorID)
	createdEvent, err = eventoutbox.Enqueue(r.Context(), qtx, createdEvent)
	if err != nil {
		slog.Warn("enqueue comment-created event failed", append(logger.RequestAttrs(r), "error", err, "issue_id", issueID)...)
		writeError(w, http.StatusInternalServerError, "failed to create comment")
		return
	}
	if threadReopened {
		unresolvedEvent, err = eventoutbox.Enqueue(r.Context(), qtx, unresolvedEvent)
		if err != nil {
			slog.Warn("enqueue comment-unresolved event failed", append(logger.RequestAttrs(r), "error", err, "issue_id", issueID)...)
			writeError(w, http.StatusInternalServerError, "failed to create comment")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		slog.Warn("commit create comment transaction failed", append(logger.RequestAttrs(r), "error", err, "issue_id", issueID)...)
		writeError(w, http.StatusInternalServerError, "failed to create comment")
		return
	}

	slog.Info("comment created", append(logger.RequestAttrs(r), "comment_id", uuidToString(comment.ID), "issue_id", issueID)...)
	h.publishEvent(createdEvent)
	if threadReopened {
		h.publishEvent(unresolvedEvent)
	}
	if authorType == "agent" {
		h.TaskService.CancelDeferredEscalationsForIssueAgent(r.Context(), issue.ID, comment.AuthorID)
	}

	originatorUserID := h.invokeOriginatorFromRequest(r, authorType, authorID)
	// MUL-4857：从与发起人解析「同一」的服务端信任 X-Task-ID 头解析 autopilot 委派授权——
	// 无归属的 autopilot 派发要在链中委派时，只有发言任务的血缘验证通过，才以 autopilot
	// 创建者身份判定。
	delegationAuthority := h.autopilotDelegationAuthorityFromRequest(r, issue, authorType, authorID)
	// 评论已保存；被拦截的提及不能让整个请求失败。把按目标结果发出来，客户端能提示「部分
	// 成功」而不是静默无操作（MUL-4525 §2）。
	dispatch := h.triggerTasksForComment(r.Context(), issue, comment, parentComment, authorType, authorID, commentTriggerComputeOptions{
		OriginatorUserID:                   originatorUserID,
		AutopilotDelegationAuthorityUserID: delegationAuthority,
		// 修改 4. 系统 / 审查反馈唤醒的运行没有人类发起人，靠这条评论所在任务范围内已发生
		// 过的委托证据续接同一授权。
		TaskDelegationAuthority: h.commentTaskDelegationResolver(r.Context(), issue, comment),
	}, suppressAgentIDs)
	resp.TriggerOutcomes = dispatch.Outcomes
	resp.ReviewReply = dispatch.ReviewReply

	writeJSON(w, http.StatusCreated, resp)
}

// clientAuthorableCommentTypes 是 POST /comments 接受的 type 集合。`status_change` 与
// `system` 是平台生成的旁白，客户端声明它们等于伪造系统输出，因此刻意不在其中。
var clientAuthorableCommentTypes = map[string]struct{}{
	"comment":         {},
	"progress_update": {},
}

// isClientAuthorableCommentType 判断 type 是否客户端可写。
func isClientAuthorableCommentType(t string) bool {
	_, ok := clientAuthorableCommentTypes[t]
	return ok
}

// noteCommentPrefix 把评论标记为仅人类可见的便签。内容第一个「空白分隔的 token」等于此前缀
// （大小写不敏感）的评论像普通评论一样存储，但绝不触发 agent。
const noteCommentPrefix = "/note"

// isNoteComment 判断内容是否用保留前缀 /note 退出了 agent 触发。前缀必须是评论第一个 token：
// "/note check expiry"、"  /NOTE"、"/note" 都命中；"/notes"、"/ note"、"see foo/note" 都不算。
func isNoteComment(content string) bool {
	trimmed := strings.TrimLeft(content, " \t\r\n")
	firstToken := trimmed
	if i := strings.IndexFunc(trimmed, unicode.IsSpace); i >= 0 {
		firstToken = trimmed[:i]
	}
	return strings.EqualFold(firstToken, noteCommentPrefix)
}

// commentTriggerDispatch 是一次评论触发的派发结果：显式 @agent / @squad 提及的按目标结果，
// 外加（仅审查员回复时）转交给负责人的实际结果。
type commentTriggerDispatch struct {
	Outcomes    []CommentTriggerOutcome
	ReviewReply *CommentReviewReplyResponse
}

// triggerTasksForComment 解析并入队评论的 agent 触发，返回显式 @agent / @squad 提及的按目标
// 结果（MUL-4525 §2）：解析阶段被拦截的提及 + 入队阶段的 queued / coalesced / deferred /
// blocked。UI 抑制的触发（用户取消勾选）在入队前移除、不产生任何结果。
// 修改 5. opts 携带调用方在受信任边界解析好的授权来源（真实发起人 / autopilot 委派 / 任务级
// 委托继承），由这里补上「本条评论自身」的排除项后原样传给触发计算，保存与延迟调和才共用
// 同一判定。
func (h *Handler) triggerTasksForComment(ctx context.Context, issue db.Issue, comment db.Comment, parentComment *db.Comment, actorType, actorID string, opts commentTriggerComputeOptions, suppressAgentIDs []pgtype.UUID) commentTriggerDispatch {
	if isNoteComment(comment.Content) {
		return commentTriggerDispatch{}
	}
	opts.ExcludeTriggerCommentID = comment.ID
	triggers, targets, reviewReply := h.computeCommentAgentTriggers(ctx, issue, comment.Content, parentComment, actorType, actorID, opts)
	triggers = filterSuppressedCommentAgentTriggers(triggers, suppressAgentIDs)
	h.noteBlockedRuntimeTargets(ctx, issue, targets)
	enqueued := h.enqueueCommentAgentTriggers(ctx, issue, comment.ID, triggers)
	return commentTriggerDispatch{
		Outcomes: commentTriggerOutcomes(targets, enqueued),
		// 6. The receipt must name the real recipient and real result, so a saved
		// reply is never mistaken for a started run (GONG-63).
		ReviewReply: reviewReplyDispatchResponse(reviewReply, enqueued),
	}
}

// noteBlockedRuntimeTargets 对每个因 runtime 不可用被拒的 agent 留一条系统评论，无论谁写的
// 触发——为何人类作者也有一条见 noteRuntimeUnusable。按 agent 去重：一条同时 @Agent 与其
// 领导的 squad 的评论只产生一条通知。
//
// 只在触发路径调用。产出这些 target 的解析器也服务编辑器预览——在那里写评论会是「打字就
// 产生副作用」。
func (h *Handler) noteBlockedRuntimeTargets(ctx context.Context, issue db.Issue, targets []commentMentionTarget) {
	var noted map[string]struct{}
	for _, t := range targets {
		if t.unusable == nil {
			continue
		}
		id := uuidToString(t.unusable.agent.ID)
		if _, done := noted[id]; done {
			continue
		}
		if noted == nil {
			noted = make(map[string]struct{}, 1)
		}
		noted[id] = struct{}{}
		h.noteRuntimeUnusable(ctx, issue, t.unusable.agent, t.unusable.verdict)
	}
}

// filterSuppressedCommentAgentTriggers 把 suppressAgentIDs 里列出的 agent 从触发列表移除
// （用户显式取消勾选，不发任务）。
func filterSuppressedCommentAgentTriggers(triggers []commentAgentTrigger, suppressAgentIDs []pgtype.UUID) []commentAgentTrigger {
	if len(triggers) == 0 || len(suppressAgentIDs) == 0 {
		return triggers
	}
	suppressed := make(map[string]struct{}, len(suppressAgentIDs))
	for _, id := range suppressAgentIDs {
		if id.Valid {
			suppressed[uuidToString(id)] = struct{}{}
		}
	}
	if len(suppressed) == 0 {
		return triggers
	}
	filtered := make([]commentAgentTrigger, 0, len(triggers))
	for _, trigger := range triggers {
		if _, ok := suppressed[uuidToString(trigger.Agent.ID)]; ok {
			continue
		}
		filtered = append(filtered, trigger)
	}
	return filtered
}

// commentEnqueueResult 是「一个执行 agent 入队」的领域结果。execSquadID 是运行实际携带的
// squad（其 leader 上下文）——只为 squad-leader 执行设置——因此共享同一 leader 的另一个
// squad 可以诚实地上报为 coalesced，而不是假装它自己的 leader 上下文跑了。
type commentEnqueueResult struct {
	status      DispatchStatus
	reason      DispatchReasonCode
	execSquadID string
}

// enqueueCommentAgentTriggers 入队每个已解析的触发（已按执行 agent 去重），返回以执行 agent
// id 为 key 的结果（MUL-4525 §2）。结果之后会 fan-out 到每个解析到该 agent 的显式提及目标，
// 合并运行永不丢命名目标的结果。queued / coalesced / deferred 都是成功形态（运行已被处理、
// 无重复任务）；只有真正的入队失败是 blocked。
func (h *Handler) enqueueCommentAgentTriggers(ctx context.Context, issue db.Issue, triggerCommentID pgtype.UUID, triggers []commentAgentTrigger) map[string]commentEnqueueResult {
	var escalationDelay time.Duration
	escalationDelayLoaded := false
	getEscalationDelay := func() time.Duration {
		if !escalationDelayLoaded {
			escalationDelay = h.commentRoutingEscalationDelay(ctx, issue.WorkspaceID)
			escalationDelayLoaded = true
		}
		return escalationDelay
	}
	results := make(map[string]commentEnqueueResult, len(triggers))
	record := func(trigger commentAgentTrigger, status DispatchStatus, reason DispatchReasonCode) {
		execSquadID := ""
		if trigger.Squad != nil {
			execSquadID = uuidToString(trigger.Squad.ID)
		}
		results[uuidToString(trigger.Agent.ID)] = commentEnqueueResult{status: status, reason: reason, execSquadID: execSquadID}
	}
	for _, trigger := range triggers {
		status, reason := h.resolveCommentTriggerEnqueue(ctx, issue, trigger, triggerCommentID, getEscalationDelay)
		record(trigger, status, reason)
	}
	return results
}

// resolveCommentTriggerEnqueue 把单个触发解析成最终派发结果：尽量把评论折进已存在的待处理
// 任务而不是丢弃（MUL-4195）。它处理 (issue, agent) 对已有待处理任务的两种情形：
//
//   - 解析阶段预标记了 AlreadyPending，或
//   - 一次全新入队在竞态中输给了并发兄弟，唯一索引
//     idx_one_pending_task_per_issue_agent 拒绝了插入（service.ErrDuplicatePendingTask,
//     #5914）。
//
// 两者都意味着「现在已有一条任务」。不变量：只有在评论「可证明已附着到会处理它的运行」时才
// 返回成功形态（coalesced / deferred / queued）：
//
//   - coalesced —— 原子 head 范围合并把它折进了排队任务；
//   - queued —— 为它新建了一条任务；
//   - deferred —— 活动任务的完成 reconcile 会重放它——要么是 planned-id 写落到了
//     claim-receipt 任务上（lost-race 路径，评论可能早于任务），要么评论比该任务新、落在
//     reconcile 的 `created_at > since` 窗口内（AlreadyPending 路径，任务先于评论存在）。
//
// 推迟不是一次性的预测：若重放后来被一条盖不住该评论的任务挡住，reconcileCommentsOnCompletion
// 会把义务转交（propagateUncoveredCommentObligation），并在挡路者的状态变化间重试（#5914，
// Elon 第 8-9 轮）。唯一一个尽力形态是「不同 HEAD 的 QUEUED 挡路者」——它既盖不住评论、
// 收下又会违反 TEN-356——这种情况以 error 记录而不隐藏；收掉它需要持久化义务记录，刻意
// 不在本次范围内。
// 绝不对当前活动任务做「分类」式推迟——快照读后证明不了任何状态（第 7 轮）。尚不能解决的
// 重复会重试（以 maxAttempts 为界）；真正不收敛时返回诚实的 internal_error，绝不编造会悄悄
// 丢弃评论的 deferred。
func (h *Handler) resolveCommentTriggerEnqueue(ctx context.Context, issue db.Issue, trigger commentAgentTrigger, triggerCommentID pgtype.UUID, getEscalationDelay func() time.Duration) (DispatchStatus, DispatchReasonCode) {
	pending := trigger.AlreadyPending
	lostRace := false
	// 懒解析被 review 过的 HEAD、最多一次——常见的非待处理路径根本不需要它，因此保持在热
	// 路径外。它同时作为 head 范围合并与 planned-comment 登记的 key，与 AlreadyPending 预检
	// 的 HEAD 一致（TEN-356）。
	var headSha pgtype.Text
	headShaLoaded := false
	getHeadSha := func() pgtype.Text {
		if !headShaLoaded {
			headSha = h.TaskService.ResolveIssueReviewSHAParam(ctx, issue.ID)
			headShaLoaded = true
		}
		return headSha
	}
	// 有界重试（#5914，Elon 第 3 轮）。不变量：没有「完成的合并 / planned-id 登记 / 全新
	// 入队 / 确认的不同 head 推迟」就不返回成功形态（coalesced / deferred / queued）。
	// 无法持久解决的重复会重试；真正不收敛返回诚实的 internal_error，绝不编造会悄悄丢弃
	// 评论的 deferred。maxAttempts 是并发抖动的后盾——每次重复都跟着一次 merge/register
	// 尝试，所以只有在兄弟持续翻转状态时循环才会空转。
	const maxAttempts = 4
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if pending {
			// (a) 折进同 HEAD 的 QUEUED 任务。这是唯一「原子地」把运行（trigger / originator /
			// accountable / overlay / connected apps）重新归属到被折评论上的路径，因此排队赢家
			// 必须总走这里、绝不是裸 planned append（MUL-4302）。HEAD 限定使不同 HEAD 的任务
			// 永远不会被折（TEN-356）——合并未命中就落到下面。合并会报告它是怎么解决的：成功
			// 是 coalesced；失败关闭/合并失败是 blocked——绝不误标成成功（MUL-4525 §2）。
			if status, reason, terminal := commentMergeTerminalOutcome(
				h.mergeCommentIntoPendingTask(ctx, issue, trigger, triggerCommentID, getHeadSha()),
			); terminal {
				return status, reason
			}
			// 没有可折入的同 HEAD QUEUED 行（合并未命中）。两条路径解析不同。
			if !lostRace {
				// (b) AlreadyPending 路径：这条评论晚于它的任务到达，因此比任务新、完成 reconcile
				// 按时间戳盖得住它；MUL-4195 保持已 claim 的任务不动。推迟给活动任务；任务若已
				// 结束则全新入队。
				active, activeErr := h.hasActiveTaskForIssueAndAgent(ctx, issue.ID, trigger.Agent.ID)
				if status, reason, enqueueFresh := decidePostMergeMiss(active, activeErr); !enqueueFresh {
					return status, reason
				}
				// enqueueFresh → fall through to the enqueue below.
			} else {
				// (c) 输掉的 INSERT 竞态：输家评论可能早于赢家，完成 reconcile 的
				// `created_at > since` 窗口看不见它。在同 HEAD 的 CLAIM-RECEIPT 任务上把它登记为
				// planned（未投递）输入（dispatched/running/waiting；queued 排除——它必须走上面的
				// 原子合并），把「丢」变成有界跟进（#5914，Elon 第 2 轮）。
				registered, err := h.registerPlannedCommentForActiveTask(ctx, issue, trigger.Agent.ID, triggerCommentID, getHeadSha())
				if err != nil {
					slog.Warn("register planned lost-race comment failed",
						"issue_id", uuidToString(issue.ID), "agent_id", uuidToString(trigger.Agent.ID), "error", err)
					return DispatchBlocked, ReasonInternalError
				}
				if registered {
					return DispatchDeferred, ReasonDeferred
				}
				// (d) 没有可折入的同 HEAD QUEUED 任务（合并未命中），也没有可登记的同 HEAD
				// CLAIM-RECEIPT 任务。别试着靠「当前活动任务的分类」推迟：快照只能证明现在，
				// 而不同 HEAD 任务覆盖该评论的 reconcile 可能被快照之后、该任务完成之前出现的更新
				// 的挡路者击穿（重放撞上挡路者，完成 reconcile 丢弃失败——Elon 第 7 轮）。每个
				// 成功形态都必须有「已完成」的写背书，而不是预测。所以直接落到全新入队：若唯一槽
				// 仍被占——必然是被一个既折不进也盖不住的不同 HEAD 任务占着——插入会再次抛
				// ErrDuplicatePendingTask 并重试，收敛到诚实的非成功；若槽空，入队创建盖得住的任务。
				//（占槽者自己的完成 reconcile 在常见无挡路情况下仍会尽力重放这条评论；我们只是
				// 不承诺它。）
			}
		}
		if err := h.enqueueSingleCommentTrigger(ctx, issue, triggerCommentID, trigger, getEscalationDelay); err != nil {
			// 入队竞态输了：这条 (issue, agent) 现在有了兄弟任务。重解析为 pending，让下一次
			// 尝试把评论折进兄弟（queued）或持久登记它（dispatched），而不是丢弃（#5914）。
			if errors.Is(err, service.ErrDuplicatePendingTask) {
				pending = true
				lostRace = true
				continue
			}
			return DispatchBlocked, commentEnqueueFailureReason(err)
		}
		return DispatchQueued, ReasonQueued
	}
	// 有界重试后仍未收敛（只有在兄弟不断在脚下翻状态的持续并发抖动下才可达）：上报诚实的
	// 非成功——绝不编造 deferred/coalesced，在没有任务可证明覆盖时谎称评论已被覆盖（#5914，
	// Elon 第 3 轮）。评论行仍在；之后的触发/编辑仍可覆盖它。
	slog.Warn("comment trigger enqueue did not converge; reporting non-success",
		"issue_id", uuidToString(issue.ID), "agent_id", uuidToString(trigger.Agent.ID))
	return DispatchBlocked, ReasonInternalError
}

// commentTriggerOutcomes 把每个显式提及目标映射成最终结果（MUL-4525 §2）：解析到某个执行
// agent 的目标取该 agent 的入队状态——多条提及合并进同一运行也各自有结果；终态目标
// （blocked / 自我抑制）带自己的状态。执行 agent 没有入队结果的目标——因为编辑器抑制（未勾选）
// 了它——不产生结果，用户是主动选择退出的。
func commentTriggerOutcomes(targets []commentMentionTarget, enqueued map[string]commentEnqueueResult) []CommentTriggerOutcome {
	if len(targets) == 0 {
		return nil
	}
	outcomes := make([]CommentTriggerOutcome, 0, len(targets))
	for _, t := range targets {
		if t.ExecAgentID != "" {
			res, ok := enqueued[t.ExecAgentID]
			if !ok {
				continue
			}
			status, reason := res.status, res.reason
			// 一条 @squad 的共享 leader 跑了、但跑在「另一个」squad 的上下文里——它没有得到
			// 自己 squad 的 leader 简报注入，单条 leader 运行（每 issue+agent 一条任务）只是把
			// 它折进来而已。上报 coalesced 而不是 queued，绝不声称这个 squad 的 leader 上下文
			// 被执行过（MUL-4525，Elon 第 3 轮）。
			if t.TargetType == "squad" && res.execSquadID != "" && res.execSquadID != t.TargetID && status == DispatchQueued {
				status, reason = DispatchCoalesced, ReasonCoalesced
			}
			outcomes = append(outcomes, CommentTriggerOutcome{TargetType: t.TargetType, TargetID: t.TargetID, Status: status, ReasonCode: reason})
			continue
		}
		outcomes = append(outcomes, CommentTriggerOutcome{TargetType: t.TargetType, TargetID: t.TargetID, Status: t.Status, ReasonCode: t.ReasonCode})
	}
	return outcomes
}

// commentBlockedTargetOutcomes 是编辑器预览的投影：按原文发出后「不会触发」的显式提及
// （MUL-4525 §2）。可解析/可执行的目标出现在预览的 `agents` 列表里，因此这里只浮现终态
// blocked 目标。
func commentBlockedTargetOutcomes(targets []commentMentionTarget) []CommentTriggerOutcome {
	var blocked []CommentTriggerOutcome
	for _, t := range targets {
		if t.Status == DispatchBlocked {
			blocked = append(blocked, CommentTriggerOutcome{TargetType: t.TargetType, TargetID: t.TargetID, Status: t.Status, ReasonCode: t.ReasonCode})
		}
	}
	return blocked
}

// commentEnqueueFailureReason 把到达响应的入队错误归类（MUL-4525 §2）。准入闸门
// （canInvokeAgent / archived / runtime）在解析阶段已经跑过，因此这里的失败要么是失败关闭的
// 归属拒绝（attribution_blocked，经 errors.Is 判别），要么是罕见竞态/基础设施错误，归类为
// 未细分的 internal error，而不是泄露原始消息。
func commentEnqueueFailureReason(err error) DispatchReasonCode {
	if errors.Is(err, service.ErrAttributionFailClosed) {
		return ReasonAttributionBlocked
	}
	return ReasonInternalError
}

// hasActiveTaskForIssueAndAgent 判断 (issue, agent) 对是否有「完成时会驱动完成 reconcile」
// 的非终态任务。它返回查询错误而不是吞掉（MUL-4525，Elon 第 4 轮）：调用方出错时必须失败
// 关闭（绝不入队可能撞车的重复）且绝不能上报成功——「无法确认运行是否活动」永远不等于
// 「运行是活动的」。两个决策点见 decidePostMergeMiss / decideSuppressedLeaderOutcome。
func (h *Handler) hasActiveTaskForIssueAndAgent(ctx context.Context, issueID, agentID pgtype.UUID) (bool, error) {
	active, err := h.Queries.HasActiveTaskForIssueAndAgent(ctx, db.HasActiveTaskForIssueAndAgentParams{
		IssueID: issueID,
		AgentID: agentID,
	})
	if err != nil {
		slog.Warn("has active task for issue+agent check failed",
			"issue_id", uuidToString(issueID), "agent_id", uuidToString(agentID), "error", err)
		return false, err
	}
	return active, nil
}

// decidePostMergeMiss 决定「评论合并未命中一个原本有待处理任务的目标」后怎么办
// （MUL-4525，Elon 第 4 轮）。查询失败（activeErr != nil）时失败关闭：绝不入队全新任务——
// 有并发重复运行风险——并上报非成功的 internal_error，因为我们无法确认运行是否活动。
// 确认有活动任务则推迟给它的 reconcile；只有确认「没有」才入队全新跟进。
func decidePostMergeMiss(active bool, activeErr error) (status DispatchStatus, reason DispatchReasonCode, enqueueFresh bool) {
	switch {
	case activeErr != nil:
		return DispatchBlocked, ReasonInternalError, false
	case active:
		return DispatchDeferred, ReasonDeferred, false
	default:
		return "", "", true
	}
}

// decideSuppressedLeaderOutcome 把「自我触发被抑制的 squad leader」的活动任务检查映射成诚实
// 的结果（MUL-4525，Elon 第 4 轮）。查询失败绝不是成功——是 internal_error，不是编造的
// deferred。确认有活动运行则推迟（它的 reconcile 覆盖该评论）；否则什么都不会跑，
// 结果是 self_trigger_suppressed。
func decideSuppressedLeaderOutcome(active bool, activeErr error) (DispatchStatus, DispatchReasonCode) {
	switch {
	case activeErr != nil:
		return DispatchBlocked, ReasonInternalError
	case active:
		return DispatchDeferred, ReasonAlreadyActive
	default:
		return DispatchBlocked, ReasonSelfTriggerSuppressed
	}
}

// commentMergeResult 区分一次待处理任务合并尝试是如何收尾的，让调用方上报诚实的结果
// （MUL-4525，Elon 第 5 轮）。真合并是 coalesced；但被拒绝或失败的合并——即使我们正确地
// 失败关闭（保留原任务、不新入队重复）——也不能上报成成功形态的 coalesced。
type commentMergeResult int

const (
	// commentMergeSucceeded: 评论折进了排队任务 → coalesced。
	commentMergeSucceeded commentMergeResult = iota
	// commentMergeNoPendingTask: 已经没有可折入的排队任务（它在去重检查与现在之间被
	// claim/start 了）。调用方跑活动任务决策（推迟 vs 全新入队）。
	commentMergeNoPendingTask
	// commentMergeAttributionBlocked: 失败关闭的归属拒绝重新盖章。原任务保留、不新入队，
	// 但重新归属「没有」发生 → 结果是 attribution_blocked 而非成功。
	commentMergeAttributionBlocked
	// commentMergeError: 未知的归属/DB 错误。失败关闭（保留任务、不入队重复），但合并未完成
	// → 结果是 internal_error 而非成功。
	commentMergeError
)

// commentMergeTerminalOutcome 把自带最终结果的合并结果（除 commentMergeNoPendingTask——
// 它还需要活动任务决策——以外的一切）映射成要上报的 (status, reason)。只有
// commentMergeNoPendingTask 时 terminal=false。
func commentMergeTerminalOutcome(result commentMergeResult) (status DispatchStatus, reason DispatchReasonCode, terminal bool) {
	switch result {
	case commentMergeSucceeded:
		return DispatchCoalesced, ReasonCoalesced, true
	case commentMergeAttributionBlocked:
		return DispatchBlocked, ReasonAttributionBlocked, true
	case commentMergeError:
		return DispatchBlocked, ReasonInternalError, true
	default: // commentMergeNoPendingTask
		return "", "", false
	}
}

// mergeCommentIntoPendingTask 把新到达的评论折进 (issue, agent) 既有的 pre-claim 任务，而不是
// 丢弃它（MUL-4195）。它通过 commentMergeResult 报告「如何解决的」，调用方绝不把被拒/失败的
// 合并误标成成功（MUL-4525 §2）。这里任何路径都不入队重复：失败时原任务原样保留，评论仍会被
// 该运行读到、其指令不丢——只是重新归属/合并记账被拒绝，而这会被诚实上报。
//
// 合并时重算（MUL-4195 review 必须修 #1）：成功时把运行的 originator_user_id、
// runtime_mcp_overlay、runtime_connected_apps 重新盖章成新触发评论的发起人，并刷新
// trigger_summary——一个成员的评论因此可以安全折进另一个成员建的任务，合并运行携带最新指令
// 的发起人与匹配的 connected-app overlay。
func (h *Handler) mergeCommentIntoPendingTask(ctx context.Context, issue db.Issue, trigger commentAgentTrigger, newTriggerCommentID pgtype.UUID, headSha pgtype.Text) commentMergeResult {
	// 把合并运行原子地重新归属给新评论的人类：整份归属快照一起动，不只那几个人名列
	//（MUL-4302）。issue-assignee 反应是 comment_source；mention / thread-parent /
	// conversation 跳转是委派。
	isMention := trigger.Source != commentTriggerSourceIssueAssignee
	attr, err := h.TaskService.AttributionForMergedComment(ctx, issue.WorkspaceID, newTriggerCommentID, isMention, trigger.Agent)
	if err != nil {
		// 新评论无法重新归属。拒绝合并——让既有排队任务保留它原始（精确）的快照，而不是把它
		// 重盖成降级的 owner_fallback；绝不派重复任务。失败关闭的拒绝是明确的诚实结果
		//（attribution_blocked）；其它错误不分细类（internal_error）——两者都不是成功。
		slog.Warn("refused comment merge: attribution failed, keeping original task snapshot",
			"issue_id", uuidToString(issue.ID),
			"agent_id", uuidToString(trigger.Agent.ID),
			"new_trigger_comment_id", uuidToString(newTriggerCommentID),
			"error", err)
		if errors.Is(err, service.ErrAttributionFailClosed) {
			return commentMergeAttributionBlocked
		}
		return commentMergeError
	}
	overlay, connectedApps := h.TaskService.BuildRuntimeMCPOverlayForMerge(ctx, attr.UserID, trigger.Agent)
	row, err := h.Queries.MergeCommentIntoPendingTask(ctx, db.MergeCommentIntoPendingTaskParams{
		IssueID:                 issue.ID,
		AgentID:                 trigger.Agent.ID,
		NewTriggerCommentID:     newTriggerCommentID,
		NewOriginatorUserID:     attr.UserID,
		NewAccountableUserID:    attr.AccountableUserID,
		NewOriginatorSource:     pgtype.Text{String: attr.Source.String(), Valid: true},
		NewDelegatedFromTaskID:  attr.DelegatedFromTaskID,
		NewRuleVersionID:        attr.RuleVersionID,
		NewTriggerEvidenceKind:  pgtype.Text{String: string(attr.EvidenceKind), Valid: attr.EvidenceKind != ""},
		NewTriggerEvidenceRefID: attr.EvidenceRefID,
		NewTriggerSummary:       h.TaskService.BuildCommentTriggerSummary(ctx, issue.WorkspaceID, newTriggerCommentID),
		NewRuntimeMcpOverlay:    overlay,
		NewRuntimeConnectedApps: connectedApps,
		HeadSha:                 headSha,
	})
	if err != nil {
		if isNotFound(err) {
			// 没有可折入的 pre-claim（queued/deferred）任务。有活动任务时调用方推迟给完成
			// reconcile，没有则全新入队。
			return commentMergeNoPendingTask
		}
		// 未知错误：待处理任务很可能仍在，因此别冒险入队重复——但合并没发生，这不是成功。
		slog.Warn("merge comment into pending task failed",
			"issue_id", uuidToString(issue.ID),
			"agent_id", uuidToString(trigger.Agent.ID),
			"error", err)
		return commentMergeError
	}
	slog.Info("merged comment into pending task",
		"task_id", uuidToString(row.ID),
		"issue_id", uuidToString(issue.ID),
		"agent_id", uuidToString(trigger.Agent.ID),
		"new_trigger_comment_id", uuidToString(newTriggerCommentID),
		"coalesced_count", len(row.CoalescedCommentIds))
	return commentMergeSucceeded
}

// registerPlannedCommentForActiveTask 在 queued 合并已无法再瞄准同 HEAD 活动任务（赢家已被
// claim → dispatched/running）时，把 lost-race 评论持久登记进它的 planned（coalesced）集合。
// 同 HEAD 活动任务吸收评论时返回 true；（false, nil）表示不存在同 HEAD 活动任务（槽被不同
// HEAD 任务占着，或任务刚终结），调用方回退到活动任务决策。已投递的 id 不动，完成 reconcile
// 会把评论作为单条有界跟进重放（#5914）。见查询 doc。
func (h *Handler) registerPlannedCommentForActiveTask(ctx context.Context, issue db.Issue, agentID, commentID pgtype.UUID, headSha pgtype.Text) (bool, error) {
	row, err := h.Queries.RegisterPlannedCommentForActiveTask(ctx, db.RegisterPlannedCommentForActiveTaskParams{
		CommentID: commentID,
		IssueID:   issue.ID,
		AgentID:   agentID,
		HeadSha:   headSha,
	})
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, err
	}
	slog.Info("registered lost-race comment as planned follow-up input",
		"task_id", uuidToString(row.ID),
		"issue_id", uuidToString(issue.ID),
		"agent_id", uuidToString(agentID),
		"comment_id", uuidToString(commentID),
		"coalesced_count", len(row.CoalescedCommentIds))
	return true, nil
}

// propagateUncoveredCommentObligation 把一条「未覆盖」评论的重放义务交给现在占着 (issue,
// agent) 槽的活动任务，让被挡的完成重放绝不静默丢弃（#5914，Elon 第 8 轮）。
//
// 没有它，推迟只会被消费「一次」：任务 A 的完成 reconcile 重放评论 C，入队撞上更新的挡路者
// B，失败被丢弃，B 永远见不到 C（C 早于 B、不在它的 planned ids）——于是 C 丢了，尽管原始
// 响应承诺过它会被处理。传播把「消费一次」变成「一路转交」，直到某任务可证明覆盖 C。
//
// 每次尝试按顺序试三种能持久覆盖评论的写，并在槽的占用者在我们脚下翻状态时「重试」——交接
// 不是一条原子语句，两步之间完成或被 claim 的挡路者必须被重新观察而不是丢失（Elon 第 9 轮）：
//
//  1. CLAIM-RECEIPT 任务（dispatched/running/waiting），任意 HEAD：claim 之后补的 planned id
//     永远不会投递给那个运行，因此它不会在错误的 HEAD 下消费评论——它纯粹把评论排进该任务
//     自己的完成 reconcile，届时以当时的 HEAD 重新入队；
//  2. 同 HEAD 的 queued 任务：原子合并，重盖 trigger / originator / overlay（MUL-4302）。
//     刻意 HEAD 限定：折进不同 HEAD 的 queued 任务会让评论成为那个运行的 trigger、claim 时
//     标记已投递——旧 HEAD 运行会消费新 HEAD 的请求、而永远不会创建新 HEAD 跟进——即
//     TEN-356 违规（Elon 第 9 轮，必须修 #2）；
//  3. 全新入队：恰在挡路者完成、释放唯一槽时正确——这正是交接中途完成留下的状态。
//
// (3) 的重复键错误意味着槽仍被占，循环重新观察再试。只有当每次尝试都发现槽被「既盖不住也
// 收不下该评论」的任务占着（不同 HEAD 的 QUEUED 挡路者）才返回 false；调用方大声记录该情况——
// 这是当前 schema 没有安全车位的一种形态，收掉它需要持久化义务记录而不是请求内交接。
func (h *Handler) propagateUncoveredCommentObligation(ctx context.Context, issue db.Issue, trigger commentAgentTrigger, commentID pgtype.UUID, headSha pgtype.Text) bool {
	noEscalation := func() time.Duration { return 0 }
	const maxAttempts = 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		registered, err := h.registerPlannedCommentForActiveTask(ctx, issue, trigger.Agent.ID, commentID, pgtype.Text{})
		if err != nil {
			slog.Warn("propagate comment obligation: register on active task failed",
				"issue_id", uuidToString(issue.ID), "agent_id", uuidToString(trigger.Agent.ID),
				"comment_id", uuidToString(commentID), "error", err)
		} else if registered {
			return true
		}
		if h.mergeCommentIntoPendingTask(ctx, issue, trigger, commentID, headSha) == commentMergeSucceeded {
			return true
		}
		err = h.enqueueSingleCommentTrigger(ctx, issue, commentID, trigger, noEscalation)
		if err == nil {
			return true
		}
		if !errors.Is(err, service.ErrDuplicatePendingTask) {
			return false
		}
		// 槽仍被占着；占用者可能在上面的步骤之间翻了状态，重新观察再试。
	}
	return false
}

// logCommentEnqueueFailure 记录一次失败的评论触发入队。良性的重复待处理任务竞态
// （service.ErrDuplicatePendingTask）不是故障——resolveCommentTriggerEnqueue 会把评论折进
// 赢家任务——因此以 debug 记录、绝不当 warning，避免把原始 Postgres 约束名留在 warn/error
// 日志里（#5914，Elon review）。其它错误保持带 error 的 warning。
func logCommentEnqueueFailure(msg string, err error, attrs ...any) {
	if errors.Is(err, service.ErrDuplicatePendingTask) {
		slog.Debug(msg+": duplicate pending task, coalescing into the sibling run", attrs...)
		return
	}
	slog.Warn(msg, append(attrs, "error", err)...)
}

// enqueueSingleCommentTrigger 为一条已计算的触发新建任务。从 enqueueCommentAgentTriggers
// 拆出来，让 merge-not-drop 路径（MUL-4195）在待处理任务中途消失时可以回退到它。返回主要的
// 入队错误（成功为 nil），调用方据此上报 trigger_outcome（MUL-4525 §2）。次要工作（推迟的
// 升级兜底）保持尽力记录，不影响返回的错误。
func (h *Handler) enqueueSingleCommentTrigger(ctx context.Context, issue db.Issue, triggerCommentID pgtype.UUID, trigger commentAgentTrigger, getEscalationDelay func() time.Duration) error {
	switch trigger.Source {
	case commentTriggerSourceIssueAssignee:
		if trigger.Squad != nil {
			if _, err := h.TaskService.EnqueueTaskForSquadLeader(ctx, issue, trigger.Agent.ID, trigger.Squad.ID, triggerCommentID); err != nil {
				logCommentEnqueueFailure("enqueue squad leader task failed", err,
					"issue_id", uuidToString(issue.ID),
					"squad_id", uuidToString(trigger.Squad.ID),
					"leader_id", uuidToString(trigger.Agent.ID))
				return err
			}
			return nil
		}
		if _, err := h.TaskService.EnqueueTaskForIssue(ctx, issue, triggerCommentID); err != nil {
			slog.Warn("enqueue agent task on comment failed", "issue_id", uuidToString(issue.ID), "error", err)
			return err
		}
	case commentTriggerSourceMentionSquadLeader:
		if _, err := h.TaskService.EnqueueTaskForSquadLeader(ctx, issue, trigger.Agent.ID, trigger.Squad.ID, triggerCommentID); err != nil {
			logCommentEnqueueFailure("enqueue squad leader mention task failed", err,
				"issue_id", uuidToString(issue.ID),
				"agent_id", uuidToString(trigger.Agent.ID))
			return err
		}
	case commentTriggerSourceMentionAgent:
		if _, err := h.TaskService.EnqueueTaskForMention(ctx, issue, trigger.Agent.ID, triggerCommentID); err != nil {
			logCommentEnqueueFailure("enqueue mention agent task failed", err,
				"issue_id", uuidToString(issue.ID),
				"agent_id", uuidToString(trigger.Agent.ID))
			return err
		}
	case commentTriggerSourceThreadParent, commentTriggerSourceConversation, commentTriggerSourceReviewAssignee:
		var task db.AgentTaskQueue
		var err error
		// 7. A reviewer-handover is thread-linked like a parent-author reply: the run
		// answers in the same thread, so the reply keeps its parent_id. A squad owner
		// still runs as its leader with the squad context.
		if trigger.Squad != nil &&
			(trigger.Source == commentTriggerSourceConversation || trigger.Source == commentTriggerSourceReviewAssignee) {
			task, err = h.TaskService.EnqueueTaskForSquadLeader(ctx, issue, trigger.Agent.ID, trigger.Squad.ID, triggerCommentID)
		} else {
			task, err = h.TaskService.EnqueueTaskForThreadParent(ctx, issue, trigger.Agent.ID, triggerCommentID)
		}
		if err != nil {
			logCommentEnqueueFailure("enqueue routed comment agent task failed", err,
				"issue_id", uuidToString(issue.ID),
				"agent_id", uuidToString(trigger.Agent.ID),
				"source", trigger.Source)
			return err
		}
		if trigger.EscalationFallback == nil || getEscalationDelay() <= 0 {
			return nil
		}
		var squadID pgtype.UUID
		if trigger.EscalationFallback.Squad != nil {
			squadID = trigger.EscalationFallback.Squad.ID
		}
		if _, err := h.TaskService.EnqueueDeferredAssigneeFallback(ctx, issue, trigger.EscalationFallback.Agent.ID, squadID, task.ID, triggerCommentID, time.Now().Add(getEscalationDelay())); err != nil {
			slog.Warn("enqueue deferred assignee fallback failed",
				"issue_id", uuidToString(issue.ID),
				"primary_agent_id", uuidToString(trigger.Agent.ID),
				"fallback_agent_id", uuidToString(trigger.EscalationFallback.Agent.ID),
				"error", err)
		}
	}
	return nil
}

// computeCommentAgentTriggers 解析一条评论会触发哪些 agent（按执行 agent 去重），外加每个
// 显式 @agent / @squad 提及的按目标列表（MUL-4525 §2）。target 只来自显式提及路径——隐式
// 路由回退（受理人、线程父级、会话）从没被用户点名，那边没有路由不算静默无操作。
// 第三个返回值只在评论是对托管审查员的回复时非空：它说明这次回复被转交给谁（GONG-63）。
func (h *Handler) computeCommentAgentTriggers(ctx context.Context, issue db.Issue, content string, parentComment *db.Comment, actorType, actorID string, opts commentTriggerComputeOptions) ([]commentAgentTrigger, []commentMentionTarget, *commentReviewReplyRoute) {
	if isNoteComment(content) {
		return nil, nil, nil
	}

	// 修改 6. 委派授权由闸门经 opts.effectiveInvokerFor(agent) 应用：运行没有人类发起人时，闸门依次
	// 回退到 opts.AutopilotDelegationAuthorityUserID（MUL-4857 的 autopilot 创建者）与
	// opts.TaskDelegationAuthority（GONG-83 的任务级委托继承）——两者都由调用方从服务端信任、
	// 血缘验证过的发言任务解析。这里不按 issue 来源重新推导，无关的无归属运行因此不能靠评论
	// 所在那条 autopilot 的 issue 来借陌生 autopilot 创建者的授权。
	mentions := util.ParseMentions(content)

	// 显式 @agent / @squad 提及是直接请求，优先级高于 @all 广播（MUL-5411）。@all 只抑制下面
	// 的隐式路由回退（受理人 / 线程父级 / 会话）——绝不能吞掉作者亲手点名的目标。在此之前，
	// 同时带 `@all` 和 `@Preflight` 的评论什么都不会入队。`all` 既不是 "agent" 也不是 "squad"，
	// 因此在 resolveMentionedAgentCommentTriggers 内被跳过、不会自建运行。
	if hasAgentOrSquadMention(mentions) {
		return h.resolveMentionedAgentCommentTriggers(ctx, issue, mentions, actorType, actorID, opts)
	}
	if util.HasMentionAll(mentions) {
		return nil, nil, nil
	}
	if hasMemberMention(mentions) {
		return nil, nil, nil
	}

	if actorType != "member" {
		// agent 写的评论不参与成员驱动的会话路由（父作者 / 线程根延续）与成员受理人回退。
		// 它们保留一条 MUL-3794 之后恢复的窄路径（MUL-3879）：worker-agent 在 squad 受理的
		// issue 上写结果评论仍可唤醒被分配的 squad leader，leader→worker→leader 的协作环因此
		// 保持闭合。leader 自我触发守卫在 routeAssignedSquadLeaderFallback。显式 @agent / @squad
		// 提及已在上面处理，这里不会把提及目标与受理 leader 重复入队。
		if issue.AssigneeType.Valid && issue.AssigneeType.String == "squad" {
			if trigger, ok := h.routeAssignedSquadLeaderFallback(ctx, issue, actorType, actorID, opts); ok {
				return []commentAgentTrigger{trigger}, nil, nil
			}
		}
		return nil, nil, nil
	}

	if parentComment != nil && parentComment.AuthorType == "agent" {
		// 8. A reply under the reviewer's thread is addressed to the reviewer, even
		// when the newest message came from the developer: the composer keeps
		// parent_id on the thread root. Hand it to the current owner instead of the
		// (deliberately uncallable) reviewer, and report who actually received it.
		if h.isReviewerComment(ctx, issue, parentComment) {
			route := h.resolveReviewReplyRoute(ctx, issue, actorType, actorID, opts)
			if route.HasTrigger {
				return []commentAgentTrigger{route.Trigger}, nil, &route
			}
			return nil, nil, &route
		}
		trigger, ok := h.routeReplyToParentAuthor(ctx, issue, parentComment, actorType, actorID, opts)
		if !ok {
			return nil, nil, nil
		}
		if fallback, ok := h.routeAssigneeFallback(ctx, issue, actorType, actorID, opts); ok &&
			uuidToString(fallback.Agent.ID) != uuidToString(trigger.Agent.ID) {
			trigger.EscalationFallback = &commentEscalationFallback{
				Agent: fallback.Agent,
				Squad: fallback.Squad,
			}
		}
		return []commentAgentTrigger{trigger}, nil, nil
	}

	if parentComment != nil {
		triggers, handled := h.routeThreadRootOwners(ctx, issue, parentComment, actorID, opts)
		if handled {
			if len(triggers) == 0 {
				return nil, nil, nil
			}
			if len(triggers) == 1 {
				if fallback, ok := h.routeAssigneeFallback(ctx, issue, actorType, actorID, opts); ok &&
					uuidToString(fallback.Agent.ID) != uuidToString(triggers[0].Agent.ID) {
					triggers[0].EscalationFallback = &commentEscalationFallback{
						Agent: fallback.Agent,
						Squad: fallback.Squad,
					}
				}
			}
			return triggers, nil, nil
		}
		// 纯成员对成员的回复不能因为线程没有 agent 主人就启动 issue 受理人。显式提及与既有
		// 会话主人已在上面解析过。
		if parentComment.AuthorType == "member" {
			return nil, nil, nil
		}
	}

	if trigger, ok := h.routeAssigneeFallback(ctx, issue, actorType, actorID, opts); ok {
		return []commentAgentTrigger{trigger}, nil, nil
	}
	return nil, nil, nil
}

// hasAgentOrSquadMention 判断提及里是否含 agent 或 squad 类型。
func hasAgentOrSquadMention(mentions []util.Mention) bool {
	for _, m := range mentions {
		if m.Type == "agent" || m.Type == "squad" {
			return true
		}
	}
	return false
}

// hasMemberMention 判断提及里是否含 member 类型。
func hasMemberMention(mentions []util.Mention) bool {
	for _, m := range mentions {
		if m.Type == "member" {
			return true
		}
	}
	return false
}

// routeReplyToParentAuthor 处理「回复一条 agent 写的评论」：触发被回复的父评论作者。
func (h *Handler) routeReplyToParentAuthor(ctx context.Context, issue db.Issue, parent *db.Comment, authorType, authorID string, opts commentTriggerComputeOptions) (commentAgentTrigger, bool) {
	if parent == nil || parent.AuthorType != "agent" || !parent.AuthorID.Valid {
		return commentAgentTrigger{}, false
	}
	agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID:          parent.AuthorID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil || !agent.RuntimeID.Valid || agent.ArchivedAt.Valid {
		return commentAgentTrigger{}, false
	}
	if !h.canInvokeAgent(ctx, agent, authorType, authorID, opts.effectiveInvokerFor(agent), uuidToString(issue.WorkspaceID)) {
		return commentAgentTrigger{}, false
	}
	hasPending, err := h.hasPendingTaskForIssueAndAgent(ctx, issue.ID, parent.AuthorID, opts)
	if err != nil {
		return commentAgentTrigger{}, false
	}
	return commentAgentTrigger{Agent: agent, Source: commentTriggerSourceThreadParent, AlreadyPending: hasPending}, true
}

// conversationRoutedAgentInfo 记录会话延续路由里某个 agent 关联的 squad（若经 squad 加入）。
type conversationRoutedAgentInfo struct {
	SquadID pgtype.UUID
}

// routeThreadRootOwners 处理成员回复线程内的成员评论：找到线程根（须为成员作者）后继续
// 解析该线程根的会话主人。
func (h *Handler) routeThreadRootOwners(ctx context.Context, issue db.Issue, parent *db.Comment, memberID string, opts commentTriggerComputeOptions) ([]commentAgentTrigger, bool) {
	if parent == nil || !parent.ID.Valid {
		return nil, false
	}
	root, err := h.Queries.GetThreadRoot(ctx, db.GetThreadRootParams{
		CommentID:   parent.ID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil || !root.ID.Valid || root.AuthorType != "member" {
		return nil, false
	}
	return h.routeConversationOwnersForRoot(ctx, issue, root, memberID, opts)
}

// routeConversationOwnersForRoot 解析以 member 根开头的会话的「延续主人」：根里若显式 @过
// agent/squad，触发它；否则按历史上从该根触发的任务去重出延续目标。
func (h *Handler) routeConversationOwnersForRoot(ctx context.Context, issue db.Issue, root db.Comment, memberID string, opts commentTriggerComputeOptions) ([]commentAgentTrigger, bool) {
	if !root.ID.Valid || root.AuthorType != "member" {
		return nil, false
	}
	if trigger, hasExplicitOwner, ok := h.routeFirstExplicitRootMentionOwner(ctx, issue, root, memberID, opts); hasExplicitOwner {
		if !ok {
			return nil, true
		}
		return []commentAgentTrigger{trigger}, true
	}

	rootID := uuidToString(root.ID)
	excludedID := uuidToString(opts.ExcludeTriggerCommentID)

	tasks, err := h.Queries.ListTasksByIssue(ctx, issue.ID)
	if err != nil {
		return nil, false
	}
	routedAgents := make(map[string]conversationRoutedAgentInfo)
	for _, task := range tasks {
		if !task.AgentID.Valid {
			continue
		}
		if excludedID != "" && uuidToString(task.TriggerCommentID) == excludedID {
			continue
		}
		ownsRoot := task.TriggerCommentID.Valid && uuidToString(task.TriggerCommentID) == rootID
		for _, deliveredID := range task.DeliveredCommentIds {
			if deliveredID.Valid && uuidToString(deliveredID) == rootID {
				ownsRoot = true
				break
			}
		}
		if !ownsRoot {
			continue
		}
		agentID := uuidToString(task.AgentID)
		info := routedAgents[agentID]
		if !info.SquadID.Valid {
			info.SquadID = task.SquadID
		}
		routedAgents[agentID] = info
	}
	if len(routedAgents) == 0 {
		return nil, false
	}

	triggers := make([]commentAgentTrigger, 0, len(routedAgents))
	for agentID, info := range routedAgents {
		trigger, ok := h.routeConversationContinuationToAgent(ctx, issue, parseUUID(agentID), info.SquadID, memberID, opts)
		if ok {
			triggers = append(triggers, trigger)
		}
	}
	return triggers, true
}

// routeFirstExplicitRootMentionOwner 查看线程根内容里第一个显式 @agent/@squad，命中则把它
// 作为会话延续触发目标（有显式主人时返回 hasExplicitOwner=true）。
func (h *Handler) routeFirstExplicitRootMentionOwner(ctx context.Context, issue db.Issue, root db.Comment, memberID string, opts commentTriggerComputeOptions) (commentAgentTrigger, bool, bool) {
	for _, mention := range util.ParseMentions(root.Content) {
		switch mention.Type {
		case "agent":
			agentID, err := util.ParseUUID(mention.ID)
			if err != nil {
				return commentAgentTrigger{}, true, false
			}
			trigger, ok := h.routeConversationContinuationToAgent(ctx, issue, agentID, pgtype.UUID{}, memberID, opts)
			return trigger, true, ok
		case "squad":
			squadID, err := util.ParseUUID(mention.ID)
			if err != nil {
				return commentAgentTrigger{}, true, false
			}
			squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
				ID:          squadID,
				WorkspaceID: issue.WorkspaceID,
			})
			if err != nil {
				return commentAgentTrigger{}, true, false
			}
			trigger, ok := h.routeConversationContinuationToAgent(ctx, issue, squad.LeaderID, squadID, memberID, opts)
			return trigger, true, ok
		}
	}
	return commentAgentTrigger{}, false, false
}

// routeConversationContinuationToAgent 校验 agent 可作为会话延续触发目标（在存在/未归档/
// runtime/可调用），构造带可选 squad 的会话延续触发。
func (h *Handler) routeConversationContinuationToAgent(ctx context.Context, issue db.Issue, agentID, squadID pgtype.UUID, memberID string, opts commentTriggerComputeOptions) (commentAgentTrigger, bool) {
	if !agentID.Valid {
		return commentAgentTrigger{}, false
	}
	agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID:          agentID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil || !agent.RuntimeID.Valid || agent.ArchivedAt.Valid {
		return commentAgentTrigger{}, false
	}
	if !h.canInvokeAgent(ctx, agent, "member", memberID, memberID, uuidToString(issue.WorkspaceID)) {
		return commentAgentTrigger{}, false
	}
	hasPending, err := h.hasPendingTaskForIssueAndAgent(ctx, issue.ID, agentID, opts)
	if err != nil {
		return commentAgentTrigger{}, false
	}
	trigger := commentAgentTrigger{Agent: agent, Source: commentTriggerSourceConversation, AlreadyPending: hasPending}
	if squadID.Valid {
		if squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
			ID:          squadID,
			WorkspaceID: issue.WorkspaceID,
		}); err == nil {
			trigger.Squad = &squad
		}
	}
	return trigger, true
}

// routeAssigneeFallback 处理隐式「评论没点名任何人但 issue 有受理人」的回退：受理是 agent 则
// 触发该 agent；受理是 squad 则触发 squad leader。
func (h *Handler) routeAssigneeFallback(ctx context.Context, issue db.Issue, authorType, authorID string, opts commentTriggerComputeOptions) (commentAgentTrigger, bool) {
	if !issue.AssigneeType.Valid || !issue.AssigneeID.Valid {
		return commentAgentTrigger{}, false
	}
	switch issue.AssigneeType.String {
	case "agent":
		agent, hasPending, ok := h.assigneeFallbackAgent(ctx, issue, authorType, authorID, opts)
		if !ok {
			return commentAgentTrigger{}, false
		}
		return commentAgentTrigger{Agent: agent, Source: commentTriggerSourceIssueAssignee, AlreadyPending: hasPending}, true
	case "squad":
		return h.routeAssignedSquadLeaderFallback(ctx, issue, authorType, authorID, opts)
	default:
		return commentAgentTrigger{}, false
	}
}

// routeAssignedSquadLeaderFallback 解析被分配的 squad 的 leader 作为回退目标；agent 作者就是
// leader 且命中自我触发抑制时不触发。
func (h *Handler) routeAssignedSquadLeaderFallback(ctx context.Context, issue db.Issue, authorType, authorID string, opts commentTriggerComputeOptions) (commentAgentTrigger, bool) {
	squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
		ID:          issue.AssigneeID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return commentAgentTrigger{}, false
	}
	if authorType == "agent" && authorID == uuidToString(squad.LeaderID) &&
		h.shouldSuppressSquadLeaderSelfTrigger(ctx, issue.ID, squad.LeaderID, squad.ID) {
		return commentAgentTrigger{}, false
	}
	agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID:          squad.LeaderID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil || !agent.RuntimeID.Valid || agent.ArchivedAt.Valid {
		return commentAgentTrigger{}, false
	}
	if !h.canInvokeAgent(ctx, agent, authorType, authorID, opts.effectiveInvokerFor(agent), uuidToString(issue.WorkspaceID)) {
		return commentAgentTrigger{}, false
	}
	hasPending, err := h.hasPendingTaskForIssueAndAgent(ctx, issue.ID, squad.LeaderID, opts)
	if err != nil {
		return commentAgentTrigger{}, false
	}
	return commentAgentTrigger{Agent: agent, Source: commentTriggerSourceIssueAssignee, Squad: &squad, AlreadyPending: hasPending}, true
}

// hasPendingTaskForIssueAndAgent 查询 (issue, agent) 是否已有待处理任务；提供
// ExcludeTriggerCommentID 时排除以该评论为触发的任务（编辑重发预览用）。
func (h *Handler) hasPendingTaskForIssueAndAgent(ctx context.Context, issueID, agentID pgtype.UUID, opts commentTriggerComputeOptions) (bool, error) {
	// 去重以被 review 过的 HEAD 为 key：mid-review 重新 push PR 会让去重失效、新运行对新的
	// HEAD 入队（TEN-356）。
	headSha := h.TaskService.ResolveIssueReviewSHAParam(ctx, issueID)
	if opts.ExcludeTriggerCommentID.Valid {
		return h.Queries.HasPendingTaskForIssueAndAgentExcludingTriggerComment(ctx, db.HasPendingTaskForIssueAndAgentExcludingTriggerCommentParams{
			IssueID:                 issueID,
			AgentID:                 agentID,
			ExcludeTriggerCommentID: opts.ExcludeTriggerCommentID,
			HeadSha:                 headSha,
		})
	}
	return h.Queries.HasPendingTaskForIssueAndAgent(ctx, db.HasPendingTaskForIssueAndAgentParams{
		IssueID: issueID,
		AgentID: agentID,
		HeadSha: headSha,
	})
}

// resolveMentionedAgentCommentTriggers 解析当前评论里的显式 @agent 与 @squad 提及，返回可
// 执行的 agent 接收方。跳过 on_mention 触发被禁用的 agent，以及被非 owner 成员提及的私密
// agent（只有 agent owner 或工作区 admin/owner 能提私密 agent）。自我提及有意允许——一条
// issue 里跑的 agent 可以在另一条 issue 上显式给自己入队（例如子任务运行通知父 issue，而父
// issue 的受理人正是同一 agent）；失控循环由 HasPendingTaskForIssueAndAgent 去重与任务队列
// 天然的 queued/dispatched 合并挡住。注意这里没有 issue 状态闸门——@提及是显式动作，对
// done/cancelled 的 issue 也该能用（需要时 agent 可以重新打开 issue）。
// commentMentionTarget 是某次显式 @agent / @squad 提及及其解析结果（MUL-4525 §2）。它与执行
// 触发分开记录：多次提及可能解析到同一执行 agent（例如 @agent A 与 @Squad S，S 的 leader 是
// A），即使运行被合并，每次提及仍要各自的结果。解析字段恰好设置一个：
//   - ExecAgentID 非空 → 该提及经这个执行 agent 运行，结果镜像该 agent 的入队状态
//     （queued/coalesced/deferred）。
//   - Status（带 ReasonCode）已设 → 终态、不执行的结果（blocked，或被推迟的 A2A 自我抑制
//     squad leader）。
type commentMentionTarget struct {
	TargetType  string // "agent" | "squad"
	TargetID    string
	ExecAgentID string
	Status      DispatchStatus
	ReasonCode  DispatchReasonCode
	// unusable 为需要持久痕迹的那一种原因（runtime_unusable）携带被拒 agent 与其判定。
	// handler 内部用：解析器也服务编辑器「预览」，它只记录发生了什么——写通知是触发路径的活。
	unusable *blockedRuntimeNotice
}

// blockedRuntimeNotice 是值得留在 issue 上的一次拒绝：哪个 agent、以及点名修复方式的判定。
type blockedRuntimeNotice struct {
	agent   db.Agent
	verdict service.AgentVerdict
}

func (h *Handler) resolveMentionedAgentCommentTriggers(ctx context.Context, issue db.Issue, mentions []util.Mention, authorType, authorID string, opts commentTriggerComputeOptions) ([]commentAgentTrigger, []commentMentionTarget, *commentReviewReplyRoute) {
	wsID := uuidToString(issue.WorkspaceID)
	triggers := make([]commentAgentTrigger, 0, len(mentions))
	// seen 按「解析后的 agent id」对 EXECUTION 去重：两条提及解析到同一 agent 只入队一条任务。
	// 映射到 trigger 的索引，让 squad-leader 提及能「升级」已加入的同 agent 普通 @agent 触发——
	// leader 任务是严格超集（设置 is_leader_task 与 squad_id，daemon 才会注入 squad 简报），
	// 合并后的运行必须带上该角色，无论提及顺序如何。
	seen := make(map[string]int, len(mentions))
	add := func(trigger commentAgentTrigger) {
		id := uuidToString(trigger.Agent.ID)
		if idx, ok := seen[id]; ok {
			if triggers[idx].Source != commentTriggerSourceMentionSquadLeader &&
				trigger.Source == commentTriggerSourceMentionSquadLeader {
				triggers[idx] = trigger
			}
			return
		}
		seen[id] = len(triggers)
		triggers = append(triggers, trigger)
	}
	// targets 按「用户点名的目标」（type:id）去重、一次显式提及一条结果——不是按执行 agent——
	// 因此被点名却没有被执行的目标绝不会静默丢失，即便它的运行与别家合并。
	var targets []commentMentionTarget
	targetSeen := make(map[string]struct{}, len(mentions))
	addTarget := func(t commentMentionTarget) {
		key := t.TargetType + ":" + t.TargetID
		if _, ok := targetSeen[key]; ok {
			return
		}
		targetSeen[key] = struct{}{}
		targets = append(targets, t)
	}
	// blockTarget 记录一条不会触发的提及。调用闸门在读取任何 archived/runtime 状态之前判定，
	// 因此无法调用私密目标的调用方只学到通用的 invocation_not_allowed，绝不能凭 reason code
	// 枚举出目标的存在性或状态。
	blockTarget := func(targetType, targetID string, reason DispatchReasonCode) {
		addTarget(commentMentionTarget{TargetType: targetType, TargetID: targetID, Status: DispatchBlocked, ReasonCode: reason})
	}
	// blockUnusableTarget 是「那个还需要持久痕迹的判定」专用的 blockTarget。每个作者都会收到，
	// 包括人类：chip 与 toast 带着 reason code 却不带修复命令，而 agent 写的提及响应根本没人看。
	blockUnusableTarget := func(targetType, targetID string, agent db.Agent, verdict service.AgentVerdict) {
		notice := &blockedRuntimeNotice{agent: agent, verdict: verdict}
		if verdict.Reason != ReasonRuntimeUnusable {
			notice = nil
		}
		addTarget(commentMentionTarget{
			TargetType: targetType, TargetID: targetID,
			Status: DispatchBlocked, ReasonCode: verdict.Reason, unusable: notice,
		})
	}
	// 9. A mention of the managed reviewer cannot start a run (canInvokeAgent keeps it
	// closed to ordinary calls), so it is handed to the current issue owner instead —
	// resolved once for all reviewer mentions in this comment, and deduplicated by the
	// trigger's agent id like any other target so @reviewer plus @owner runs the owner
	// only once (GONG-63).
	var reviewReply *commentReviewReplyRoute
	reviewRoute := func() *commentReviewReplyRoute {
		if reviewReply == nil {
			route := h.resolveReviewReplyRoute(ctx, issue, authorType, authorID, opts)
			reviewReply = &route
		}
		return reviewReply
	}
	for _, m := range mentions {
		if m.Type == "squad" {
			// @squad 提及 → 触发 squad 的 leader agent。
			// 提及 id 来自不可信的评论文本，MentionRe 接受任意 hex/dash 串（mention://squad/-），
			// 所以用带错误返回的 ParseUUID——Must* 变体会让请求 panic，而在创建路径上那时评论行
			// 已提交。畸形 id 与「格式合法但不拥有 squad」无法区分：同样的 target_unavailable
			// 结果、不跑运行。
			squadUUID, err := util.ParseUUID(m.ID)
			if err != nil {
				blockTarget("squad", m.ID, ReasonTargetUnavailable)
				continue
			}
			squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
				ID:          squadUUID,
				WorkspaceID: issue.WorkspaceID,
			})
			if err != nil {
				blockTarget("squad", m.ID, ReasonTargetUnavailable)
				continue
			}
			leaderID := squad.LeaderID
			// A2A 自我抑制：作者就是这个 squad 的 leader，且它在本 issue 上最近的任务是
			// leader/generic 角色（不是新的同 squad worker→leader 交接），就不因它自己的 @提及
			// 重新触发 leader。结果必须反映现实、不能假设成功（MUL-4525）：只有真实非终态任务仍
			// 活动时才 `deferred`（它的 reconcile 覆盖该评论）；查询失败是非成功的 internal_error、
			// 绝非编造的 deferred；否则什么都没跑 → self_trigger_suppressed。
			if authorType == "agent" && authorID == uuidToString(leaderID) &&
				h.shouldSuppressSquadLeaderSelfTrigger(ctx, issue.ID, leaderID, squad.ID) {
				active, activeErr := h.hasActiveTaskForIssueAndAgent(ctx, issue.ID, leaderID)
				status, reason := decideSuppressedLeaderOutcome(active, activeErr)
				addTarget(commentMentionTarget{TargetType: "squad", TargetID: m.ID, Status: status, ReasonCode: reason})
				continue
			}
			agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
				ID:          leaderID,
				WorkspaceID: issue.WorkspaceID,
			})
			if err != nil {
				blockTarget("squad", m.ID, ReasonTargetUnavailable)
				continue
			}
			// 私密 leader 闸门优先（枚举安全：调不动 leader 的调用方永远学不到它的
			// archived/runtime 状态）。
			if !h.canInvokeAgent(ctx, agent, authorType, authorID, opts.effectiveInvokerFor(agent), wsID) {
				blockTarget("squad", m.ID, ReasonInvocationNotAllowed)
				continue
			}
			if agent.ArchivedAt.Valid {
				blockTarget("squad", m.ID, ReasonTargetUnavailable)
				continue
			}
			// 与下面的直接 agent 分支共用同一条就绪判定。
			if verdict, err := service.AgentReadiness(ctx, h.runtimeLookup(obsmetrics.RuntimeLookupSourceComment), agent); err == nil && verdict.Blocked() {
				blockUnusableTarget("squad", m.ID, agent, verdict)
				continue
			}
			hasPending, err := h.hasPendingTaskForIssueAndAgent(ctx, issue.ID, leaderID, opts)
			if err != nil {
				blockTarget("squad", m.ID, ReasonInternalError)
				continue
			}
			add(commentAgentTrigger{Agent: agent, Source: commentTriggerSourceMentionSquadLeader, Squad: &squad, AlreadyPending: hasPending})
			addTarget(commentMentionTarget{TargetType: "squad", TargetID: m.ID, ExecAgentID: uuidToString(leaderID)})
			continue
		}
		if m.Type != "agent" {
			continue
		}
		agentUUID, err := util.ParseUUID(m.ID)
		if err != nil {
			// 不可信的评论文本：MentionRe 匹配任意 hex/dash 串，畸形 id 绝不能进会 panic 的
			// Must* 解析器。连 UUID 都不是的串在「任何」工作区都命名不了实体，这里没有存在性
			// 可藏，调用权限码会是虚假成因（MUL-5548）：与上面 squad 路径一致上报
			// target_unavailable。只有下面「格式合法但解析不到」的情况保持枚举安全。
			blockTarget("agent", m.ID, ReasonTargetUnavailable)
			continue
		}
		// 以当前 issue 的工作区为界加载 agent。用裸 GetAgent 会让提及解析到别的工作区的 agent，
		// 下面的可见性检查就会对着错误工作区的角色判定（本工作区的 owner 会替「住在别人工作区」
		// 的私密 agent 放行）。
		agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
			ID:          agentUUID,
			WorkspaceID: issue.WorkspaceID,
		})
		if err != nil {
			// 不透露 id 是否存在（可能是别的工作区里的私密 agent）：统一返回通用的
			// invocation_not_allowed。
			blockTarget("agent", m.ID, ReasonInvocationNotAllowed)
			continue
		}
		// 10. A member's mention of the managed reviewer goes to the current owner rather
		// than reading as a rejected mention: the reviewer is not callable by design, and
		// the user's message should still reach whoever owns the task. No per-target
		// outcome is recorded — the handover is reported through review_reply. Agent
		// authors keep the blocked outcome, so an agent cannot wake the owner by
		// mentioning the reviewer.
		if authorType == "member" && isManagedReviewerAgent(agent) {
			if route := reviewRoute(); route.HasTrigger {
				add(route.Trigger)
			}
			continue
		}
		// 私密 agent 闸门优先，先于任何 archived/runtime 状态读取。
		if !h.canInvokeAgent(ctx, agent, authorType, authorID, opts.effectiveInvokerFor(agent), wsID) {
			blockTarget("agent", m.ID, ReasonInvocationNotAllowed)
			continue
		}
		if agent.ArchivedAt.Valid {
			blockTarget("agent", m.ID, ReasonTargetUnavailable)
			continue
		}
		// 所有准入路径共用同一条就绪判定（service.AgentReadiness）。只有 BLOCKED 判定拒绝提及：
		// 未绑定的 agent 没有可带回来的机器（MUL-5559），CLI 跑不了的机器也不会自己开始跑
		//（MUL-6164）。仅离线的 runtime 保持入队——机器回来后等待自己结束，取消它反而会移除
		// 人们依赖的行为。
		if verdict, err := service.AgentReadiness(ctx, h.runtimeLookup(obsmetrics.RuntimeLookupSourceComment), agent); err == nil && verdict.Blocked() {
			blockUnusableTarget("agent", m.ID, agent, verdict)
			continue
		}
		hasPending, err := h.hasPendingTaskForIssueAndAgent(ctx, issue.ID, agentUUID, opts)
		if err != nil {
			blockTarget("agent", m.ID, ReasonInternalError)
			continue
		}
		add(commentAgentTrigger{Agent: agent, Source: commentTriggerSourceMentionAgent, AlreadyPending: hasPending})
		addTarget(commentMentionTarget{TargetType: "agent", TargetID: m.ID, ExecAgentID: uuidToString(agentUUID)})
	}
	return triggers, targets, reviewReply
}

// UpdateComment 处理 PATCH/PUT /api/issues/comments/:commentId：更新评论内容/附件集。
// 权限：只有作者本人或工作区 owner/admin 可编辑；权限不足返回 403。可选 expected_revision /
// content_base 提供乐观并发控制（不匹配返回 409），strictContentEdit 时取消旧内容触发的任务
// 并重放幸存者；内容变更会走与创建完全一致的触发解析把新结果放回 trigger_outcomes。
func (h *Handler) UpdateComment(w http.ResponseWriter, r *http.Request) {
	commentId := chi.URLParam(r, "commentId")

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	commentUUID, ok := parseUUIDOrBadRequest(w, commentId, "comment id")
	if !ok {
		return
	}

	// Load comment scoped to current workspace.
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	existing, err := h.Queries.GetCommentInWorkspace(r.Context(), db.GetCommentInWorkspaceParams{
		ID:          commentUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "comment not found")
		return
	}

	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}

	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	isAuthor := existing.AuthorType == actorType && uuidToString(existing.AuthorID) == actorID
	isAdmin := roleAllowed(member.Role, "owner", "admin")
	if !isAuthor && !isAdmin {
		writeError(w, http.StatusForbidden, "only comment author or admin can edit")
		return
	}

	var req struct {
		Content          string    `json:"content"`
		ContentBase      *string   `json:"content_base,omitempty"`
		AttachmentIDs    *[]string `json:"attachment_ids"`
		SuppressAgentIDs []string  `json:"suppress_agent_ids"`
		ExpectedRevision *int64    `json:"expected_revision,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// See CreateComment: strip NUL / invalid-UTF-8 bytes PostgreSQL's TEXT column
	// rejects before the empty check, so an edit that introduces such a byte
	// can't 500 (GH #5388).
	req.Content = sanitizeNullBytes(req.Content)
	if req.ContentBase != nil {
		sanitized := sanitizeNullBytes(*req.ContentBase)
		req.ContentBase = &sanitized
	}
	if req.Content == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}
	if req.ExpectedRevision != nil {
		if *req.ExpectedRevision < 1 {
			writeError(w, http.StatusBadRequest, "expected_revision must be a positive integer")
			return
		}
		if existing.Revision != *req.ExpectedRevision {
			writeRevisionConflict(w, "comment", existing.ID, *req.ExpectedRevision, existing.Revision)
			return
		}
	}
	strictContentEdit := req.ContentBase != nil || req.ExpectedRevision != nil

	var attachmentIDs []pgtype.UUID
	replaceAttachments := req.AttachmentIDs != nil
	if replaceAttachments {
		var ok bool
		attachmentIDs, ok = parseUUIDSliceOrBadRequest(w, *req.AttachmentIDs, "attachment_ids")
		if !ok {
			return
		}
	}
	suppressAgentIDs, ok := parseUUIDSliceOrBadRequest(w, req.SuppressAgentIDs, "suppress_agent_ids")
	if !ok {
		return
	}

	// NOTE: See CreateComment — Markdown is sanitized at render/edit time, not here.

	// 默认保留现有归属血缘——此路径只服务于「未改动」的编辑（不重新触发）。下面内容真变时，
	// 血缘从「本次编辑动作」重新推导（MUL-4857），绝不从评论原始作者运行继承。
	oldContent := existing.Content
	sourceTaskID := existing.SourceTaskID
	var triggerIssue *db.Issue
	var cancelled []db.AgentTaskQueue
	if oldContent != req.Content {
		issue, err := h.Queries.GetIssue(r.Context(), existing.IssueID)
		if err != nil {
			slog.Warn("load issue for edit post-processing failed", "issue_id", uuidToString(existing.IssueID), "error", err)
			writeError(w, http.StatusInternalServerError, "failed to load issue")
			return
		}
		triggerIssue = &issue
		// 内容编辑是新动作，因此它的委派血缘必须以「本次编辑」为 key。只有 agent 作者重编自己
		// 的评论才把血缘带下去（commentSourceTaskID 重新盖章当前编辑任务）——预览、保存与推迟的
		// 完成 reconcile 都从这一动作解析授权。任何「其它」编辑者——owner/admin 编辑 agent 的
		// 评论，或 member 编辑自己的——都会清空血缘，让推迟的 reconcile 失败关闭，而不是复活原
		// autopilot 运行的创建者授权。admin 对评论持有的是管理权，不是对作者私密 agent 的调用权
		//（Elon 必须修，第 3 轮）。
		if actorType == "agent" && isAuthor {
			sourceTaskID = h.commentSourceTaskID(r)
		} else {
			sourceTaskID = pgtype.UUID{}
		}
		// 老客户端保留现有的「先取消后更新」行为。严格 revision 写把取消推迟到条件 UPDATE
		// 胜出之后，返回 409 的竞态因此不会改动任务队列。
		if !strictContentEdit {
			cancelled, err = h.TaskService.CancelTasksByTriggerComment(r.Context(), existing.ID)
			if err != nil {
				slog.Warn("cancel tasks for edited comment failed", "comment_id", uuidToString(existing.ID), "error", err)
				writeError(w, http.StatusInternalServerError, "failed to prepare comment edit")
				return
			}
		}
	}

	updateParams := db.UpdateCommentParams{
		ID:           commentUUID,
		Content:      req.Content,
		SourceTaskID: sourceTaskID,
	}
	if req.ContentBase != nil {
		updateParams.ContentBase = pgtype.Text{String: *req.ContentBase, Valid: true}
	}
	if req.ExpectedRevision != nil {
		updateParams.ExpectedRevision = pgtype.Int8{Int64: *req.ExpectedRevision, Valid: true}
	}
	var comment db.Comment
	var issueRevision int64
	var persistedEvent events.Event
	var cancelledEvents []events.Event
	transactionalEdit := replaceAttachments || (oldContent != req.Content && strictContentEdit)
	if transactionalEdit {
		// 严格正文编辑、附件集编辑、以及取消旧正文建的任务是同一个数据库结果。UpdateComment
		// 在替换附件前先拿行锁，两个现代编辑器因此不能交错它们的 CAS 检查与附件选择。正文 +
		// 附件是一次可见变更，因此 revision 恰好 +1。
		tx, beginErr := h.TxStarter.Begin(r.Context())
		if beginErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to prepare comment edit")
			return
		}
		defer tx.Rollback(r.Context())
		qtx := h.Queries.WithTx(tx)
		var updated db.UpdateCommentRow
		updated, err = qtx.UpdateComment(r.Context(), updateParams)
		if err == nil {
			comment = updated.Comment()
			issueRevision = updated.IssueRevision
		}
		if err == nil && oldContent != req.Content && strictContentEdit {
			cancelled, err = qtx.CancelAgentTasksByTriggerComment(r.Context(), existing.ID)
			if err == nil {
				err = service.SettleDeliveredDelegatedFailureRecoveries(r.Context(), qtx, cancelled...)
			}
			if err == nil {
				for _, task := range cancelled {
					var event events.Event
					event, err = service.RecordTaskTerminalEventTx(r.Context(), qtx, protocol.EventTaskCancelled, task,
						map[string]any{"reason": "comment_edited"})
					if err != nil {
						break
					}
					cancelledEvents = append(cancelledEvents, event)
				}
			}
		}
		if err == nil && replaceAttachments {
			var changed int64
			changed, err = qtx.ReplaceCommentAttachments(r.Context(), db.ReplaceCommentAttachmentsParams{
				CommentID:     comment.ID,
				IssueID:       existing.IssueID,
				AttachmentIds: attachmentIDs,
			})
			if err == nil && changed > 0 && oldContent == req.Content {
				comment, err = qtx.BumpCommentRevision(r.Context(), db.BumpCommentRevisionParams{
					ID:          comment.ID,
					WorkspaceID: existing.WorkspaceID,
				})
			}
		}
		if err == nil && comment.Revision != existing.Revision {
			persistedEvent, err = eventoutbox.Enqueue(r.Context(), qtx,
				buildCommentMutationEvent(comment, protocol.EventCommentUpdated, actorType, actorID, issueRevision))
		}
		if err == nil {
			err = tx.Commit(r.Context())
		}
		if err == nil {
			h.TaskService.BroadcastCancelledTasksWithEvents(r.Context(), uuidToString(existing.WorkspaceID), cancelled, cancelledEvents)
		}
	} else {
		// 即使 legacy/非严格编辑，事件也必须与行更新同事务记录。这条路径最可能被老客户端
		// 走到，若把它留成直接写，会重新引入「变更而无重放」的空档。
		tx, beginErr := h.TxStarter.Begin(r.Context())
		if beginErr != nil {
			err = beginErr
		} else {
			defer tx.Rollback(r.Context())
			qtx := h.Queries.WithTx(tx)
			var updated db.UpdateCommentRow
			updated, err = qtx.UpdateComment(r.Context(), updateParams)
			if err == nil {
				comment = updated.Comment()
				issueRevision = updated.IssueRevision
				if comment.Revision != existing.Revision {
					persistedEvent, err = eventoutbox.Enqueue(r.Context(), qtx,
						buildCommentMutationEvent(comment, protocol.EventCommentUpdated, actorType, actorID, issueRevision))
				}
			}
			if err == nil {
				err = tx.Commit(r.Context())
			}
		}
	}
	if err != nil {
		slog.Warn("update comment failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentId)...)
		if triggerIssue != nil && !strictContentEdit {
			// Cancellation committed but the edit did not. Restore the complete
			// original batch, including the still-valid unchanged comment.
			h.retriggerCancelledTaskSurvivors(r.Context(), *triggerIssue, cancelled, pgtype.UUID{})
		}
		if errors.Is(err, pgx.ErrNoRows) && strictContentEdit {
			current, reloadErr := h.Queries.GetCommentInWorkspace(r.Context(), db.GetCommentInWorkspaceParams{ID: commentUUID, WorkspaceID: wsUUID})
			if reloadErr == nil {
				if req.ExpectedRevision != nil {
					writeRevisionConflict(w, "comment", current.ID, *req.ExpectedRevision, current.Revision)
				} else {
					writeEditConflict(w, "comment", current.ID)
				}
				return
			}
		}
		writeError(w, http.StatusInternalServerError, "failed to update comment")
		return
	}
	retriggerEditedComment := func() commentTriggerDispatch {
		if oldContent == comment.Content {
			return commentTriggerDispatch{}
		}
		issue := *triggerIssue
		var parentComment *db.Comment
		if existing.ParentID.Valid {
			parent, err := h.Queries.GetComment(r.Context(), existing.ParentID)
			if err == nil {
				parentComment = &parent
			}
		}

		h.retriggerCancelledTaskSurvivors(r.Context(), issue, cancelled, existing.ID)
		// MUL-4857：source_task_id 刚刚由「本次编辑」重新推导过（agent 作者重盖当前任务；其它
		// 编辑者清空它），所以从评论解析就把委派授权 key 在本次编辑动作上——与编辑预览从同一
		// 请求算出的、以及与完成 reconcile 将恢复的完全一致。非作者编辑留下 NULL，跨 issue 的
		// 编辑任务在 autopilotDelegationAuthority 内被拒，两者都不能借用旧作者运行的权利。
		delegationAuthority := h.autopilotDelegationAuthorityFromComment(r.Context(), issue, comment)
		return h.triggerTasksForComment(r.Context(), issue, comment, parentComment, actorType, actorID, commentTriggerComputeOptions{
			OriginatorUserID:                   h.invokeOriginatorFromRequest(r, actorType, actorID),
			AutopilotDelegationAuthorityUserID: delegationAuthority,
			// 修改 7. 编辑重发与完成调和必须从「同一个」存储血缘解析任务级继承（GONG-83），
			// 否则预览说会派发、真正重放时却被拒。
			TaskDelegationAuthority: h.commentTaskDelegationResolver(r.Context(), issue, comment),
		}, suppressAgentIDs)
	}

	// 取更新后评论的表情与附件。
	grouped := h.groupReactions(r, []pgtype.UUID{comment.ID})
	groupedAtt := h.groupAttachments(r, []pgtype.UUID{comment.ID})
	cid := uuidToString(comment.ID)
	resp := commentToResponse(comment, grouped[cid], groupedAtt[cid])
	resp.IssueRevision = issueRevision
	slog.Info("comment updated", append(logger.RequestAttrs(r), "comment_id", commentId)...)
	if persistedEvent.ID != "" {
		h.publishEvent(persistedEvent)
	}

	// 上面的广播有意不带 trigger_outcomes——那是编辑者的私有反馈，不是共享时间线状态
	//（MUL-4525 §2）。
	dispatch := retriggerEditedComment()
	resp.TriggerOutcomes = dispatch.Outcomes
	resp.ReviewReply = dispatch.ReviewReply

	writeJSON(w, http.StatusOK, resp)
}

// DeleteComment 处理 DELETE /api/issues/comments/:commentId。权限：作者本人或工作区
// owner/admin。删除前收集附件 URL、取消以该评论为触发的待处理任务；删除与其持久化事件同事务，
// 之后删 S3 对象并重放取消任务批次里的幸存评论。返回 204。
func (h *Handler) DeleteComment(w http.ResponseWriter, r *http.Request) {
	commentId := chi.URLParam(r, "commentId")

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	commentUUID, ok := parseUUIDOrBadRequest(w, commentId, "comment id")
	if !ok {
		return
	}

	// Load comment scoped to current workspace.
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	comment, err := h.Queries.GetCommentInWorkspace(r.Context(), db.GetCommentInWorkspaceParams{
		ID:          commentUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "comment not found")
		return
	}

	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}

	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	isAuthor := comment.AuthorType == actorType && uuidToString(comment.AuthorID) == actorID
	isAdmin := roleAllowed(member.Role, "owner", "admin")
	if !isAuthor && !isAdmin {
		writeError(w, http.StatusForbidden, "only comment author or admin can delete")
		return
	}
	issue, err := h.Queries.GetIssue(r.Context(), comment.IssueID)
	hasIssue := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("load issue for delete post-processing failed", "issue_id", uuidToString(comment.IssueID), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load issue")
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		// issue 删除可能在 GetCommentInWorkspace 之后赢下竞态。继续走，让 DeleteComment 报告
		// 评论本身是否仍存在；触碰 issue activity 刻意做成尽力而为。
		slog.Info("comment parent issue no longer exists", "issue_id", uuidToString(comment.IssueID), "comment_id", commentId)
	}

	// 在 CASCADE 删除移除它们之前收集附件 URL。
	attachmentURLs, _ := h.Queries.ListAttachmentURLsByCommentID(r.Context(), comment.ID)

	// 取消「planned 批次含此评论」的活动任务，避免 agent 带着已删内容运行。必须在
	// DeleteComment 之前执行——FK ON DELETE SET NULL 否则会清空 trigger_comment_id，让那些
	// 任务在 queued 里变成孤儿。
	cancelled, cancelErr := h.TaskService.CancelTasksByTriggerComment(r.Context(), comment.ID)
	if cancelErr != nil {
		slog.Warn("cancel tasks for deleted trigger comment failed", append(logger.RequestAttrs(r), "error", cancelErr, "comment_id", commentId)...)
	}

	// 删除与其持久化事件共享同一事务。任务取消刻意在事务之前准备：删除查询可能经既有关系
	// 规则清空 trigger_comment_id。
	tx, beginErr := h.TxStarter.Begin(r.Context())
	if beginErr != nil {
		if hasIssue {
			h.retriggerCancelledTaskSurvivors(r.Context(), issue, cancelled, pgtype.UUID{})
		}
		writeError(w, http.StatusInternalServerError, "failed to delete comment")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	deleted, err := qtx.DeleteComment(r.Context(), db.DeleteCommentParams{
		ID:          comment.ID,
		WorkspaceID: comment.WorkspaceID,
	})
	var persistedEvent events.Event
	if err == nil && deleted.Changed {
		persistedEvent, err = eventoutbox.Enqueue(r.Context(), qtx,
			buildCommentDeletedEvent(comment, actorType, actorID, deleted.IssueRevision))
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil || !deleted.Changed {
		slog.Warn("delete comment failed", append(logger.RequestAttrs(r), "error", err, "changed", deleted.Changed, "comment_id", commentId)...)
		// 取消已提交但删除没有。父 issue 若仍在，就先重建完整的取消批次（含此触发），再上报
		// 存储错误或并发空操作。
		if hasIssue {
			h.retriggerCancelledTaskSurvivors(r.Context(), issue, cancelled, pgtype.UUID{})
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to delete comment")
		} else {
			writeError(w, http.StatusNotFound, "comment not found")
		}
		return
	}

	h.deleteS3Objects(r.Context(), attachmentURLs)
	slog.Info("comment deleted", append(logger.RequestAttrs(r), "comment_id", commentId, "issue_id", uuidToString(comment.IssueID))...)
	if persistedEvent.ID != "" {
		h.publishEvent(persistedEvent)
	}
	if hasIssue {
		h.retriggerCancelledTaskSurvivors(r.Context(), issue, cancelled, comment.ID)
	}
	w.WriteHeader(http.StatusNoContent)
}

// retriggerCancelledTaskSurvivors 修复来自一个「含被编辑/删除评论」批次的幸存输入。取消释放
// 了 one-pending-task 约束；其它每条评论都走常规鉴权/路由路径重放，限定在承载它的那条任务
// 所属 agent。按时间重放让幸存者合并回一个批次，并让最新真实评论重盖发起人与 connected-app
// 上下文。
func (h *Handler) retriggerCancelledTaskSurvivors(ctx context.Context, issue db.Issue, cancelled []db.AgentTaskQueue, excludedCommentID pgtype.UUID) {
	if len(cancelled) == 0 {
		return
	}
	targetsByComment := make(map[string]map[string]pgtype.UUID)
	for _, task := range cancelled {
		if !task.AgentID.Valid {
			continue
		}
		plannedCommentIDs := append([]pgtype.UUID{}, task.CoalescedCommentIds...)
		if task.TriggerCommentID.Valid {
			plannedCommentIDs = append(plannedCommentIDs, task.TriggerCommentID)
		}
		for _, commentID := range plannedCommentIDs {
			if !commentID.Valid || (excludedCommentID.Valid && commentID == excludedCommentID) {
				continue
			}
			commentKey := uuidToString(commentID)
			if targetsByComment[commentKey] == nil {
				targetsByComment[commentKey] = make(map[string]pgtype.UUID)
			}
			targetsByComment[commentKey][uuidToString(task.AgentID)] = task.AgentID
		}
	}

	comments := make([]db.Comment, 0, len(targetsByComment))
	for commentID := range targetsByComment {
		comment, err := h.Queries.GetComment(ctx, parseUUID(commentID))
		if err != nil {
			slog.Warn("retrigger cancelled comment batch: load survivor failed",
				"issue_id", uuidToString(issue.ID), "comment_id", commentID, "error", err)
			continue
		}
		if comment.IssueID != issue.ID {
			continue
		}
		comments = append(comments, comment)
	}
	sort.Slice(comments, func(i, j int) bool {
		if !comments[i].CreatedAt.Time.Equal(comments[j].CreatedAt.Time) {
			return comments[i].CreatedAt.Time.Before(comments[j].CreatedAt.Time)
		}
		return uuidToString(comments[i].ID) < uuidToString(comments[j].ID)
	})

	for i := range comments {
		comment := comments[i]
		if isNoteComment(comment.Content) {
			continue
		}
		// 平台恢复评论绕过通用 member/agent 路由。编辑/删除另一条评论取消掉了承载它的排队
		// 批次时，经持久的「委派失败恢复」路径重放，让被取消、未投递的任务不能吞掉义务。
		if service.IsDelegatedFailureRecoveryComment(comment) {
			if err := h.TaskService.DispatchDelegatedFailureRecoveryComment(ctx, comment, pgtype.UUID{}); err != nil {
				slog.Warn("retrigger cancelled comment batch: delegated failure recovery replay failed",
					"issue_id", uuidToString(issue.ID),
					"comment_id", uuidToString(comment.ID),
					"error", err,
				)
			}
			continue
		}
		var parentComment *db.Comment
		if comment.ParentID.Valid {
			if parent, err := h.Queries.GetComment(ctx, comment.ParentID); err == nil {
				parentComment = &parent
			}
		}
		actorType := comment.AuthorType
		actorID := uuidToString(comment.AuthorID)
		originatorUserID := actorID
		var delegationAuthority string
		if actorType != "member" {
			originatorUserID = uuidToString(h.TaskService.ResolveOriginatorFromTriggerComment(ctx, issue.WorkspaceID, comment.ID))
			// MUL-4857：reconcile 基于持久化评论工作，所以 autopilot 委派授权从存储的
			// comment.source_task_id 解析。
			delegationAuthority = h.autopilotDelegationAuthorityFromComment(ctx, issue, comment)
		}
		triggers, _, _ := h.computeCommentAgentTriggers(ctx, issue, comment.Content, parentComment, actorType, actorID, commentTriggerComputeOptions{
			ExcludeTriggerCommentID:            comment.ID,
			OriginatorUserID:                   originatorUserID,
			AutopilotDelegationAuthorityUserID: delegationAuthority,
			// 修改 8. 推迟到完成时才重放的评论，同样靠这条评论自己的存储血缘续接
			// 任务范围内的既定委托授权。幸存者重放的作者运行早已结束，所以走重放形态。
			TaskDelegationAuthority: h.commentTaskDelegationReplayResolver(ctx, issue, comment),
		})
		targets := targetsByComment[uuidToString(comment.ID)]
		scoped := make([]commentAgentTrigger, 0, len(targets))
		for _, trigger := range triggers {
			if _, ok := targets[uuidToString(trigger.Agent.ID)]; ok {
				scoped = append(scoped, trigger)
			}
		}
		if len(scoped) > 0 {
			h.enqueueCommentAgentTriggers(ctx, issue, comment.ID, scoped)
		}
	}
}

// loadCommentForActor 把 URL 参数 {commentId} 解析成调用方工作区里的一条评论。返回评论、
// 工作区 UUID、actor 身份与 ok。Resolve/Unresolve handler 共享这套脚手架，让工作区成员 +
// 租户守卫保持一致。任何评论（根或回复）都可被 resolve：解决根折叠整条线程；解决回复把它
// 标记为线程的结论。哪条是线程的结论是纯前端推导，后端保持一个朴素 setter。
func (h *Handler) loadCommentForActor(w http.ResponseWriter, r *http.Request) (db.Comment, string, string, string, bool) {
	commentId := chi.URLParam(r, "commentId")
	userID, ok := requireUserID(w, r)
	if !ok {
		return db.Comment{}, "", "", "", false
	}
	commentUUID, ok := parseUUIDOrBadRequest(w, commentId, "comment id")
	if !ok {
		return db.Comment{}, "", "", "", false
	}
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return db.Comment{}, "", "", "", false
	}
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return db.Comment{}, "", "", "", false
	}
	comment, err := h.Queries.GetCommentInWorkspace(r.Context(), db.GetCommentInWorkspaceParams{
		ID:          commentUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "comment not found")
		return db.Comment{}, "", "", "", false
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	return comment, workspaceID, actorType, actorID, true
}

// ResolveComment 处理 POST /api/issues/comments/:commentId/resolve：把一条评论标成「线程已
// 解决」。权限与 loadCommentForActor 相同（工作区成员）。单解决不变量：同一线程里先清掉其它
// 解决标记再设新的，两条写在同一个事务里原子完成，崩溃也不会留下两个（或零个）可见解决。
func (h *Handler) ResolveComment(w http.ResponseWriter, r *http.Request) {
	comment, _, actorType, actorID, ok := h.loadCommentForActor(w, r)
	if !ok {
		return
	}
	wasResolved := comment.ResolvedAt.Valid

	actorUUID, err := util.ParseUUID(actorID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid actor id")
		return
	}

	// 单解决不变量：一条线程至多一条已解决评论，因此解决这条必须先清掉同线程的其它解决。
	// 两条写在同一个事务里——清旧的 + 设新的原子完成，崩溃永不留下两个（或没有）可见解决。
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve comment")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	cleared, err := qtx.ClearOtherThreadResolutions(r.Context(), db.ClearOtherThreadResolutionsParams{
		TargetID:    comment.ID,
		IssueID:     comment.IssueID,
		WorkspaceID: comment.WorkspaceID,
	})
	if err != nil {
		slog.Warn("clear other thread resolutions failed", append(logger.RequestAttrs(r), "error", err, "comment_id", uuidToString(comment.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to resolve comment")
		return
	}
	persistedEvents := make([]events.Event, 0, len(cleared)+1)
	for _, clearedComment := range cleared {
		event, enqueueErr := eventoutbox.Enqueue(r.Context(), qtx,
			buildCommentMutationEvent(clearedComment, protocol.EventCommentUnresolved, actorType, actorID, 0))
		if enqueueErr != nil {
			slog.Warn("enqueue comment unresolved event failed", append(logger.RequestAttrs(r), "error", enqueueErr, "comment_id", uuidToString(clearedComment.ID))...)
			writeError(w, http.StatusInternalServerError, "failed to resolve comment")
			return
		}
		persistedEvents = append(persistedEvents, event)
	}

	updated, err := qtx.ResolveComment(r.Context(), db.ResolveCommentParams{
		ID:             comment.ID,
		ResolvedByType: pgtype.Text{String: actorType, Valid: true},
		ResolvedByID:   actorUUID,
	})
	if err != nil {
		slog.Warn("resolve comment failed", append(logger.RequestAttrs(r), "error", err, "comment_id", uuidToString(comment.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to resolve comment")
		return
	}
	if !wasResolved {
		event, enqueueErr := eventoutbox.Enqueue(r.Context(), qtx,
			buildCommentMutationEvent(updated, protocol.EventCommentResolved, actorType, actorID, 0))
		if enqueueErr != nil {
			slog.Warn("enqueue comment resolved event failed", append(logger.RequestAttrs(r), "error", enqueueErr, "comment_id", uuidToString(updated.ID))...)
			writeError(w, http.StatusInternalServerError, "failed to resolve comment")
			return
		}
		persistedEvents = append(persistedEvents, event)
	}

	if err := tx.Commit(r.Context()); err != nil {
		slog.Warn("resolve comment commit failed", append(logger.RequestAttrs(r), "error", err, "comment_id", uuidToString(comment.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to resolve comment")
		return
	}
	for _, event := range persistedEvents {
		h.publishEvent(event)
	}

	grouped := h.groupReactions(r, []pgtype.UUID{updated.ID})
	groupedAtt := h.groupAttachments(r, []pgtype.UUID{updated.ID})
	cid := uuidToString(updated.ID)
	resp := commentToResponse(updated, grouped[cid], groupedAtt[cid])

	// 重复 resolve 的空操作抑制目标事件，消费者不必重处理未变的线程（通知、日志噪音）。被清
	// 的兄弟仍会各自拿到上面的事件——那些行确实变了。
	if !wasResolved {
		slog.Info("comment resolved", append(logger.RequestAttrs(r), "comment_id", cid)...)
	}
	writeJSON(w, http.StatusOK, resp)
}

// UnresolveComment 处理 POST /api/issues/comments/:commentId/unresolve：撤掉一条评论的
// 「已解决」标记。仅在原状态是已解决时广播取消事件（幂等空操作不发事件）。
func (h *Handler) UnresolveComment(w http.ResponseWriter, r *http.Request) {
	comment, _, actorType, actorID, ok := h.loadCommentForActor(w, r)
	if !ok {
		return
	}
	wasResolved := comment.ResolvedAt.Valid

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to unresolve comment")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	updated, err := qtx.UnresolveComment(r.Context(), comment.ID)
	if err != nil {
		slog.Warn("unresolve comment failed", append(logger.RequestAttrs(r), "error", err, "comment_id", uuidToString(comment.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to unresolve comment")
		return
	}
	var persistedEvent events.Event
	if wasResolved {
		persistedEvent, err = eventoutbox.Enqueue(r.Context(), qtx,
			buildCommentMutationEvent(updated, protocol.EventCommentUnresolved, actorType, actorID, 0))
		if err != nil {
			slog.Warn("enqueue comment unresolved event failed", append(logger.RequestAttrs(r), "error", err, "comment_id", uuidToString(comment.ID))...)
			writeError(w, http.StatusInternalServerError, "failed to unresolve comment")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to unresolve comment")
		return
	}

	grouped := h.groupReactions(r, []pgtype.UUID{updated.ID})
	groupedAtt := h.groupAttachments(r, []pgtype.UUID{updated.ID})
	cid := uuidToString(updated.ID)
	resp := commentToResponse(updated, grouped[cid], groupedAtt[cid])

	if wasResolved {
		slog.Info("comment unresolved", append(logger.RequestAttrs(r), "comment_id", cid)...)
		h.publishEvent(persistedEvent)
	}
	writeJSON(w, http.StatusOK, resp)
}
