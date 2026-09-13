// 本文件实现 agent 的两个读频道历史的命令：`multica chat history`（频道概览）与
// `multica chat thread [id]`（单线程消息）。会话绑定哪个 IM 频道（目前只有
// Slack）由服务端钉死，agent 永远看不到按平台区分的 API；未绑定频道的会话（Web
// 聊天、飞书/企微/钉钉）回退读 Multica 自己的 chat_message 转录。鉴权只认
// task-scoped token（X-Actor-Source=task_token），防成员伪造 X-Task-ID 越权读
// 别人的会话历史（MUL-3871）。
package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/slack"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ChatChannelHistoryReader 读取某 chat 会话绑定的 IM 频道历史。Slack reader
// （slack.History）满足它；未来平台注册自己的实现。两个操作支撑两个 agent 命令：
// ChannelOverview 是频道目录（`multica chat history`），Thread 读单个线程的消息
// （`multica chat thread [id]`）。两者都在服务端限定到会话自己的频道
// （MUL-3871）。
type ChatChannelHistoryReader interface {
	ChannelOverview(ctx context.Context, chatSessionID pgtype.UUID, opts channel.HistoryOptions) (channel.HistoryPage, error)
	Thread(ctx context.Context, chatSessionID pgtype.UUID, threadID string, opts channel.HistoryOptions) (channel.HistoryPage, error)
}

// ChatChannelHistoryResponse 是两个命令的统一负载——无论哪个频道支撑会话，都是
// 同一形状，agent 永远看不到某个平台专属的 API。
type ChatChannelHistoryResponse struct {
	ChannelType string `json:"channel_type"`
	// ThreadID 在读线程时设置：这些消息属于哪个线程。
	ThreadID   string                   `json:"thread_id,omitempty"`
	Messages   []channel.HistoryMessage `json:"messages"`
	NextCursor string                   `json:"next_cursor,omitempty"`
	// Note 解释空结果（例如会话没有频道支撑），让 agent 得到明确答复而不是光秃秃
	// 的空列表。
	Note string `json:"note,omitempty"`
}

// GetChatChannelHistory 服务 `multica chat history`——频道概览：最近的顶层消息，
// 每条线程标注其 id + 回复数（不含线程内容）。agent 用 `multica chat thread`
// 下钻到具体线程。
//
// 未绑定 IM 频道的 chat 会话没有可读的频道概览——它的历史就是 chat_message 表
// （web 聊天、飞书）。对这种会话，本端点回退返回那份转录，让 agent 在丢失恢复后
// 仍能重建对话，而不是被告知什么都读不到（见 execenv 的连续性提示切分）。
func (h *Handler) GetChatChannelHistory(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.chatHistorySession(w, r)
	if !ok {
		return
	}
	var page channel.HistoryPage
	var err error
	if h.SlackHistory == nil {
		// 未配置 Slack 集成，则会话不可能是 Slack 承载：直接给存储的转录。否则
		// 无 Slack 的部署——正是本功能瞄准的对象——会死在「没有频道集成」的提示
		// 上，永远到不了转录。
		page, err = h.chatMessageHistory(r, scope)
	} else {
		page, err = h.SlackHistory.ChannelOverview(r.Context(), scope.sessionID, scope.historyOptions(r))
		if errors.Is(err, slack.ErrNoSlackSession) {
			// 非 Slack 承载：改读会话自己的存储转录。
			page, err = h.chatMessageHistory(r, scope)
		}
	}
	h.respondChatHistory(w, r, scope.sessionID, page, err)
}

// chatMessageHistory 把会话自己存储的转录（chat_message）作为 channel.HistoryPage
// 读出，旧在前，遵守共享的 ?limit / ?before 分页契约。它支撑无 IM 频道（web 聊天、
// 飞书、企微、钉钉）会话的 `multica chat history`——这些会话的历史只存在于
// Multica，没有平台可回读。它按前端消息列表同一个 (created_at, id) 游标翻页，让
// agent 能一路倒读长会话，而不必每轮重读最近窗口。
func (h *Handler) chatMessageHistory(r *http.Request, scope chatHistoryScope) (channel.HistoryPage, error) {
	limit := clampTranscriptLimit(parseHistoryLimit(r.URL.Query().Get("limit")))
	beforeCreatedAt, beforeID := parseTranscriptCursor(r.URL.Query().Get("before"))
	var messages []db.ChatMessage
	var err error
	if scope.contextRevision.Valid {
		messages, err = h.Queries.ListChatMessagesPageForChannelContext(r.Context(), db.ListChatMessagesPageForChannelContextParams{
			ChatSessionID: scope.sessionID, ChannelContextRevision: scope.contextRevision,
			PageLimit: int32(limit), BeforeCreatedAt: beforeCreatedAt, BeforeID: beforeID,
		})
	} else {
		messages, err = h.Queries.ListChatMessagesPage(r.Context(), db.ListChatMessagesPageParams{
			ChatSessionID: scope.sessionID, Limit: int32(limit),
			BeforeCreatedAt: beforeCreatedAt, BeforeID: beforeID,
		})
	}
	if err != nil {
		return channel.HistoryPage{}, err
	}
	// ListChatMessagesPage 返回新在前；频道契约是旧在前，因此逆序输出。
	out := make([]channel.HistoryMessage, 0, len(messages))
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		role := channel.HistoryRoleUser
		if m.Role == "assistant" {
			role = channel.HistoryRoleAssistant
		}
		out = append(out, channel.HistoryMessage{
			ID:     uuidToString(m.ID),
			Role:   role,
			Text:   m.Content,
			Author: transcriptAuthor(role),
			TS:     m.CreatedAt.Time.UTC().Format(time.RFC3339Nano),
		})
	}
	// 标明转录来自哪个平台。HistoryPage.ChannelType 只在「会话未绑任何频道」时
	// 才文档化为空，所以这里留空会告诉飞书/企微/钉钉的 agent 它在一个纯 web
	// 聊天里——并且会与下面空读路径矛盾，后者对同一个会话会如实上报所绑平台。
	channelType, err := h.sessionChannelType(r.Context(), scope.sessionID)
	if err != nil {
		return channel.HistoryPage{}, fmt.Errorf("%w: %w", errChannelBindingRead, err)
	}
	page := channel.HistoryPage{ChannelType: channelType, Messages: out}
	// 满页返回时给出游标，让 agent 能继续翻更旧的消息（镜像 Slack reader 的
	// "可能还有更多"信号）。
	if len(messages) == limit && len(out) > 0 {
		oldest := messages[len(messages)-1]
		page.NextCursor = transcriptCursor(oldest.CreatedAt.Time, oldest.ID)
	}
	return page, nil
}

// 转录分页边界，镜像 Slack reader 的 clamp，让 agent 不能把长会话的整份转录倒进
// 自己的上下文。
const (
	defaultTranscriptLimit = 30
	maxTranscriptLimit     = 50
)

func clampTranscriptLimit(n int) int {
	if n <= 0 {
		return defaultTranscriptLimit
	}
	if n > maxTranscriptLimit {
		return maxTranscriptLimit
	}
	return n
}

// transcriptCursor 把 (created_at, id) 对编码进频道契约用的不透明 ?before 游标。
// 转录与前端消息列表按同一个 (created_at, id) 元组分页，所以游标必须带两半；
// RFC3339Nano 让时间戳无损且对查看它的 agent 可读。
func transcriptCursor(createdAt time.Time, id pgtype.UUID) string {
	return createdAt.UTC().Format(time.RFC3339Nano) + "|" + uuidToString(id)
}

// parseTranscriptCursor 把 ?before 游标拆回 ListChatMessagesPage 分页所用的
// (created_at, id) 元组。游标缺失或畸形返回零值，让读取从最近消息开始。
func parseTranscriptCursor(before string) (pgtype.Timestamptz, pgtype.UUID) {
	ts, id, ok := strings.Cut(before, "|")
	if !ok {
		return pgtype.Timestamptz{}, pgtype.UUID{}
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return pgtype.Timestamptz{}, pgtype.UUID{}
	}
	uid, err := util.ParseUUID(id)
	if err != nil {
		return pgtype.Timestamptz{}, pgtype.UUID{}
	}
	return pgtype.Timestamptz{Time: t, Valid: true}, uid
}

// transcriptAuthor 用频道词汇（"Bot" / "User"）而非原始角色串标记 chat_message
// 行，让读转录的 agent 看到与 Slack 路径一致的作者类别，而不是字面 "user" /
// "assistant"。
func transcriptAuthor(role channel.HistoryRole) string {
	if role == channel.HistoryRoleAssistant {
		return "Bot"
	}
	return "User"
}

// GetChatThread 服务 `multica chat thread [id]`——单个线程的消息。带 ?id 读指定
// 线程；不带则读会话当前所在的线程。频道由服务端钉在会话上，所以 id 只是
// 频道内的定位符。
func (h *Handler) GetChatThread(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.chatHistorySession(w, r)
	if !ok {
		return
	}
	if h.SlackHistory == nil {
		h.writeNoChannelIntegration(w)
		return
	}
	threadID := r.URL.Query().Get("id")
	page, err := h.SlackHistory.Thread(r.Context(), scope.sessionID, threadID, scope.historyOptions(r))
	h.respondChatHistory(w, r, scope.sessionID, page, err)
}

// chatHistoryScope 携带一次鉴权通过的请求的会话与频道上下文参数。
// 它只凭 task-scoped token 鉴权：中间件把 token 的任务写进
// X-Actor-Source=task_token + X-Task-ID（普通 JWT / mul_ PAT 的 X-Actor-Source
// 为空，且不会剥掉客户端伪造的 X-Task-ID），因此要求 task-token actor 是承重的——
// 否则成员可伪造 X-Task-ID 读别人的会话历史。
type chatHistoryScope struct {
	sessionID       pgtype.UUID
	contextRevision pgtype.Int8
	historyStart    string
	historyEnd      string
	boundaryPending bool
}

func (s chatHistoryScope) historyOptions(r *http.Request) channel.HistoryOptions {
	opts := historyOptionsFrom(r)
	opts.BoundaryPending = s.boundaryPending
	opts.After = s.historyStart
	opts.Until = s.historyEnd
	if s.contextRevision.Valid {
		opts.ContextRevision = s.contextRevision.Int64
	}
	return opts
}

func (h *Handler) chatHistorySession(w http.ResponseWriter, r *http.Request) (chatHistoryScope, bool) {
	if r.Header.Get("X-Actor-Source") != "task_token" {
		writeError(w, http.StatusForbidden, "chat history is only available from within an agent task")
		return chatHistoryScope{}, false
	}
	taskIDHeader := r.Header.Get("X-Task-ID")
	if taskIDHeader == "" {
		writeError(w, http.StatusBadRequest, "missing task context")
		return chatHistoryScope{}, false
	}
	taskUUID, err := util.ParseUUID(taskIDHeader)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid task id")
		return chatHistoryScope{}, false
	}
	task, err := h.Queries.GetAgentTask(r.Context(), taskUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return chatHistoryScope{}, false
	}
	if !task.ChatSessionID.Valid {
		writeError(w, http.StatusBadRequest, "this task is not a chat task")
		return chatHistoryScope{}, false
	}
	// 纵深防御：加载会话并确认它住在 token 盖过章的工作区里。token→task 绑定已
	// 保证 agent 只能够到自己的任务；这一步让未来某次接线回归也能失败关闭。
	session, err := h.Queries.GetChatSession(r.Context(), task.ChatSessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "chat session not found")
		return chatHistoryScope{}, false
	}
	if ws := ctxWorkspaceID(r.Context()); ws != "" && uuidToString(session.WorkspaceID) != ws {
		writeError(w, http.StatusForbidden, "chat session does not belong to this workspace")
		return chatHistoryScope{}, false
	}
	scope := chatHistoryScope{sessionID: task.ChatSessionID, contextRevision: task.ChannelContextRevision}
	if task.ChannelContextRevision.Valid {
		generation, err := h.Queries.GetChannelChatContextGeneration(r.Context(), db.GetChannelChatContextGenerationParams{
			ChatSessionID: task.ChatSessionID, Revision: task.ChannelContextRevision.Int64,
		})
		if err != nil {
			writeError(w, http.StatusNotFound, "chat context generation not found")
			return chatHistoryScope{}, false
		}
		scope.historyStart = generation.HistoryStartMessageID.String
		scope.historyEnd = generation.HistoryEndMessageID.String
		scope.boundaryPending = generation.HistoryBoundaryPending
	}
	return scope, true
}

// respondChatHistory 写共享响应：会话非频道承载时给 200 + note，真实读取失败给
// 502，否则给页面本身。
func (h *Handler) respondChatHistory(w http.ResponseWriter, r *http.Request, sessionID pgtype.UUID, page channel.HistoryPage, err error) {
	if err != nil {
		if errors.Is(err, slack.ErrNoSlackSession) {
			// 绑定读一次、派生出两个字段。读两次会让归档插在它们之间，产出一个
			// channel_type 点名某平台、note 却说没有频道的自相矛盾响应。
			channelType, bindingErr := h.sessionChannelType(r.Context(), sessionID)
			if bindingErr != nil {
				slog.Error("chat session channel binding read failed", append(logger.RequestAttrs(r),
					"error", bindingErr, "chat_session_id", uuidToString(sessionID))...)
				writeError(w, http.StatusInternalServerError, "failed to read chat session channel binding")
				return
			}
			writeJSON(w, http.StatusOK, ChatChannelHistoryResponse{
				ChannelType: channelType,
				Messages:    []channel.HistoryMessage{},
				Note:        noHistoryNote(channelType),
			})
			return
		}
		if errors.Is(err, errChannelBindingRead) {
			slog.Error("chat session channel binding read failed", append(logger.RequestAttrs(r),
				"error", err, "chat_session_id", uuidToString(sessionID))...)
			writeError(w, http.StatusInternalServerError, "failed to read chat session channel binding")
			return
		}
		slog.Error("chat channel history read failed", append(logger.RequestAttrs(r),
			"error", err, "chat_session_id", uuidToString(sessionID))...)
		writeError(w, http.StatusBadGateway, "failed to read channel history")
		return
	}
	messages := page.Messages
	if messages == nil {
		messages = []channel.HistoryMessage{}
	}
	writeJSON(w, http.StatusOK, ChatChannelHistoryResponse{
		ChannelType: page.ChannelType,
		ThreadID:    page.ThreadID,
		Messages:    messages,
		NextCursor:  page.NextCursor,
	})
}

func (h *Handler) writeNoChannelIntegration(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, ChatChannelHistoryResponse{
		Messages: []channel.HistoryMessage{},
		Note:     "No chat channel integration is configured on this server.",
	})
}

// historyOptionsFrom 读共享的 ?limit / ?before 分页参数。
func historyOptionsFrom(r *http.Request) channel.HistoryOptions {
	return channel.HistoryOptions{
		Limit:  parseHistoryLimit(r.URL.Query().Get("limit")),
		Before: r.URL.Query().Get("before"),
	}
}

// parseHistoryLimit 读 ?limit 查询参数，忽略垃圾值（reader 会 clamp 范围）。
// 0 表示「用 reader 的默认」。
func parseHistoryLimit(raw string) int {
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// noHistoryNote 用 agent 能行动的语言解释一次空读。它是调用方已解析出的频道类型
// 的纯函数，因此 note 与响应的 channel_type 不可能互相矛盾。
//
// reader 目前只有 Slack，所以其它每个平台都落到这里——note 曾写「这段对话没有
// 连接聊天频道」，对企微/飞书/钉钉会话纯属错误。被告知自己在一个纯 web 会话的
// agent，与被告知「在一个看不到积压的群组里」的 agent，对「谁能看到我的答复」会
// 得出不同推理——这个差别不值得撒谎。
func noHistoryNote(channelType string) string {
	if channelType == "" {
		return "This conversation is not connected to a chat channel, so there is no channel history to read."
	}
	return "This conversation is on " + channelType + ", whose backlog this server cannot read. You can see the messages addressed to you in this session, but not the rest of the room."
}

// errChannelBindingRead 标记一次「MESSAGES 已取到、但频道绑定读不到」的转录读。
// 它不是上游频道故障，所以 respondChatHistory 像空读路径回应同类故障那样回应它
// ——一个原因一个状态码，与会话是否恰好有消息无关。
var errChannelBindingRead = errors.New("chat session channel binding read failed")

// sessionChannelType 点名会话背后的平台，没有则返回 ""。刻意平台无关：在这里做
// 按平台的查找，会在下次新增频道时失明——上面那条 note 正是这样变错的。
//
// 只有「没有这行」才意味着「没有频道」。任何其它失败都是我们无法判断，在这里
// 答 "" 会把本改动要移除的那条 note 递给 agent——企微或飞书会话因为一次连接
// 抖动，就在 200 上被告知自己是 web-only。调用方宁可如实上报该失败也不猜，正如
// 归档路径对同一次读拒绝猜测。
func (h *Handler) sessionChannelType(ctx context.Context, sessionID pgtype.UUID) (string, error) {
	if h.Queries == nil || !sessionID.Valid {
		return "", nil
	}
	binding, err := h.Queries.GetChannelChatSessionBindingBySessionAny(ctx, sessionID)
	switch {
	case err == nil:
		return binding.ChannelType, nil
	case errors.Is(err, pgx.ErrNoRows):
		return "", nil
	default:
		return "", err
	}
}
