// 本文件实现聊天的「尽力而为」LLM 自动起标题（MUL-4295）：用户发出首条消息后，
// 异步调 LLM 把开场压缩成简短标题，再经 compare-and-swap 写回会话。非阻塞
// （绝不让发送等待）、静默回退（LLM 未配置或失败就保留原标题，不打扰用户）、
// 不覆盖（生成期间用户手动改名绝不会被盖掉）。channel 引擎的确定性兜底标题与
// 各字段清洗规则也在此。注意：提示词文本刻意不含任何具体语言/CJK（MUL-5689）。
package handler

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// chatTitleGenTimeout 给整段尽力而为的起标题（LLM 调用 + CAS 写入）设上界。它跑
// 在脱离的 background context 上——与即时返回的原始 HTTP 请求解耦——所以这是唯一
// 防止上游挂死时 goroutine 滞留的东西。刻意保持很短：聊天标题只是锦上添花，
// 不值得为它钉住 goroutine 一分钟。
const chatTitleGenTimeout = 20 * time.Second

// ChannelChatStarted 广播列表作废元数据，不改变任何客户端当前的导航。显式新建
// 的空 Chat 已提交。
func (h *Handler) ChannelChatStarted(event engine.ChannelChatStartedEvent) {
	ws, user, session := uuidToString(event.WorkspaceID), uuidToString(event.CreatorID), uuidToString(event.SessionID)
	h.publishChat(protocol.EventChatSessionCreated, ws, "member", user, session, protocol.ChatSessionCreatedPayload{
		WorkspaceID: ws, ChatSessionID: session, AgentID: uuidToString(event.AgentID), CreatorID: user, Title: event.Title,
		ChannelSource: protocol.ChatSessionChannelSource{
			ChannelType: string(event.ChannelType), InstallationID: uuidToString(event.InstallationID), RouteRevision: event.RouteRevision,
		},
		IsCurrentChannelRoute: true,
	})
}

// ChannelChatTitleInitialized 立即广播确定性的首条消息 / 媒体兜底标题。LLM 起标题
// 是可选的，因此客户端绝不能依赖它随后的 CAS 更新才能看到已提交的标题。
func (h *Handler) ChannelChatTitleInitialized(workspaceID, creatorID, sessionID pgtype.UUID, title string) {
	h.publishChat(protocol.EventChatSessionUpdated, uuidToString(workspaceID), "member", uuidToString(creatorID), uuidToString(sessionID), protocol.ChatSessionUpdatedPayload{
		ChatSessionID: uuidToString(sessionID),
		Title:         title,
	})
}

// GenerateChannelChatTitle 在共享引擎持久化好确定性的首条消息兜底标题后，复用
// 既有的 LLM + CAS 起标题路径。
func (h *Handler) GenerateChannelChatTitle(workspaceID, creatorID, sessionID pgtype.UUID, currentTitle, sourceText string) {
	h.maybeGenerateChatTitleAsync(uuidToString(workspaceID), uuidToString(creatorID), sessionID, currentTitle, sourceText)
}

// chatTitleSystemPrompt 指示模型把一段对话的开场压缩成简短、与语言匹配的标题。
// 规则镜像 MUL-4295 的验收标准：不带引号、不带句尾标点、不带标签前缀、跟随对话
// 的语言。sanitizeChatTitle 在模型无视规则时防御性地重施加这些规则。
//
// 这段文本刻意不提任何具体语言、不含 CJK。把某种语言点名的提示词——哪怕只是
// 作为格式示例——会被读成「允许用该语言作答」，这正是 quick actions 曾在英文对话
// 里冒出中文药丸的原因（MUL-5689）。它过去枚举的标签前缀仍由 chatTitleLabelPrefixes
// 真实剥除，那个保证才属于那里。
const chatTitleSystemPrompt = `You write a very short title that summarizes the topic of a chat conversation, given the user's opening message.

Rules:
- Output ONLY the title text — nothing else, no explanation.
- Keep it short: a few words, ideally under 8, never a full sentence.
- Write the title in the SAME language as the user's message, and in no other.
- Do NOT wrap the title in quotes or brackets.
- Do NOT prefix it with a label such as "Title:", in any language.
- Do NOT end with a period or any trailing punctuation.`

// maybeGenerateChatTitleAsync 为某 chat 会话触发尽力而为的 LLM 起标题，然后立即
// 返回。它是 SendChatMessage 首条用户消息路径接进来的入口。
//
// MUL-4295 的设计约束：
//   - 非阻塞：绝不让用户的发送 / 首个响应等待。工作在脱开的 goroutine、跑在
//     context.Background() 上（SendChatMessage 一返回，请求 context 即被取消）。
//   - 静默回退：LLM 层未配置（自托管没 key）或调用失败时什么都不做，保留原标题
//     派生的首条消息标题——不向用户冒任何错误。
//   - 不覆盖：更新是相对 currentTitle 的 compare-and-swap，生成期间落下的手动
//     改名绝不会被盖掉。
//
// currentTitle 是触发时点观察到的会话标题（默认/原标题）。sourceText 是用户的
// 首条消息，模型把它压缩成标题。
func (h *Handler) maybeGenerateChatTitleAsync(workspaceID, userID string, sessionID pgtype.UUID, currentTitle, sourceText string) {
	// 在派生 goroutine 之前先短路 LLM 层被禁用（自托管且没有
	// MULTICA_LLM_API_KEY / MULTICA_LLM_BASE_URL）的情况：原标题原样保留，恰好
	// 与功能上线前行为一致。
	if h.LLM == nil || !h.LLM.Enabled() {
		return
	}
	if strings.TrimSpace(sourceText) == "" {
		return
	}

	go func() {
		// 恐慌兜底：该 goroutine 与 HTTP 请求脱开，chi 的 Recoverer 中间件不在
		// 调用栈里。下方任何 panic（GenerateText、清洗、DB 写、广播）都会崩掉整个
		// 服务进程。这是尽力而为的锦上添花——吞掉 panic、记日志、保留原标题。
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("chat title generation panicked; keeping original title",
					"session_id", uuidToString(sessionID),
					"panic", rec,
				)
			}
		}()

		ctx, cancel := context.WithTimeout(context.Background(), chatTitleGenTimeout)
		defer cancel()

		updated, applied, err := h.generateChatSessionTitle(ctx, sessionID, currentTitle, sourceText)
		if err != nil {
			// 超时、上游 4xx/5xx、空 choices 等。记日志即弃：发送已成功，原标题
			// 保持。
			slog.Warn("chat title generation failed; keeping original title",
				"session_id", uuidToString(sessionID),
				"error", err,
			)
			return
		}
		if !applied {
			// 要么模型没产出可用结果，要么标题在我们眼皮底下变了（手动改名 /
			// 已被自动起过标题）。无需推送。
			return
		}

		// 复用既有的 chat:session_updated 实时通道，让前端就地刷新标题——与手动
		// 改名完全一致。
		resolvedSessionID := uuidToString(updated.ID)
		h.publishChat(protocol.EventChatSessionUpdated, workspaceID, "member", userID, resolvedSessionID, protocol.ChatSessionUpdatedPayload{
			ChatSessionID: resolvedSessionID,
			Title:         updated.Title,
			UpdatedAt:     timestampToString(updated.UpdatedAt),
		})
	}()
}

// generateChatSessionTitle 是起标题的同步核心：调 LLM、清洗输出、再以
// compare-and-swap 写到会话上。与 goroutine 包装分离，便于直接做单元测试。
//
// 返回契约：
//   - (session, true, nil)： 已生成并写入新标题。
//   - (zero, false, nil)：   生成没产出可用结果，或标题自 currentTitle 被观察后
//     已变化（CAS miss / 手动改名）——两者都是非错误「别管它」的结果。
//   - (zero, false, err)：   LLM 层被禁用或调用失败，或 CAS 写撞上真实 DB 错误。
//     调用方把它当尽力而为，保留原标题。
func (h *Handler) generateChatSessionTitle(ctx context.Context, sessionID pgtype.UUID, currentTitle, sourceText string) (db.ChatSession, bool, error) {
	// GenerateText 在 model == "" 时隐式用 DefaultModel()：部署配置
	// MULTICA_LLM_DEFAULT_MODEL（或内置 gpt-5.6-luna 兜底）——前端不穿透任何
	// model。
	raw, err := h.LLM.GenerateText(ctx, "", chatTitleSystemPrompt, sourceText)
	if err != nil {
		return db.ChatSession{}, false, err
	}

	title := sanitizeChatTitle(raw)
	if title == "" {
		// 模型只返回了引号/标点/空白——当作「无可用标题」并静默回退原标题。
		return db.ChatSession{}, false, nil
	}

	updated, err := h.Queries.UpdateChatSessionTitleIfCurrent(ctx, db.UpdateChatSessionTitleIfCurrentParams{
		ID:            sessionID,
		ExpectedTitle: currentTitle,
		NewTitle:      title,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 自观察到 currentTitle 后标题已变——手动改名或并发写者赢了。
			// 不覆盖它。
			return db.ChatSession{}, false, nil
		}
		return db.ChatSession{}, false, err
	}
	return updated, true, nil
}

// chatTitleLabelPrefixes 是模型有时无视提示词仍前置的标签。大小写不敏感、只在
// 最开头匹配。
var chatTitleLabelPrefixes = []string{
	"title:", "title：",
	"标题:", "标题：",
	"题目:", "题目：",
	"主题:", "主题：",
}

// sanitizeChatTitle 无论模型把提示词遵循得多好，都防御性地强制标题格式规则：
// 折叠空白、剥掉开头的 "Title:" / "标题：" 之类标签、移除包围的引号/括号对、
// 去掉句尾句读、并把长度硬顶到 chatSessionTitleMaxLen 个 rune（与手动改名端点
// 同一上限）。什么都不剩时返回 ""。
//
// 前缀剥离、包装剥离与句尾标点修剪全部跑在同一个循环里，直到字符串不再变化。
// 它们会相互作用：模型可能嵌套（例如 `"Title: Fix login".` 或 `「标题：修复登录问
// 题」。`）——句尾的 `.` / `。` 让闭合包装器无法被识别，进而藏住被禁止的前缀。
// 把三者迭代到不动点，就能按任意顺序把它们剥干净。
func sanitizeChatTitle(raw string) string {
	// 把所有内部空白（包括模型可能吐出的换行/制表）折叠成单空格，让多行回复
	// 变成干净的一行。
	s := strings.TrimSpace(strings.Join(strings.Fields(raw), " "))
	if s == "" {
		return ""
	}

	// 交替剥离前置标签前缀、一层包围的引号/括号、句尾句读标点，直到三者都不再
	// 产生变化。字符串只会越来越短，因此必然终止。
	for {
		before := s
		s = strings.TrimSpace(stripChatTitleLabelPrefix(s))
		s = stripSurroundingQuotes(s)
		// ASCII + 常见 CJK/全角句读。句尾修剪放在循环内，这样去掉句尾的 "." /
		// "。" 会让闭合包装器（以及它藏住的前缀）在下一轮重新暴露。
		s = strings.TrimSpace(strings.TrimRight(s, ".。!！?？,，;；:：、 "))
		if s == before || s == "" {
			break
		}
	}
	if s == "" {
		return ""
	}

	// 按 rune 数硬顶，匹配手动改名的上限。
	if runes := []rune(s); len(runes) > chatSessionTitleMaxLen {
		s = strings.TrimSpace(string(runes[:chatSessionTitleMaxLen]))
	}
	return s
}

// stripChatTitleLabelPrefix 存在时剥掉一个前置标签前缀（"Title:"、"标题："……），
// 大小写不敏感。无匹配时原样返回。
func stripChatTitleLabelPrefix(s string) string {
	lower := strings.ToLower(s)
	for _, p := range chatTitleLabelPrefixes {
		if strings.HasPrefix(lower, p) {
			return s[len(p):]
		}
	}
	return s
}

// chatTitleQuotePairs 把开引号/括号映射到它的闭合伙伴。
var chatTitleQuotePairs = map[rune]rune{
	'"':  '"',
	'\'': '\'',
	'`':  '`',
	'“':  '”',
	'‘':  '’',
	'「':  '」',
	'『':  '』',
	'《':  '》',
	'（':  '）',
	'(':  ')',
	'【':  '】',
	'[':  ']',
}

// stripSurroundingQuotes 移除整串被成对包围的引号/括号，遇到嵌套包装会反复剥离。
func stripSurroundingQuotes(s string) string {
	for {
		runes := []rune(s)
		if len(runes) < 2 {
			return s
		}
		closer, ok := chatTitleQuotePairs[runes[0]]
		if !ok || runes[len(runes)-1] != closer {
			return s
		}
		s = strings.TrimSpace(string(runes[1 : len(runes)-1]))
		if s == "" {
			return s
		}
	}
}
