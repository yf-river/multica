// history.go：按需拉读 Slack 会话历史的读取器——统一命令 `multica chat history`
// （频道总览）与 `multica chat thread [id]`（单条线程）的拉取半边（MUL-3871）。
//
// 两条命令都锁死在该会话【自己】的频道上：频道由服务端按绑定解析、绝不采纳
// agent 传进来的频道，线程 id 只是「频道内定位器」。没有 Slack 绑定的会话返回
// ErrNoSlackSession，让命令在非 Slack 会话上优雅地给空结果而不是报错。
//
// 拉回来的原始 Slack 消息还要过三道净化关：只保留本代路由归属内的 bot 消息
// （filterRouteGeneration / filterContextGeneration，防止跨代上下文串台），把
// 富文本/附件/卡片正文拍平成纯文本（flattenSlackText 一族），再统一排序并补全
// 发送者昵称（normalizePage / resolveUserNames）。
package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/slack-go/slack"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ErrNoSlackSession 表示该聊天会话没有 Slack 频道绑定——它是飞书或纯 Web 的
// 会话。调用方把它当成「空结果」而不是失败来展示，让 `multica chat history` /
// `multica chat thread` 命令在非 Slack 会话上也能答得优雅。
var ErrNoSlackSession = errors.New("slack: session has no slack channel binding")

const (
	// defaultHistoryLimit 是调用方不指定分页大小时的默认页大小。
	defaultHistoryLimit = 20
	// maxHistoryLimit 封顶单页大小，避免一次拉取把无界的完整聊天记录灌进 agent
	// 的上下文。
	maxHistoryLimit = 50
)

// historyQueries 是读取器需要的生成查询子集。
type historyQueries interface {
	GetChannelChatSessionBindingBySession(ctx context.Context, arg db.GetChannelChatSessionBindingBySessionParams) (db.ChannelChatSessionBinding, error)
	GetChannelInstallation(ctx context.Context, arg db.GetChannelInstallationParams) (db.ChannelInstallation, error)
	ListChannelOutboundMessageIDsForContext(ctx context.Context, arg db.ListChannelOutboundMessageIDsForContextParams) ([]string, error)
	ListChannelOutboundMessagesByIDs(ctx context.Context, arg db.ListChannelOutboundMessagesByIDsParams) ([]db.ChannelOutboundMessage, error)
}

// historyClient 是读取器要调的那一小块 slack-go Web API。真 *slack.Client 满足
// 它；测试注入假实现。
type historyClient interface {
	GetConversationHistoryContext(ctx context.Context, params *slack.GetConversationHistoryParameters) (*slack.GetConversationHistoryResponse, error)
	GetConversationRepliesContext(ctx context.Context, params *slack.GetConversationRepliesParameters) ([]slack.Message, bool, string, error)
	GetUsersInfoContext(ctx context.Context, users ...string) (*[]slack.User, error)
}

// History 按需读取 Slack 会话——统一 `multica chat history`（频道总览）与
// `multica chat thread [id]`（单条线程）命令的拉取侧（MUL-3871）。两条命令都
// 只在该会话【自己】的频道内工作：频道是服务端按绑定解析出来的、从不采用 agent
// 给的频道，所以线程 id 充其量只是「频道内定位器」。没有 Slack 绑定的会话返回
// ErrNoSlackSession。
type History struct {
	q         historyQueries
	decrypt   Decrypter
	logger    *slog.Logger
	newClient func(botToken string) historyClient
}

// NewHistory 基于生成查询与 bot 令牌解密器构造读取器（接线时解密器由 box.Open
// 提供）。
func NewHistory(q historyQueries, decrypt Decrypter, logger *slog.Logger) *History {
	if logger == nil {
		logger = slog.Default()
	}
	h := &History{q: q, decrypt: decrypt, logger: logger}
	h.newClient = func(botToken string) historyClient {
		// 读历史只需要 bot 令牌；应用级令牌是给入站 Socket Mode 连接用的
		// （slack_channel.go）。
		return slack.New(botToken)
	}
	return h
}

// slackTarget 是解析出的、按会话维度的读取上下文：一个 bot 令牌客户端，外加
// 该会话钉住的频道和它自己的线程根。
type slackTarget struct {
	client          historyClient
	installationID  pgtype.UUID
	bindingID       pgtype.UUID
	channelID       string
	threadRoot      string // 会话自己的线程根（私聊为空）
	botUserID       string
	historyStart    string
	historyEnd      string
	boundaryPending bool
}

// resolve 把 chat_session 映射到它的 Slack 频道 + bot 客户端。频道在这里由服务
// 端推导、绝不接受调用方传入——这正是 `multica chat thread <id>` 的安全边界
// （agent 只提供一个频道内的线程定位符）。
func (h *History) resolve(ctx context.Context, chatSessionID pgtype.UUID) (slackTarget, error) {
	binding, err := h.q.GetChannelChatSessionBindingBySession(ctx, db.GetChannelChatSessionBindingBySessionParams{
		ChatSessionID: chatSessionID,
		ChannelType:   string(TypeSlack),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return slackTarget{}, ErrNoSlackSession
		}
		return slackTarget{}, fmt.Errorf("lookup slack chat binding: %w", err)
	}
	inst, err := h.q.GetChannelInstallation(ctx, db.GetChannelInstallationParams{
		ID:          binding.InstallationID,
		ChannelType: string(TypeSlack),
	})
	if err != nil {
		return slackTarget{}, fmt.Errorf("load slack installation: %w", err)
	}
	if inst.Status != "active" {
		return slackTarget{}, ErrNoSlackSession // 安装已被吊销：没有可读的东西
	}
	creds, err := decodeCredentials(inst.Config, h.decrypt)
	if err != nil {
		return slackTarget{}, fmt.Errorf("decode slack credentials: %w", err)
	}
	channelID, threadRoot := historyTarget(binding)
	return slackTarget{
		client:          h.newClient(creds.BotToken),
		installationID:  binding.InstallationID,
		bindingID:       binding.ID,
		channelID:       channelID,
		threadRoot:      threadRoot,
		botUserID:       creds.BotUserID,
		historyStart:    binding.HistoryStartMessageID.String,
		historyEnd:      binding.HistoryEndMessageID.String,
		boundaryPending: binding.HistoryBoundaryPending,
	}, nil
}

// ChannelOverview 返回频道最近的顶层消息（从旧到新），每条开线程的消息都标上
// 线程 id + 回复数。它【不】展开线程内容——这是 agent 用来「找线程」的目录，找到
// 后用 `multica chat thread <id>` 钻进去。支撑 `multica chat history` 命令。
func (h *History) ChannelOverview(ctx context.Context, chatSessionID pgtype.UUID, opts channel.HistoryOptions) (channel.HistoryPage, error) {
	t, err := h.resolve(ctx, chatSessionID)
	if err != nil {
		return channel.HistoryPage{}, err
	}
	start, end := historyBounds(opts, t)
	if opts.BoundaryPending || t.boundaryPending || historyCursorReachedStart(opts.Before, start) {
		return channel.HistoryPage{ChannelType: string(TypeSlack)}, nil
	}
	limit := clampHistoryLimit(opts.Limit)
	latest, oldest, inclusive := historyWindow(opts.Before, start, end)
	resp, err := t.client.GetConversationHistoryContext(ctx, &slack.GetConversationHistoryParameters{
		ChannelID:          t.channelID,
		Latest:             latest,
		Oldest:             oldest,
		Inclusive:          inclusive,
		Limit:              limit,
		IncludeAllMetadata: true,
	})
	if err != nil {
		return channel.HistoryPage{}, fmt.Errorf("read slack channel: %w", err)
	}
	nextCursor := historyNextCursor(resp.Messages, limit, start)
	raw, err := h.filterRouteGeneration(ctx, t, resp.Messages, start, end)
	if err != nil {
		return channel.HistoryPage{}, err
	}
	allowedBotMessages, err := h.allowedBotMessages(ctx, chatSessionID, t, opts, raw)
	if err != nil {
		return channel.HistoryPage{}, err
	}
	raw = filterContextGeneration(raw, opts, start, end, t.botUserID, allowedBotMessages)
	page := normalizePage(ctx, t.client, h.logger, raw, t.botUserID, limit, true)
	page.NextCursor = nextCursor
	page.ChannelType = string(TypeSlack)
	return page, nil
}

// Thread 返回一条线程的消息（从旧到新）。threadID 为空读「会话所在的线程」
// （agent 自己的那条）；非空则读指定线程——但永远限定在会话钉住的频道内。私聊
// 没有线程，直接读线性的私聊记录。支撑 `multica chat thread [id]` 命令。
func (h *History) Thread(ctx context.Context, chatSessionID pgtype.UUID, threadID string, opts channel.HistoryOptions) (channel.HistoryPage, error) {
	t, err := h.resolve(ctx, chatSessionID)
	if err != nil {
		return channel.HistoryPage{}, err
	}
	limit := clampHistoryLimit(opts.Limit)
	ts := threadID
	if ts == "" {
		ts = t.threadRoot // 读会话自己的线程根
	}
	start, end := historyBounds(opts, t)
	if opts.BoundaryPending || t.boundaryPending {
		return channel.HistoryPage{ChannelType: string(TypeSlack), ThreadID: ts}, nil
	}

	var raw []slack.Message
	if ts == "" {
		// 没有线程可读（私聊，或群会话的线程根已无法恢复）：退化为读频道的线性
		// 对话。
		if historyCursorReachedStart(opts.Before, start) {
			return channel.HistoryPage{ChannelType: string(TypeSlack), ThreadID: ts}, nil
		}
		latest, oldest, inclusive := historyWindow(opts.Before, start, end)
		resp, herr := t.client.GetConversationHistoryContext(ctx, &slack.GetConversationHistoryParameters{
			ChannelID:          t.channelID,
			Latest:             latest,
			Oldest:             oldest,
			Inclusive:          inclusive,
			Limit:              limit,
			IncludeAllMetadata: true,
		})
		if herr != nil {
			return channel.HistoryPage{}, fmt.Errorf("read slack thread: %w", herr)
		}
		raw = resp.Messages
	} else {
		if historyCursorReachedStart(opts.Before, start) {
			return channel.HistoryPage{ChannelType: string(TypeSlack), ThreadID: ts}, nil
		}
		latest, oldest, inclusive := historyWindow(opts.Before, start, end)
		msgs, _, _, rerr := t.client.GetConversationRepliesContext(ctx, &slack.GetConversationRepliesParameters{
			ChannelID:          t.channelID,
			Timestamp:          ts,
			Latest:             latest,
			Oldest:             oldest,
			Inclusive:          inclusive,
			Limit:              limit,
			IncludeAllMetadata: true,
		})
		if rerr != nil {
			return channel.HistoryPage{}, fmt.Errorf("read slack thread: %w", rerr)
		}
		raw = msgs
	}
	nextCursor := historyNextCursor(raw, limit, start)
	raw, err = h.filterRouteGeneration(ctx, t, raw, start, end)
	if err != nil {
		return channel.HistoryPage{}, err
	}
	allowedBotMessages, err := h.allowedBotMessages(ctx, chatSessionID, t, opts, raw)
	if err != nil {
		return channel.HistoryPage{}, err
	}
	raw = filterContextGeneration(raw, opts, start, end, t.botUserID, allowedBotMessages)
	page := normalizePage(ctx, t.client, h.logger, raw, t.botUserID, limit, false)
	page.NextCursor = nextCursor
	page.ChannelType = string(TypeSlack)
	page.ThreadID = ts
	return page, nil
}

// historyBounds 把调用方要的时间窗与绑定上记录的会话史界（history_start/end）
// 收窄成交集：只读两者都允许的范围，避免把清空/新建边界之外的老消息捞回来。
func historyBounds(opts channel.HistoryOptions, target slackTarget) (start, end string) {
	start = opts.After
	if target.historyStart != "" && (start == "" || slackTSLess(start, target.historyStart)) {
		start = target.historyStart
	}
	end = opts.Until
	if target.historyEnd != "" && (end == "" || slackTSLess(target.historyEnd, end)) {
		end = target.historyEnd
	}
	return start, end
}

// historyWindow 把 before/start/end 换算成 Slack 拉取接口的 Latest/Oldest/
// Inclusive 参数。end（会话史末端）比 before 更早时以 end 为 Latest，保证不越过
// 会话边界。
func historyWindow(before, start, end string) (latest, oldest string, inclusive bool) {
	latest = before
	if end != "" && (latest == "" || slackTSLess(end, latest)) {
		latest = end
	}
	if before == "" {
		return latest, start, start != "" || end != ""
	}
	return latest, "", false
}

// historyCursorReachedStart 判断「向前翻页已经翻到底」：before 游标已不早于会话
// 史界起点时，后面没有更老的消息可拉了。
func historyCursorReachedStart(before, start string) bool {
	return before != "" && start != "" && !slackTSLess(start, before)
}

// historyNextCursor 从本次拉到的消息里挑出最老一条作为下一页游标；没拉满一页、
// 或最老消息已经顶到会话史界时返回空（没有下一页）。
func historyNextCursor(raw []slack.Message, limit int, historyStart string) string {
	if len(raw) < limit || len(raw) == 0 {
		return ""
	}
	oldest := raw[0].Timestamp
	for i := 1; i < len(raw); i++ {
		if slackTSLess(raw[i].Timestamp, oldest) {
			oldest = raw[i].Timestamp
		}
	}
	if historyStart != "" && !slackTSLess(historyStart, oldest) {
		return ""
	}
	return oldest
}

// filterRouteGeneration 把原始消息裁成只属于【当前路由代】的部分：剔掉时间窗之
// 外的、剔掉带着 metadata 的 control_ack 或别的绑定的 bot 消息（用出站台账与
// 消息自带 metadata 双重核对）。此外，若窗口起点正是会话开始那条首消息，且它是
// 一条「/new 起新会话」命令，正文为空的直接扔掉、非空的剥成后续正文。
func (h *History) filterRouteGeneration(ctx context.Context, target slackTarget, raw []slack.Message, start, end string) ([]slack.Message, error) {
	window := raw[:0]
	ids := make([]string, 0, len(raw))
	for _, message := range raw {
		if start != "" && slackTSLess(message.Timestamp, start) {
			continue
		}
		if end != "" && !slackTSLess(message.Timestamp, end) {
			continue
		}
		window = append(window, message)
		ids = append(ids, message.Timestamp)
	}
	if len(window) == 0 {
		return window, nil
	}
	rows, err := h.q.ListChannelOutboundMessagesByIDs(ctx, db.ListChannelOutboundMessagesByIDsParams{
		InstallationID: target.installationID, ChannelMessageIds: ids,
	})
	if err != nil {
		return nil, fmt.Errorf("load slack outbound route ledger: %w", err)
	}
	owners := make(map[string]db.ChannelOutboundMessage, len(rows))
	for _, row := range rows {
		owners[row.ChannelMessageID] = row
	}
	result := window[:0]
	for _, message := range window {
		if message.Metadata.EventType == slackOutboundMetadataEvent {
			kind, _ := message.Metadata.EventPayload["kind"].(string)
			bindingID, _ := message.Metadata.EventPayload["binding_id"].(string)
			if kind == "control_ack" || (bindingID != "" && bindingID != util.UUIDToString(target.bindingID)) {
				continue
			}
		}
		if owner, ok := owners[message.Timestamp]; ok {
			if owner.OutboundKind == "control_ack" || owner.BindingID != target.bindingID {
				continue
			}
		}
		if message.Timestamp == target.historyStart {
			text := strings.TrimSpace(strings.ReplaceAll(message.Text, "<@"+target.botUserID+">", ""))
			if body, command := engine.ParseNewChatCommand(text); command {
				if body == "" {
					continue
				}
				message.Text = body
			}
		}
		result = append(result, message)
	}
	return result, nil
}

// allowedBotMessages 只在 ContextRevision > 1（即存在多代上下文）时生效：它从
// 出站台账里查出「本代会话确实持久化过的 bot 消息 id」，返回这个白名单，供
// filterContextGeneration 决定哪些 bot 消息可以进 agent 上下文。
func (h *History) allowedBotMessages(ctx context.Context, chatSessionID pgtype.UUID, target slackTarget, opts channel.HistoryOptions, raw []slack.Message) (map[string]struct{}, error) {
	if opts.ContextRevision <= 1 {
		return nil, nil
	}
	candidates := make([]string, 0, len(raw))
	for _, message := range raw {
		if message.User == target.botUserID {
			candidates = append(candidates, message.Timestamp)
		}
	}
	if len(candidates) == 0 {
		return map[string]struct{}{}, nil
	}
	ids, err := h.q.ListChannelOutboundMessageIDsForContext(ctx, db.ListChannelOutboundMessageIDsForContextParams{
		ChatSessionID:          chatSessionID,
		ChannelType:            pgtype.Text{String: string(TypeSlack), Valid: true},
		InstallationID:         target.installationID,
		ChannelChatID:          pgtype.Text{String: target.channelID, Valid: true},
		ChannelContextRevision: pgtype.Int8{Int64: opts.ContextRevision, Valid: true},
		CandidateMessageIds:    candidates,
	})
	if err != nil {
		return nil, fmt.Errorf("load slack reply provenance: %w", err)
	}
	allowed := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		allowed[id] = struct{}{}
	}
	return allowed, nil
}

func filterContextGeneration(raw []slack.Message, opts channel.HistoryOptions, start, end, botUserID string, allowedBotMessages map[string]struct{}) []slack.Message {
	out := raw[:0]
	for _, message := range raw {
		if start != "" && slackTSLess(message.Timestamp, start) {
			continue
		}
		if end != "" && !slackTSLess(message.Timestamp, end) {
			continue
		}
		// 光看 Slack 时间戳当不了因果边界：旧任务回的某条消息可能迟于 /clear 才
		// 落下来。从第 2 代上下文起，只有「provider id 落在这个确切代的任务上」
		// 的 assistant 消息才被信任。人类与第三方 bot 消息仍按时间窗放行。来历
		// 不明的自家 bot 输出对 agent 上下文关门失败，但对 Slack 里的人类依旧
		// 可见。
		if opts.ContextRevision > 1 && message.User == botUserID {
			if _, ok := allowedBotMessages[message.Timestamp]; !ok {
				continue
			}
		}
		if message.Timestamp == opts.After {
			text := strings.TrimSpace(strings.ReplaceAll(message.Text, "<@"+botUserID+">", ""))
			if body, ok := engine.ParseFreshSessionCommand(text); ok {
				if body == "" {
					continue
				}
				message.Text = body
			}
		}
		out = append(out, message)
	}
	return out
}

// clampHistoryLimit 把请求的分页大小夹进 [defaultHistoryLimit, maxHistoryLimit]。
func clampHistoryLimit(n int) int {
	if n <= 0 {
		return defaultHistoryLimit
	}
	if n > maxHistoryLimit {
		return maxHistoryLimit
	}
	return n
}

// historyTarget 从绑定里找回真实频道 id 与会话自己的线程根。channel_chat_id 可能
// 是 "channel:threadRoot" 这种复合隔离键，所以真实频道 id 从绑定 config
// （slackBindingConfig）读；线程根取记录的回复线程（last_thread_id），找不到再回
// 退用复合键的后缀；私聊则为空。
func historyTarget(b db.ChannelChatSessionBinding) (channelID, threadRoot string) {
	channelID = b.ChannelChatID
	if len(b.Config) > 0 {
		var cfg slackBindingConfig
		if err := json.Unmarshal(b.Config, &cfg); err == nil && cfg.ChannelID != "" {
			channelID = cfg.ChannelID
		}
	}
	if b.LastThreadID.Valid && b.LastThreadID.String != "" {
		threadRoot = b.LastThreadID.String
	} else if i := strings.IndexByte(b.ChannelChatID, ':'); i >= 0 {
		threadRoot = b.ChannelChatID[i+1:]
	}
	return channelID, threadRoot
}

// normalizePage 把原始 Slack 消息归一化成一页「从旧到新」的消息：一次批量解析
// 展示名、给发送者打标、映射角色、并算出向前翻页的游标。overview 为 true 时，
// 顶着一条线程的消息（reply_count > 0）会被标上线程 id + 回复数，agent 才能用
// `multica chat thread <id>` 钻进去。
func normalizePage(ctx context.Context, client historyClient, logger *slog.Logger, raw []slack.Message, botUserID string, limit int, overview bool) channel.HistoryPage {
	sort.SliceStable(raw, func(i, j int) bool { return slackTSLess(raw[i].Timestamp, raw[j].Timestamp) })

	names := resolveUserNames(ctx, client, logger, raw, botUserID)
	labeler := newHistoryLabeler(names)

	out := make([]channel.HistoryMessage, 0, len(raw))
	for i := range raw {
		m := raw[i]
		text := flattenSlackText(m)
		if text == "" {
			continue // 真正的入群/系统/编辑标记：没有可读正文
		}
		own := m.User != "" && m.User == botUserID
		role := channel.HistoryRoleUser
		if own {
			role = channel.HistoryRoleAssistant
		}
		hm := channel.HistoryMessage{
			ID:       m.Timestamp,
			Author:   labeler.label(m, own),
			AuthorID: m.User,
			Role:     role,
			Text:     text,
			TS:       m.Timestamp,
		}
		if overview && m.ReplyCount > 0 {
			hm.ThreadID = m.Timestamp
			hm.ReplyCount = m.ReplyCount
			hm.LatestReply = m.LatestReply
		}
		out = append(out, hm)
	}

	return channel.HistoryPage{Messages: out}
}

// maxDerivedTextLen 封顶从附件/blocks 里提炼出的正文长度，避免一条啰嗦的告警
// 卡片把 agent 的上下文灌爆。只作用于兜底路径；正常的顶层消息正文原样直通。
const maxDerivedTextLen = 4000

// flattenSlackText 把一条 Slack 消息渲染成历史契约承诺的纯文本正文
// （channel.HistoryMessage.Text）。告警/webhook bot（Grafana 卡片、incoming
// webhook）把整段正文放在附件或 Block Kit 块里、顶层 Text 是空的；没有这个兜底
// 的话，这类消息会跟入群/系统标记长得一模一样而被丢掉（MUL-3931 / #4803）。
// 取值顺序：顶层 text → 各附件的渲染 text/fields → 最后兜底的附件 fallback →
// blocks 尽力拍平。只有真没有任何可渲染内容（纯系统标记）时才返回 ""。
func flattenSlackText(m slack.Message) string {
	if t := strings.TrimSpace(m.Text); t != "" {
		return t
	}
	parts := make([]string, 0, len(m.Attachments)+1)
	for i := range m.Attachments {
		if t := attachmentText(m.Attachments[i]); t != "" {
			parts = append(parts, t)
		}
	}
	if len(parts) == 0 {
		if t := flattenBlocks(m.Blocks); t != "" {
			parts = append(parts, t)
		}
	}
	return truncateRunes(strings.TrimSpace(strings.Join(parts, "\n")), maxDerivedTextLen)
}

// attachmentText 把一条附件浓缩成纯文本。附件的 Fallback 只是给「渲染不了附件
// 的客户端」用的最后兜底摘要；Grafana 式告警往往把真正的告警正文放在 Text/Fields，
// Fallback 反而是重复的短标题，所以 Fallback 排到最后。
func attachmentText(a slack.Attachment) string {
	parts := make([]string, 0, 3+len(a.Fields))
	for _, s := range []string{a.Pretext, a.Title, a.Text} {
		if s = strings.TrimSpace(s); s != "" {
			parts = append(parts, s)
		}
	}
	for _, f := range a.Fields {
		if s := strings.TrimSpace(f.Title + " " + f.Value); s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}
	if t := strings.TrimSpace(a.Fallback); t != "" {
		return t
	}
	return flattenBlocks(a.Blocks)
}

// flattenBlocks 把 Block Kit 块尽力拍平成纯文本：遍历常见带文字的块（section、
// header、context、markdown、rich_text），跳过交互/媒体块。
func flattenBlocks(blocks slack.Blocks) string {
	parts := make([]string, 0, len(blocks.BlockSet))
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			parts = append(parts, s)
		}
	}
	for _, b := range blocks.BlockSet {
		switch v := b.(type) {
		case *slack.SectionBlock:
			if v.Text != nil {
				add(v.Text.Text)
			}
			for _, f := range v.Fields {
				if f != nil {
					add(f.Text)
				}
			}
		case *slack.HeaderBlock:
			if v.Text != nil {
				add(v.Text.Text)
			}
		case *slack.MarkdownBlock:
			add(v.Text)
		case *slack.ContextBlock:
			for _, el := range v.ContextElements.Elements {
				if tb, ok := el.(*slack.TextBlockObject); ok {
					add(tb.Text)
				}
			}
		case *slack.RichTextBlock:
			add(richTextBlockText(v))
		}
	}
	return strings.Join(parts, "\n")
}

// richTextBlockText 把 rich_text 块尽力拍平成纯文本：遍历 section、list、quote、
// preformatted，把文字与链接串接起来（每节一行）。提及、emoji 等行内装饰跳过——
// agent 要的是纯正文，不是逐像素还原。纯 rich_text 正文是 Slack 自家富文本输入
// 框产出的标准形态：顶层 Text 为空、只靠它承载的 bot 消息，不做这层兜底就会丢。
func richTextBlockText(b *slack.RichTextBlock) string {
	var lines []string
	var writeElement func(el slack.RichTextElement)
	writeSection := func(els []slack.RichTextSectionElement) {
		var sb strings.Builder
		for _, e := range els {
			switch v := e.(type) {
			case *slack.RichTextSectionTextElement:
				sb.WriteString(v.Text)
			case *slack.RichTextSectionLinkElement:
				if v.Text != "" {
					sb.WriteString(v.Text)
				} else {
					sb.WriteString(v.URL)
				}
			}
		}
		if s := strings.TrimSpace(sb.String()); s != "" {
			lines = append(lines, s)
		}
	}
	writeElement = func(el slack.RichTextElement) {
		switch v := el.(type) {
		case *slack.RichTextSection:
			writeSection(v.Elements)
		case *slack.RichTextQuote:
			writeSection(v.Elements)
		case *slack.RichTextPreformatted:
			writeSection(v.Elements)
		case *slack.RichTextList:
			for _, item := range v.Elements {
				writeElement(item)
			}
		}
	}
	for _, el := range b.Elements {
		writeElement(el)
	}
	return strings.Join(lines, "\n")
}

// truncateRunes 把 s 截断到最多 max 个字符，截断处补省略号。
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// resolveUserNames 批量解析人类发送者的展示名，尽力而为。失败（缺 users:read
// scope、传输错误）时返回 nil map，让打标器退化成位置编号的 "User N"，而不是
// 卡住整次读取。
func resolveUserNames(ctx context.Context, client historyClient, logger *slog.Logger, msgs []slack.Message, botUserID string) map[string]string {
	seen := make(map[string]bool)
	ids := make([]string, 0, len(msgs))
	for i := range msgs {
		u := msgs[i].User
		if u == "" || u == botUserID || seen[u] {
			continue
		}
		seen[u] = true
		ids = append(ids, u)
	}
	if len(ids) == 0 {
		return nil
	}
	users, err := client.GetUsersInfoContext(ctx, ids...)
	if err != nil || users == nil {
		if err != nil {
			logger.WarnContext(ctx, "slack history: user name resolution failed", "ids", len(ids), "error", err)
		}
		return nil
	}
	names := make(map[string]string, len(*users))
	for _, u := range *users {
		if name := slackDisplayName(u); name != "" {
			names[u.ID] = name
		}
	}
	return names
}

// slackDisplayName 从 Slack 用户对象里挑最友好可用的名字。
func slackDisplayName(u slack.User) string {
	switch {
	case u.Profile.DisplayName != "":
		return u.Profile.DisplayName
	case u.RealName != "":
		return u.RealName
	default:
		return u.Name
	}
}

// historyLabeler 在一页内分配稳定、可读的发送者标签：本 bot 标 "Bot"；解析到真名
// 的人类用真名；解析不到的人类回退成按位置编号的 "User N"；第三方 bot 用它发消息
// 时的用户名。
type historyLabeler struct {
	names map[string]string
	seen  map[string]string
	n     int
}

func newHistoryLabeler(names map[string]string) *historyLabeler {
	return &historyLabeler{names: names, seen: make(map[string]string)}
}

// label 给一条消息的作者打标签；同页内同一个作者始终同标。
func (l *historyLabeler) label(m slack.Message, own bool) string {
	if own {
		return "Bot"
	}
	key := m.User
	if key == "" {
		if m.Username != "" {
			return m.Username
		}
		key = "bot:" + m.BotID
	}
	if lbl, ok := l.seen[key]; ok {
		return lbl
	}
	var lbl string
	if name := l.names[m.User]; name != "" {
		lbl = name
	} else if m.Username != "" {
		lbl = m.Username
	} else {
		l.n++
		lbl = fmt.Sprintf("User %d", l.n)
	}
	l.seen[key] = lbl
	return lbl
}

// slackTSLess 按时间先后比较两个 Slack 时间戳（"秒.微秒"）。
func slackTSLess(a, b string) bool {
	return parseSlackTS(a) < parseSlackTS(b)
}

// parseSlackTS 把 Slack ts 文本解析成浮点秒数；解析失败返回 0（比较退化为相等/
// 纯数值语义，够用即可）。
func parseSlackTS(ts string) float64 {
	f, _ := strconv.ParseFloat(ts, 64)
	return f
}
