// outbound_outcome.go —— 一条「本适配器被要求投递的回复」最后怎么了，以运维能数
// 的方式呈现。
//
// 出站路径上每一个「没把话送到用户面前就结束一轮」的分支，过去都是一个光秃秃的
// `return nil` 或孤零零一条 WARN。这正是 GH #7215 和 #6890 的形状：答案在
// Multica 转写里、企微会话一片安静，而服务端证据要么是一行没带原因的话、要么什
// 么都没有。多种不同的原因产出同一个无法区分的症状时，我们和部署的运维都说不清
// 是哪一个触发的，所谓修复只是猜。
//
// 所以这些分支每一个都自己报上名来。计数器是持久的那一半——它总会被自增——日志
// 级别是判断的那一半：该由人行动的原因记 WARN，健康部署里稀松平常的只记 DEBUG、
// 只当作速率来读。
//
// 原因集合刻意封闭。它是 metric label，开放集合正是 forbiddenMetricLabels 存在
// 要防的无界基数问题。
package wecom

import (
	"context"
	"errors"

	"github.com/multica-ai/multica/server/internal/events"
)

// dropReason 点名一条回复为何没送到用户面前。封闭集合：见文件头。
type dropReason string

const (
	// dropNoConnection —— 没有活 WebSocket 承载这条回复。两种情况会走到它。
	// 无 relay：本进程内没有——多副本部署上这无法与「租约只是恰好在别处」区分。
	// 有 relay：回复确实被路由给每个副本、却没有一个握着连接——这正是
	// SELF_HOSTING.md 描述的残余窗口——由路由它的那个副本从无人认领的占位记录一
	// 次（RelayOutbound.watchOutcomes）。
	dropNoConnection dropReason = "no_live_connection"

	// dropTaskMissing —— 完成事件所属的任务解析不出来：事件上没有 id，或行在它
	// 的结局还在路上时被清掉。
	dropTaskMissing dropReason = "task_missing"

	// dropPlatformRefused —— 企微对这次发送回了非零 errcode。一句说出口的拒绝：
	// 帧超预算、机器人已不在会话、租户被限流。
	dropPlatformRefused dropReason = "platform_refused"

	// dropTransport —— 什么都没到达平台，而且是本地原因。写本身失败、写之前的
	// 某次查询失败、或这次投递自己的预算在轮到上线之前耗尽。三者共同的、也是运
	// 维需要的事实是：失败在我们这一侧的 socket 这边，所以没给任何人展示过任何
	// 东西。
	dropTransport dropReason = "transport_error"

	// dropAttachmentNotAdmitted —— 因为同时在跑 / 在等的太多，这次投递被拒。
	//
	// 它出现在两个「单位」上、各指不同的事。在文件计数器上是「一个不会被送出的
	// 文件」。在回复计数器上，它只在「文件本身就是回复」时出现——一条空完成只因
	// 绑了东西才走到投递——那里它意味着用户什么都没得到。文字已经落地的回复在这
	// 道闸之前就结算了，永远不会走到它。
	dropAttachmentNotAdmitted dropReason = "attachment_not_admitted"
)

// skipReason 点名一条「本适配器从一开始就不打算投递」的完成。单独成集、单独放
// 计数器后面，因为「我们选择不发这个」和「我们欠这个且失败了」回答的是两个不同
// 的问题，其中只有一个算事件。
type skipReason string

const (
	// skipOriginNotChannel —— 这一轮是在 Multica Web 界面问的，却落在一条发源
	// 于 WeCom 的会话上，所以它的答案只属于 Multica。健康部署里稀松平常，也是
	// 繁忙工作区里这个计数器的最大单一来源。
	skipOriginNotChannel skipReason = "origin_not_channel"

	// skipInstallationInactive —— 安装在触发与回复之间被撤销。不是投递失败：已
	// 经没有可投的安装了，机器人也从用户那一侧消失了。
	skipInstallationInactive skipReason = "installation_inactive"

	// skipNothingToSay —— 一条不带文件的空完成。这里从来就没有消息。
	skipNothingToSay skipReason = "nothing_to_say"
)

// actionable 报告某个原因是否值得一个人去看。其余的在健康部署里稀松平常、会把日
// 志淹掉。现在每条 drop 都值得看——因为那两个普通结局已移去 skipReason。
func (r dropReason) actionable() bool { return true }

// errNoLiveConnection —— 本进程里这条安装没有活 WebSocket。做成哨兵而不是在调用
// 点现造 errors.New，好让 classifyDrop 能点名它、而不必对散文做模式匹配。
var errNoLiveConnection = errors.New("wecom: connection not ready on this replica")

// unconfirmedReason 点名一个「结局未知」的失败——消息可能已经在用户面前——对确凿
// 失败返回 ""。集合与 sendOutcome 的 deliveryUnknown 精确对应：裁定一直没来、写本
// 身报出的失败（对端可能握着本地报错那侧的字节）、以及两者还在挂起时 ctx 被打断。
// 调用方必须在 classifyDrop 之前先查它：把未知当丢弃上报，等于叫运维重发一条用户
// 可能已经收到的消息。
func unconfirmedReason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errAckTimeout):
		return "ack_timeout"
	case errors.Is(err, errWriteAttempted):
		return "write_attempted"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "interrupted"
	}
	return ""
}

// classifyDrop 把一次确凿的发送失败变成运维读的那个原因。是否确凿是调用方的责任：
// 先查 unconfirmedReason。
func classifyDrop(err error) dropReason {
	var apiErr *wecomAPIError
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errNoLiveConnection):
		return dropNoConnection
	case errors.As(err, &apiErr):
		return dropPlatformRefused
	default:
		return dropTransport
	}
}

// dropped 记录一条未送达的回复：永远是计数器，外加一行级别说明是否该有人行动的
// 日志。
//
// 刻意不作为错误返回。这些分支里有好几个到达的是「从来就不归本适配器答」的事件，
// 把它们变成错误会改变 processEvent 的调用方——以及一打既有测试——对「这里没什
// 么可做」的理解。
func (o *Outbound) dropped(ctx context.Context, e events.Event, reason dropReason, err error) {
	o.droppedFor(ctx, e.ChatSessionID, e.Type, reason, err)
}

// droppedFor 是给「手上有会话 id 而没有事件」的调用方用的 dropped——比如附件路
// 径，它在事件早已消失后很久才跑。
func (o *Outbound) droppedFor(ctx context.Context, sessionID, eventType string, reason dropReason, err error) {
	o.mx().RecordOutboundDropped(string(reason))
	attrs := []any{
		"reason", string(reason),
		"chat_session_id", sessionID,
		"event", eventType,
	}
	if err != nil {
		attrs = append(attrs, "error", err)
	}
	if reason.actionable() {
		o.logger.WarnContext(ctx, "wecom outbound: reply not delivered", attrs...)
		return
	}
	o.logger.DebugContext(ctx, "wecom outbound: reply not delivered", attrs...)
}

// unconfirmed 记录一条结局未知的回复。WARN，因为「要不要重发」的决定者需要知道这
// 不是一次失败。
func (o *Outbound) unconfirmed(ctx context.Context, e events.Event, reason string, err error) {
	o.unconfirmedFor(ctx, e.ChatSessionID, e.Type, reason, err)
}

func (o *Outbound) unconfirmedFor(ctx context.Context, sessionID, eventType, reason string, err error) {
	o.mx().RecordOutboundUnconfirmed(reason)
	attrs := []any{"reason", reason, "chat_session_id", sessionID, "event", eventType}
	if err != nil {
		attrs = append(attrs, "error", err)
	}
	o.logger.WarnContext(ctx, "wecom outbound: reply delivery unconfirmed", attrs...)
}

// attachmentUnconfirmed 记录一个结局未知的 FILE。
func (o *Outbound) attachmentUnconfirmed(ctx context.Context, reason string, err error) {
	o.mx().RecordAttachmentUnconfirmed(reason)
	attrs := []any{"reason", reason}
	if err != nil {
		attrs = append(attrs, "error", err)
	}
	o.logger.WarnContext(ctx, "wecom outbound: attachment delivery unconfirmed", attrs...)
}

// skipped 记录一条本适配器从不打算投递的完成。永远 DEBUG：没有一个是事件，而在
// 一个「人们用 Web 界面跑企微绑定会话」的工作区里，这是本文件最忙的路径。
func (o *Outbound) skipped(ctx context.Context, e events.Event, reason skipReason) {
	o.skippedFor(ctx, e.ChatSessionID, reason)
}

func (o *Outbound) skippedFor(ctx context.Context, sessionID string, reason skipReason) {
	o.mx().RecordOutboundSkipped(string(reason))
	o.logger.DebugContext(ctx, "wecom outbound: reply not owed to WeCom",
		"reason", string(reason), "chat_session_id", sessionID)
}

// attachmentDelivered / attachmentDropped 记录一个 FILE。为什么文件与回复分开计
// 见 Metrics 接口上的说明。
func (o *Outbound) attachmentDelivered() { o.mx().RecordAttachmentDelivered() }

func (o *Outbound) attachmentDropped(ctx context.Context, reason dropReason, err error) {
	o.mx().RecordAttachmentDropped(string(reason))
	attrs := []any{"reason", string(reason)}
	if err != nil {
		attrs = append(attrs, "error", err)
	}
	o.logger.WarnContext(ctx, "wecom outbound: attachment not delivered", attrs...)
}

// attachmentShed 记录一次在查询之前就被拒绝准入的投递尝试。不是文件计数：此刻没
// 人知道文件存不存在。
func (o *Outbound) attachmentShed() { o.mx().RecordAttachmentDeliveryShed() }

// worseUnconfirmedReason 是 worseDropReason 在另一个单位上的孪生。一条回复的文件
// 因多于一个原因回到 unknown 时，它的结局要由一条规则选出，而不是靠循环恰好停在
// 哪一行——这和确凿侧要有文档化的优先级是同一个理由。
//
// 按各自「对帧确立了多具体的事实」排序。ack_timeout 最具体：帧已上线、只差裁定。
// write_attempted 其次：本地报了失败，而对端可能仍握着字节。interrupted 说得最少
// ——等待结束了、而结束在哪不可从此处得知。
func worseUnconfirmedReason(a, b string) string {
	rank := func(r string) int {
		switch r {
		case "ack_timeout":
			return 3
		case "write_attempted":
			return 2
		case "interrupted":
			return 1
		default:
			return 0
		}
	}
	if rank(b) > rank(a) {
		return b
	}
	return a
}

// worseDropReason 是「回复的文件因多于一个原因失败」时的聚合规则。优先级：说出口
// 的拒绝 > 本地传输失败 > 一直没来的裁定——按各自对「内容为何没到」确立的事实有
// 多具体排序。稳定且有文档，让多文件回复的原因是一条规则，而不是循环顺序的巧合。
func worseDropReason(a, b dropReason) dropReason {
	rank := func(r dropReason) int {
		switch r {
		case dropPlatformRefused:
			return 3
		case dropTransport:
			return 2
		default:
			return 0
		}
	}
	if rank(b) > rank(a) {
		return b
	}
	return a
}

// delivered 记录一条送到用户面前的回复。没有它，丢弃计数器就没有分母，「今天零丢
// 弃」无法和「今天零流量」区分——那正是 #7215 被报成的那同一种静默。
func (o *Outbound) delivered() { o.mx().RecordOutboundDelivered() }

// mx 返回 metrics 出口，没有则给空实现。与 wecomChannel.mx 对齐。
func (o *Outbound) mx() Metrics { return orNopMetrics(o.metrics) }

// WithOutboundMetrics 把适配器的健康出口挂到订阅者上。
func WithOutboundMetrics(m Metrics) OutboundOption {
	return func(o *Outbound) { o.metrics = m }
}
