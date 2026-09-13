// inbound_enricher.go：入站富化器——回调飞书 IM API，把用户【显式】附带的
// 上下文（引用回复 / 合并转发包）以及群聊近期语境内联进消息正文。
package lark

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
)

// larkMsgTypeMergeForward 是“合并转发”消息的 msg_type——用户把一个包里的多条
// 消息整体转发。它自己的 body.content 是一段固定的哨兵字符串；真正被转发的
// 消息来自 GetMessage 调用多出的 items[]。
const larkMsgTypeMergeForward = "merge_forward"

// defaultMaxForwardChildren 限制单次转发我们内联的子消息条数。Lark 自己把
// merge_forward 上限设为 100 条；我们镜像它作为安全阀，病态的大包不会撑爆
// agent 的上下文。超过上限的部分丢弃，并带一个可见的 "... (N more truncated)"
// 标记。
const defaultMaxForwardChildren = 100

// DefaultRecentContextSize 是生产接线给群聊上下文预取用的窗口：用户在有 @ Bot
// 时的那次单条 list 调用的 page_size。它是【拉取】预算而非保证的渲染条数——
// 触发消息本身与任何被引用的父消息都会从结果里滤掉，所以 <recent_context> 块
// 通常会少渲染一两行。10 能让 agent 的提示词有意义的上下文又不臃肿，也不给入站
// ACK 预算添负担（一次 list 调用，page_size 10）。
const DefaultRecentContextSize = 10

const (
	recentContextEndpoint         = "im/v1/messages.list"
	recentContextMaxFetchAttempts = 2

	recentContextFailureUnknown          = "unknown"
	recentContextFailureChannelUnbound   = "channel_unbound"
	recentContextFailureTimeout          = "timeout"
	recentContextFailurePermissionDenied = "permission_denied"
	recentContextFailureMessageDeleted   = "message_deleted"
	recentContextFailureRateLimited      = "rate_limited"
	recentContextFailureTokenExpired     = "token_expired"
	recentContextFailureTemporary        = "temporary"
)

// errRecentContextChannelUnbound 表示近期上下文预取缺 chat_id。
var errRecentContextChannelUnbound = errors.New("lark enricher: missing chat_id for recent context")

// Enricher 通过回调飞书 IM API，用用户【显式】附带的上下文——引用回复或合并
// 转发包——展开一条入站消息的正文。它跑在（快速、无 HTTP 的）decoder 之后、
// dispatcher 之前，把一条裸的 "@bot 总结一下" 变成已内联引用对话的正文。
//
// 按契约是 best-effort：任何拉取失败都退化成可读的说明或占位符，Enrich 绝不
// 返回错误或阻塞摄入。没有要展开内容的消息（无 parent_id、非 merge_forward）
// 不经任何网络调用原样返回。
type Enricher interface {
	Enrich(ctx context.Context, msg InboundMessage, creds InstallationCredentials) InboundMessage
}

// InboundEnricherConfig 调优富化器。所有字段都有默认。
type InboundEnricherConfig struct {
	// MaxForwardChildren 限制内联的转发子消息条数。<=0 用
	// defaultMaxForwardChildren。
	MaxForwardChildren int
	// RecentContextSize 限制用户在有 @ Bot 时富化器预取并内联成
	// <recent_context> 块的周边群消息条数。<=0 完全禁用预取（只用显式附带的
	// 引用/转发上下文）；生产接线设 DefaultRecentContextSize。超过 Lark 单页 50
	// 上限的值由 client 夹紧。
	RecentContextSize int
	// Logger 接收拉取失败的 best-effort 警告。nil 用 slog.Default()。
	Logger *slog.Logger
}

// inboundEnricher 是 Enricher 的实现。
type inboundEnricher struct {
	client             APIClient
	maxForwardChildren int
	recentContextSize  int
	logger             *slog.Logger
}

// NewInboundEnricher 构造由给定 Lark API client 支撑的 Enricher。client 提供
// GetMessage；其余一切（压平、块组装、说话者命名）都是本地处理。
func NewInboundEnricher(client APIClient, cfg InboundEnricherConfig) Enricher {
	if cfg.MaxForwardChildren <= 0 {
		cfg.MaxForwardChildren = defaultMaxForwardChildren
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &inboundEnricher{
		client:             client,
		maxForwardChildren: cfg.MaxForwardChildren,
		recentContextSize:  cfg.RecentContextSize,
		logger:             cfg.Logger,
	}
}

// Enrich 重写 msg.Body，内联周边群语境和/或引用回复的父消息和/或转发包。组合
// 顺序从宽到窄：先是周边群历史，其次显式引用的父消息（特定引用），最后是消息
// 自身内容（转发则是渲染后的记录）。
//
//	<recent_context …>…</recent_context>
//
//	<quoted_message …>…</quoted_message>
//
//	<[发送者名]: 用户自己的消息，或转发记录>
//
// <recent_context> 块只对发给 Bot 的群消息产生，且仅当 RecentContextSize > 0
// ——它回答 MUL-3084（Bot 只看到被 @ 的那一行，看不到周边对话）。它是这里唯一
// 不是由“用户显式附带的东西”触发的拉取。@ 落在飞书话题（话题）里时窗口限定在
// 该话题内，一个话题的上下文绝不会包含兄弟话题的消息（#5835——见
// fetchRecentItems）。
//
// 在群聊里，所有块（近期 + 引用 + 转发）里的每个说话者以及 @ Bot 的发送者，
// 都通过一次 Contact 批量调用解析成真实显示名，agent 读到的是 "[Alice]: …"
// 而不是 "[User 1]: …"，并且知道是谁 @ 的。这也是为什么引用/转发条目要在
// 解析名字（Phase 2）之前先拉取（Phase 1）。解析不到的发送者退回位置标签
// "User N"；名字解析是 best-effort、绝不阻塞。p2p 私聊保持位置标签（一对一里
// 身份没有歧义）。
//
// 持久化说明：与引用/转发块一样，被重写的 Body 会随被 @ 的那一轮后续持久化到
// chat_message.content（AppendUserMessage）。把周边群消息——包括没有 @ Bot 的
// 发送者的——内联进某成员的被 @ 轮次，是 MUL-3084 接受的产品决策。它并【不】
// 放松 MUL-2671 的丢弃审计不变量：未被 @ 的群消息仍然绝不会创建自己的会话行，
// 只会作为“附在 workspace 成员显式指向 Bot 的那一轮上的阅读上下文”浮出。
func (e *inboundEnricher) Enrich(ctx context.Context, msg InboundMessage, creds InstallationCredentials) InboundMessage {
	freshSource := msg.CommandBody
	if freshSource == "" {
		freshSource = msg.Body
	}
	startChat := false
	if control, ok := engine.ParseControlCommand(freshSource); ok {
		msg.Body = control.Body
		switch control.Kind {
		case engine.ControlCommandFreshSession:
			msg.ForceFreshSession = true
		case engine.ControlCommandNewChat:
			// 新对话不能继承适配器从路由上一条对话生成的近期上下文。显式的引用/
			// 转发上下文仍会在下面展开，因为用户把它附给了这条命令。
			startChat = true
		}
	}

	isForward := msg.MessageType == larkMsgTypeMergeForward
	wantRecent := !startChat && e.recentContextSize > 0 && msg.ChatType == ChatTypeGroup && msg.AddressedToBot
	if msg.ParentID == "" && !isForward && !wantRecent {
		// 没有要展开的东西、也不想要群预取——不发网络调用。
		return msg
	}
	// 传输层没接线（没有 Lark app 的部署上的 stub client）时直接跳过，别给每条
	// 回复都盖一个拉取错误。Body 保持 decoder 产出的样子。
	if e.client == nil || !e.client.IsConfigured() {
		return msg
	}

	// Phase 1 —— 拉取可能要渲染的每一组消息。每组都是 best-effort；错误在该块
	// 渲染处处理。我们一次性先拉取（而不是逐块拉取-渲染），这样 Phase 2 才能用
	// 一次 Contact 批量调用解析【所有】块的每个说话者的显示名——否则不在近期窗口
	// 里的引用/转发发送者会退回 "User N"。
	var recentItems []LarkMessage
	var recentErr error
	if wantRecent {
		recentItems, recentErr = e.fetchRecentItems(ctx, creds, msg)
	}
	var quotedItems []LarkMessage
	var quotedErr error
	if msg.ParentID != "" {
		quotedItems, quotedErr = e.client.GetMessage(ctx, creds, msg.ParentID)
	}
	var forwardItems []LarkMessage
	var forwardErr error
	if isForward {
		forwardItems, forwardErr = e.client.GetMessage(ctx, creds, msg.MessageID)
	}

	// Phase 2 —— 用一次批量调用解析即将渲染的每个说话者（近期 + 引用 + 转发）
	// 加 @ Bot 的发送者的显示名。仅群聊；p2p 保持位置标签（一对一里身份无歧义）。
	// 解析不到的 id 按 speakerLabeler 退回 "User N"。
	var names map[string]string
	if msg.ChatType == ChatTypeGroup {
		ids := senderOpenIDs(recentItems)
		ids = append(ids, senderOpenIDs(quotedItems)...)
		ids = append(ids, senderOpenIDs(forwardItems)...)
		if msg.SenderOpenID != "" {
			ids = append(ids, string(msg.SenderOpenID))
		}
		names = e.resolveNames(ctx, creds, ids)
	}

	// Phase 3 —— 用完整名字映射按“从宽到窄”渲染。
	var b strings.Builder
	if wantRecent {
		if recentErr != nil {
			b.WriteString(recentContextUnavailableLine(classifyRecentContextFetchError(recentErr).category))
		} else if len(recentItems) > 0 {
			b.WriteString(e.renderRecentContextBlock(recentItems, names))
		}
	}
	if msg.ParentID != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(e.renderQuotedBlock(msg.ParentID, quotedItems, quotedErr, names))
	}

	var core string
	if isForward {
		if forwardErr != nil {
			e.logger.Warn("lark enricher: forward fetch failed", "message_id", msg.MessageID, "err", forwardErr)
			core = forwardedErrorBlock()
		} else {
			core = e.renderForwardedItems(forwardItems, msg.MessageID, names)
		}
	} else {
		core = msg.Body
		// 用真实名字标注用户自己的消息，让 agent 知道是谁 @ 的——而不只是说了
		// 什么。仅当名字解析成功时（群路径）；否则正文原样通过。
		if name := names[string(msg.SenderOpenID)]; name != "" {
			core = fmt.Sprintf("[%s]: %s", name, msg.Body)
		}
	}
	if b.Len() > 0 && core != "" {
		b.WriteString("\n\n")
	}
	b.WriteString(core)

	msg.Body = b.String()
	return msg
}

// senderOpenIDs 返回给定消息里去重后的非 app 发送者 open_id，按首次出现顺序
// ——这是 Contact 名字查询的输入集。
func senderOpenIDs(msgs []LarkMessage) []string {
	seen := make(map[string]bool, len(msgs))
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		if m.SenderType == "app" || m.SenderID == "" || seen[m.SenderID] {
			continue
		}
		seen[m.SenderID] = true
		out = append(out, m.SenderID)
	}
	return out
}

// resolveNames 批量把 open_id 解析成显示名，best-effort：失败（联系人范围受限、
// transport 错误）只记日志并返回 nil，让每个说话者标签退化成位置标签 "User N"
// 而不是阻塞摄入。重复/空 id 先被丢弃。
func (e *inboundEnricher) resolveNames(ctx context.Context, creds InstallationCredentials, ids []string) map[string]string {
	uniq := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		uniq = append(uniq, id)
	}
	if len(uniq) == 0 {
		return nil
	}
	names, err := e.client.BatchGetUsers(ctx, creds, uniq)
	if err != nil {
		e.logger.Warn("lark enricher: speaker name resolution failed", "ids", len(uniq), "err", err)
		return nil
	}
	return names
}

// fetchRecentItems 拉取近期群窗口，返回要渲染的消息——滤掉触发消息本身和被
// 直接引用的父消息（它有自己的 <quoted_message> 块），按旧到新排序。拉取失败
// 返回给调用方（它渲染一条安全可读的退化说明）；绝不阻塞摄入。
//
// 触发落在飞书话题里（msg.ThreadID != ""）时，窗口限定在该话题
// （container_id_type=thread），共享 chat_id 的兄弟话题不会漏进本话题的上下文或
// 其持久化轮次（#5835）。因为线程容器拒绝 end_time，话题路径改为在客户端侧按
// 触发时间锚定；它对 thread_id 也 fail-closed——任何返回条目缺 thread_id 或不
// 匹配都被丢弃，而不是被信任。话题拉取失败与聊天路径一样优雅退化，绝不回退到
// 全聊天拉取（那会重新打开泄漏口）。话题之外，聊天路径保持不变：用 end_time
// 锚定到触发时间。
func (e *inboundEnricher) fetchRecentItems(ctx context.Context, creds InstallationCredentials, msg InboundMessage) ([]LarkMessage, error) {
	if msg.ChatID == "" {
		classified := classifyRecentContextFetchError(errRecentContextChannelUnbound)
		e.logRecentContextFetchFailure(msg, errRecentContextChannelUnbound, classified, 0)
		return nil, errRecentContextChannelUnbound
	}

	// 飞书把 create_time 按 epoch 毫秒下发；缺失/不可解析返回 0。聊天路径把它
	// 转成秒给 end_time；话题路径用原始毫秒做下面客户端侧锚点。
	triggerMillis := parseLarkMillis(msg.CreateTime)
	params := ListMessagesParams{
		ChatID:   msg.ChatID,
		PageSize: e.recentContextSize,
	}
	if msg.ThreadID != "" {
		// 话题限定拉取：不带 end_time（线程容器拒绝它）；窗口在下方客户端侧
		// 锚定。
		params.ThreadID = msg.ThreadID
	} else {
		// 0 告诉 client “不要 end_time”（最新 N 条）。
		params.EndTime = triggerMillis / 1000
	}
	var items []LarkMessage
	var err error
	for attempt := 1; attempt <= recentContextMaxFetchAttempts; attempt++ {
		items, err = e.client.ListChatMessages(ctx, creds, params)
		if err == nil {
			if attempt > 1 {
				e.logger.Info("lark enricher: recent context fetch recovered after retry",
					"layer", "lark_inbound_enricher",
					"endpoint", recentContextEndpoint,
					"status", "recovered",
					"attempts", attempt,
					"chat_id", string(msg.ChatID),
					"message_id", msg.MessageID)
			}
			break
		}
		classified := classifyRecentContextFetchError(err)
		// 只有在共享富化预算还剩时间时重试才有意义。两次尝试复用同一个 ctx
		// （ws_connector 把整个 Enrich 限制在 EnrichTimeout、约 2 秒），所以 ctx
		// 结束后第二次调用会立即失败——此刻就该退化，而不是烧一次注定失败请求。
		// 这就是“首次尝试超时从不恢复”的原因。
		if !classified.retryable || attempt == recentContextMaxFetchAttempts || ctx.Err() != nil {
			e.logRecentContextFetchFailure(msg, err, classified, attempt)
			return nil, err
		}
		e.logger.Warn("lark enricher: recent context fetch failed; retrying",
			"layer", "lark_inbound_enricher",
			"endpoint", recentContextEndpoint,
			"status", "retrying",
			"category", classified.category,
			"retryable", classified.retryable,
			"attempt", attempt,
			"next_attempt", attempt+1,
			"chat_id", string(msg.ChatID),
			"message_id", msg.MessageID,
			"err", err)
	}

	exclude := map[string]bool{msg.MessageID: true}
	if msg.ParentID != "" {
		exclude[msg.ParentID] = true
	}
	inThread := msg.ThreadID != ""
	kept := make([]LarkMessage, 0, len(items))
	for _, it := range items {
		if exclude[it.MessageID] {
			continue
		}
		// Bot 的 markdown 回复以 schema-2.0 交互卡片发出，压平后是零信号的
		// "[interactive card]" 占位符——丢弃它们，免得渲染噪音（#5835）。
		if it.SenderType == "app" && it.MessageType == "interactive" {
			continue
		}
		if inThread {
			// 话题隔离 fail-closed：线程容器应当只返回本话题的消息，但若飞书
			// 返回了 thread_id 缺失或不匹配的条目，丢弃它也不冒把兄弟话题内容漏
			// 进本话题的风险。
			if it.ThreadID != msg.ThreadID {
				continue
			}
			// 线程容器忽略 end_time，所以客户端侧锚定：丢弃任何创建时间严格晚于
			// 被 @ 时刻的消息。触发时间为 0（不可解析）时禁用锚定。
			if triggerMillis > 0 && parseLarkMillis(it.CreateTime) > triggerMillis {
				continue
			}
		}
		kept = append(kept, it)
	}

	// list 端点按最新在前返回；我们按旧到新渲染，让记录像聊天一样从上往下读。
	sort.SliceStable(kept, func(i, j int) bool {
		return parseLarkMillis(kept[i].CreateTime) < parseLarkMillis(kept[j].CreateTime)
	})
	return kept, nil
}

// logRecentContextFetchFailure 记录一次近期上下文拉取失败（带分类与尝试次数）。
func (e *inboundEnricher) logRecentContextFetchFailure(msg InboundMessage, err error, classified recentContextFetchClassification, attempts int) {
	e.logger.Warn("lark enricher: recent context fetch failed",
		"layer", "lark_inbound_enricher",
		"endpoint", recentContextEndpoint,
		"status", "failed",
		"category", classified.category,
		"retryable", classified.retryable,
		"attempts", attempts,
		"chat_id", string(msg.ChatID),
		"message_id", msg.MessageID,
		"err", err)
}

// renderRecentContextBlock 把周边对话渲染成 <recent_context> 块：每条消息一行
// "[<说话者>]: <文本>"，旧到新，说话者用 `names` 里的真实名字（缺失退回位置
// 标签 "User N"）。调用方传入非空的 `kept`。
func (e *inboundEnricher) renderRecentContextBlock(kept []LarkMessage, names map[string]string) string {
	labeler := newSpeakerLabeler(names)
	lines := make([]string, 0, len(kept))
	for _, m := range kept {
		label := labeler.label(m)
		var text string
		switch {
		case m.MessageType == larkMsgTypeMergeForward:
			text = "[merge_forward, expand manually]"
		default:
			text = e.flattenMessage(m)
			if text == "" {
				text = "[empty message]"
			}
		}
		lines = append(lines, fmt.Sprintf("[%s]: %s", label, text))
	}
	return fmt.Sprintf("<recent_context count=\"%d\">\n%s\n</recent_context>",
		len(kept), strings.Join(lines, "\n"))
}

// recentContextFetchClassification 是一次近期上下文拉取错误的分类结果。
type recentContextFetchClassification struct {
	category  string
	retryable bool
}

// classifyRecentContextFetchError 给错误分类：是否可重试、属于哪个可见原因类别。
func classifyRecentContextFetchError(err error) recentContextFetchClassification {
	if err == nil {
		return recentContextFetchClassification{category: recentContextFailureUnknown}
	}
	if errors.Is(err, errRecentContextChannelUnbound) {
		return recentContextFetchClassification{category: recentContextFailureChannelUnbound}
	}
	// Lark 业务码能精确命名失败，所以优先于下面的文本启发式——经
	// larkErrorCodeMsg 读取，凡携带业务码的两种形态都能找到：调用方拒绝的 2xx
	// 信封（*APIError）与 HTTP client 返回的非 2xx 回复。只有解析成真实类别的码
	// 才短路；其它一律落下去，这样 "http 403" 之类的纯状态码信号仍能按错误文本
	// 分类。
	if code, msg, ok := larkErrorCodeMsg(err); ok {
		if cls := classifyRecentContextAPIError(code, msg); cls.category != recentContextFailureUnknown {
			return cls
		}
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) ||
		(errors.As(err, &netErr) && netErr.Timeout()) {
		return recentContextFetchClassification{category: recentContextFailureTimeout, retryable: true}
	}

	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "missing chat_id") || strings.Contains(msg, "missing chat id"):
		return recentContextFetchClassification{category: recentContextFailureChannelUnbound}
	case containsAny(msg, "code=99991002", "code=230001", "no permission", "permission denied", "insufficient permissions", "forbidden", "http 403"):
		return recentContextFetchClassification{category: recentContextFailurePermissionDenied}
	case containsAny(msg, "code=230110", "code=230011", "code=230050", "deleted", "recalled", "not visible", "invisible"):
		return recentContextFetchClassification{category: recentContextFailureMessageDeleted}
	case containsAny(msg, "code=99991663", "code=99991664"):
		return recentContextFetchClassification{category: recentContextFailureTokenExpired, retryable: true}
	case containsAny(msg, "code=230020", "rate limit", "rate_limit", "http 429"):
		// 不可重试：client 丢弃 Retry-After，同一预算内立刻发第二次几乎总会再次
		// 撞限，同时给本已受限的 tenant 双倍 list 负载。
		return recentContextFetchClassification{category: recentContextFailureRateLimited}
	case containsAny(msg, "deadline exceeded", "timeout", "timed out"):
		return recentContextFetchClassification{category: recentContextFailureTimeout, retryable: true}
	case containsAny(msg, "http 500", "http 502", "http 503", "http 504", "connection reset", "connection refused", "temporary"):
		return recentContextFetchClassification{category: recentContextFailureTemporary, retryable: true}
	default:
		return recentContextFetchClassification{category: recentContextFailureUnknown}
	}
}

// classifyRecentContextAPIError 按 Lark 业务码直接分类。
func classifyRecentContextAPIError(code int, msg string) recentContextFetchClassification {
	switch {
	case code == 99991002 || code == 230001:
		return recentContextFetchClassification{category: recentContextFailurePermissionDenied}
	case code == 230110 || code == 230011 || code == 230050:
		return recentContextFetchClassification{category: recentContextFailureMessageDeleted}
	case isTokenError(code):
		return recentContextFetchClassification{category: recentContextFailureTokenExpired, retryable: true}
	case code == 230020:
		// 见 classifyRecentContextFetchError：限流失效后退化而不重试，因为 client
		// 丢弃 Retry-After。
		return recentContextFetchClassification{category: recentContextFailureRateLimited}
	}
	return classifyRecentContextFetchError(errors.New(msg))
}

// containsAny 判断 s 是否包含任一 needle。
func containsAny(s string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

// recentContextUnavailableLine 按失败类别返回近期上下文不可用的可读说明行。
func recentContextUnavailableLine(category string) string {
	switch category {
	case recentContextFailureChannelUnbound:
		return "[Recent Lark context unavailable: chat binding is missing. Continuing with the latest message.]"
	case recentContextFailurePermissionDenied:
		return "[Recent Lark context unavailable: the bot cannot read this chat history. Continuing with the latest message.]"
	case recentContextFailureMessageDeleted:
		return "[Recent Lark context unavailable: the referenced chat history is deleted or no longer visible. Continuing with the latest message.]"
	case recentContextFailureTimeout, recentContextFailureRateLimited, recentContextFailureTokenExpired, recentContextFailureTemporary:
		return "[Recent Lark context temporarily unavailable; continuing with the latest message.]"
	default:
		return "[Recent Lark context unavailable; continuing with the latest message.]"
	}
}

// renderQuotedBlock 从已拉取的 GetMessage(parentID) 结果渲染 <quoted_message>
// 块。父消息本身是 merge_forward 时，会在引用块里嵌套一份 <forwarded_messages>
// 记录（GetMessage 响应已同时携带转发哨兵与其子消息）。拉取错误/空/已删除的
// 父消息退化成文档化的错误块。说话者用 `names`（共享的已解析映射）标注，缺失
// 退回 "User N"。
func (e *inboundEnricher) renderQuotedBlock(parentID string, items []LarkMessage, err error, names map[string]string) string {
	if err != nil || len(items) == 0 {
		e.logger.Warn("lark enricher: quoted parent fetch failed",
			"parent_id", parentID, "items", len(items), "err", err)
		return quotedErrorBlock(parentID)
	}
	parent := items[0]
	if parent.Deleted {
		return quotedErrorBlock(parentID)
	}

	labeler := newSpeakerLabeler(names)
	sender := labeler.label(parent)

	if parent.MessageType == larkMsgTypeMergeForward {
		inner := e.renderForwardedItems(items, parentID, names)
		return wrapQuoted(parentID, sender, larkMsgTypeMergeForward, inner)
	}
	text := e.flattenMessage(parent)
	if text == "" {
		text = "[empty message]"
	}
	return wrapQuoted(parentID, sender, parent.MessageType, text)
}

// renderForwardedItems 渲染转发记录 id 为 forwardID 的转发的子消息。子消息按
// 时间排序、受上限约束，每条渲染成 "[<说话者>]: <文本>"；本身又是转发的子消息
// 不递归展开（给它一个“手动展开”占位符），让对 ACK 延迟敏感的入站路径上的
// HTTP 扇出保持有界。
func (e *inboundEnricher) renderForwardedItems(items []LarkMessage, forwardID string, names map[string]string) string {
	// 验证过的契约是 GetMessage(forward_id) 返回一层打包：[哨兵、直接子消息…]。
	// 因此我们把每个非哨兵条目都当直接子消息。刻意按 id 过滤（而不是按
	// upper_message_id == forwardID）：严格按 upper_message_id 匹配会在飞书返回
	// 一个字段未填充的真实子消息时静默【丢弃】它。本身是转发的子消息在下文渲染
	// 成“手动展开”占位符而不递归，孙消息从不内联。
	children := make([]LarkMessage, 0, len(items))
	for _, it := range items {
		if it.MessageID == forwardID {
			continue // 转发哨兵本身
		}
		children = append(children, it)
	}
	total := len(children)
	if total == 0 {
		return "<forwarded_messages count=\"0\">\n[no forwarded content available]\n</forwarded_messages>"
	}

	sort.SliceStable(children, func(i, j int) bool {
		return parseLarkMillis(children[i].CreateTime) < parseLarkMillis(children[j].CreateTime)
	})

	truncated := 0
	if total > e.maxForwardChildren {
		truncated = total - e.maxForwardChildren
		children = children[:e.maxForwardChildren]
	}

	labeler := newSpeakerLabeler(names)
	lines := make([]string, 0, len(children))
	for _, c := range children {
		label := labeler.label(c)
		var text string
		switch {
		case c.MessageType == larkMsgTypeMergeForward:
			text = "[nested merge_forward, expand manually]"
		default:
			text = e.flattenMessage(c)
			if text == "" {
				text = "[empty message]"
			}
		}
		lines = append(lines, fmt.Sprintf("[%s]: %s", label, text))
	}
	body := strings.Join(lines, "\n")
	if truncated > 0 {
		body += fmt.Sprintf("\n... (%d more truncated)", truncated)
	}
	return fmt.Sprintf("<forwarded_messages count=\"%d\">\n%s\n</forwarded_messages>", total, body)
}

// flattenMessage 把一条拉取到的消息转成纯文本：先按 msg_type 结构压平，再对
// 消息自己的 mentions 解析 @_user_N 占位符。这里不剥 Bot 自己的 @（不同于入站
// decoder）——引用/转发的消息是历史上下文而非新触发，所以传空 bot 标识让每个
// @ 都渲染成可读的 @名字。
func (e *inboundEnricher) flattenMessage(m LarkMessage) string {
	if m.Deleted {
		return "[deleted message]"
	}
	raw := flattenContent(m.MessageType, m.Content)
	if raw == "" {
		return ""
	}
	return resolveMentions(raw, restMentionsToEvent(m.Mentions), "", "")
}

// restMentionsToEvent 把 IM REST 的 mention 形态（扁平字符串 id）适配成
// resolveMentions 消费的 WS 事件 larkMention 形态，让两条入站路径共用同一套
// @ 解析实现。
func restMentionsToEvent(ms []LarkMessageMention) []larkMention {
	if len(ms) == 0 {
		return nil
	}
	out := make([]larkMention, 0, len(ms))
	for _, m := range ms {
		lm := larkMention{Key: m.Key, Name: m.Name}
		lm.ID.OpenID = m.ID
		out = append(out, lm)
	}
	return out
}

// wrapQuoted 把引用块的内部内容包成 <quoted_message> 标签。
func wrapQuoted(messageID, sender, msgType, inner string) string {
	return fmt.Sprintf("<quoted_message message_id=%q sender=%q type=%q>\n%s\n</quoted_message>",
		messageID, sender, msgType, inner)
}

// quotedErrorBlock 生成引用父消息拉取失败的错误占位块。
func quotedErrorBlock(messageID string) string {
	return fmt.Sprintf("<quoted_message message_id=%q type=\"error\">[unable to fetch]</quoted_message>", messageID)
}

// forwardedErrorBlock 生成转发拉取失败的错误占位块。
func forwardedErrorBlock() string {
	return "<forwarded_messages type=\"error\">[unable to fetch]</forwarded_messages>"
}

// parseLarkMillis 把飞书的 epoch 毫秒字符串解析成 int64；不可解析时返回 0。
func parseLarkMillis(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// speakerLabeler 给一个渲染块内的发送者分配稳定、人可读的标签。Lark 消息条目
// 只携带发送者 id（负载里没有显示名），所以富化器经 Contact API 带外解析真实
// 名字，作为 sender-id -> name 映射传入。映射里有的发送者用真实名字标注；没有
// 的（联系人范围受限、已停用用户、名字查询失败）按首次出现顺序退回 "User 1"、
// "User 2"……。app 发送者恒为 "Bot"。
type speakerLabeler struct {
	names map[string]string // 已解析 open_id -> 显示名（可为 nil）
	seen  map[string]string
	n     int
}

// newSpeakerLabeler 构造说话者标签器。
func newSpeakerLabeler(names map[string]string) *speakerLabeler {
	return &speakerLabeler{names: names, seen: make(map[string]string)}
}

// label 返回某条消息发送者的稳定标签。
func (l *speakerLabeler) label(m LarkMessage) string {
	if m.SenderType == "app" {
		return "Bot"
	}
	key := m.SenderID
	if key == "" {
		key = "unknown"
	}
	if lbl, ok := l.seen[key]; ok {
		return lbl
	}
	var lbl string
	if name := l.names[key]; name != "" {
		lbl = name
	} else {
		l.n++
		lbl = fmt.Sprintf("User %d", l.n)
	}
	l.seen[key] = lbl
	return lbl
}
