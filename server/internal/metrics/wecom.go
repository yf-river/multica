// 本文件实现企微（WeCom）集成的 Prometheus 指标：连接/鉴权失败、回调入队积压，
// 以及「agent 回复/附件是否真的送达企微用户」的投递去向分解。指标对象实现了
// integrations/wecom 适配器定义的 Metrics 接口，是业务适配层与观测系统的唯一桥梁。
package metrics

import "github.com/prometheus/client_golang/prometheus"

// WecomMetrics 是企微适配器 Metrics 接口（server/internal/integrations/wecom/metrics.go）
// 背后的生产实现。所有计数都遵守本包统一规则：绝不使用 installation_id / workspace_id /
// session_id 这类无上界标识作标签，归属信息交给结构化日志。
//
// 设计基调是「静默降级」：拨号失败、握手被服务器拒绝，都只是把连接交还 Supervisor
// 退避重试；入队队列满了读循环就停着等 worker 追上来，不丢任何东西。这三种情况操作者
// 从指标上看不到任何差别——「机器人从周二就掉线」和「今天恰好没人发消息」在外观上一样
// 安静。因此连接侧拆了两个计数器，把「等一等就好」和「需要人动手」分开：
//
//   - ConnectFailures（连接失败）：拨号连不上、或握手被拒但理由只是服务器无法核实
//     （限流、平台侧故障）——基础设施类问题，通常自己会恢复。
//   - AuthFailures（鉴权失败）：服务器核实了凭据本身不合法（企微 errcode 40001/40013，
//     密钥错误或机器人被删）。这种错每次退避重试都会以同样方式复现，直到有人修好配置。
//     混成一个数字，操作者就分不清「等着」还是「去换密钥」。
//
// 握手应答落在哪一边由适配器里的 classifySubscribeAck 判断，本包只负责计数。它无法
// 核实的那类 ack（限流、平台侧故障）被归为连接失败、即「等一等」那侧——因为一个本来
// 好好的密钥被误换，会制造第二场故障。
//
// 出站（outbound）计数回答的是另一类问题，也就是 GH #7215 与 GH #6890 里提的：
// 回复已经产出、记录里也有，但企微群里始终安静。多种互不相干的原因都会导致这种外观，
// 从聊天侧看是同一个症状，所以按操作者能下手的地方逐个计数：
//   - dropped（丢弃，带 reason）：平台拒收帧，或本进程里没有该安装的 socket。这是真丢失。
//   - skipped（跳过，带 reason）：本来就不欠企微的——在 Multica Web 端发起、或回复前
//     安装已被吊销、或空回复无内容。算成 drop 会让普通 Web 使用被误读为企微故障。
//   - unconfirmed（未确认，带 reason）：写到了连接上但 verdict 迟迟没回、等待被打断。
//     消息可能已上屏，所以不算丢弃，也不该提示重发。
//     Delivered（送达）是上述分解的分母：没有它，0% 丢弃率和「没人发消息」长得一模一样。
//
// reason 是个封闭集合（见 wecom/outbound_outcome.go），是本组唯一的标签，基数受
// 构造约束，绝不会混入安装/工作区/会话 ID。
type WecomMetrics struct {
	// ConnectFailures 连接尝试失败计数：套接字没起来，或握手被服务器以「无法核实
	// 机器人」类理由拒绝（限流/平台侧故障）。不含凭据被拒——那单独算 AuthFailures。
	ConnectFailures prometheus.Counter
	// AuthFailures 服务器以凭据本身不合法为由拒绝握手（企微 errcode 40001/40013）。
	// 机器人会一直掉线，直到有人修好安装。
	AuthFailures prometheus.Counter
	// CallbacksQueued 交给 ingest worker 的入站回调数，是其它入站数字的基准。
	CallbacksQueued prometheus.Counter
	// CallbackQueueBlocked 读循环因入队队列满而等待的次数。背压是设计使然；该值持续
	// 上涨说明引擎处理不过来，套接字即将停止被排空。
	CallbackQueueBlocked prometheus.Counter
	// OutboundDelivered 真正送达企微用户的 agent 回复数。丢弃分解的分母。
	OutboundDelivered prometheus.Counter
	// OutboundDropped 欠了用户却没送达的回复，按 reason 拆。每个 reason 都意味着
	// 有人正等一个不会来的答复；本就无需投递的完成品单独算进 OutboundSkipped。
	OutboundDropped *prometheus.CounterVec
	// OutboundSkipped 本来就不需要投给企微的完成品，按 reason 拆。不算丢弃是因为
	// 这些都不是投递失败。
	OutboundSkipped *prometheus.CounterVec
	// AttachmentDelivered 送达用户的文件数。按「文件」计，与按「回复」计的
	// delivered/dropped/skipped 三件套单位不同。
	AttachmentDelivered prometheus.Counter
	// AttachmentDropped agent 产出但没到用户手上的文件，按 reason 拆。与回复计数
	// 分开：一条回复若文字到了而附件没到，是「已送达的回复 + 失败的附件」，
	// 一个数字说不清这两件事。
	AttachmentDropped *prometheus.CounterVec
	// AttachmentSheds 在查询执行前就被拒绝入场的附件投递尝试数。按「调度决定」计而
	// 非按文件计——此关口还不知道该轮带 0 个还是 5 个文件，按文件数造假基数。
	AttachmentSheds prometheus.Counter
	// OutboundUnconfirmed 投递结果未知的回复，按 reason 拆：帧已上线路
	// （write_attempted）、verdict 没回来（ack_timeout）、等待被打断（interrupted）。
	// 消息可能已在用户面前，因此不算丢弃——按丢弃率告警的人不该为「大概送出去了」
	// 的投递被惊醒，这里也不该触发重发。
	OutboundUnconfirmed *prometheus.CounterVec
	// AttachmentUnconfirmed 投递结果未知的文件，按 reason 拆。按「文件」计，单位与
	// attachment delivered/dropped 一对一致。
	StreamOpened          prometheus.Counter
	StreamFinished        prometheus.Counter
	StreamFellBack        prometheus.Counter
	AttachmentUnconfirmed *prometheus.CounterVec
	// RelayShed 跨副本分发器因分片队列满而拒收的帧数，按帧类型（reply|inbox）标。
	// 单独成指标是因为 relay 既承载收件箱通知也承载回复，而回复计数器是按回复计的。
	// 记录在拒收帧的那个副本上，且不动任何回复计数：每个副本都会读到每一帧，
	// 单个副本无法在本地判断自己的丢弃是否让用户损失了什么。某个被丢弃的回复是否真的
	// 丢了，由路由它的那个副本最终判定——当没有任何副本认领投递时，会落成
	// outbound_dropped_total{reason="no_live_connection"}。
	RelayShed *prometheus.CounterVec
}

// NewWecomMetrics 创建企微指标全集。计数器统一挂 multica_wecom_ 命名空间；
// 局部 helper counter 省去每组计数器都写一遍命名空间/子系统的样板。
func NewWecomMetrics() *WecomMetrics {
	counter := func(name, help string) prometheus.Counter {
		return prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "multica", Subsystem: "wecom", Name: name, Help: help,
		})
	}
	return &WecomMetrics{
		ConnectFailures: counter("connect_failures_total",
			"Long-connection attempts that failed for a reason nobody has to act on: the socket never came up, or the server answered the handshake with a code that only means it could not verify the bot (a throttle, a platform-side failure). Excludes credential rejections, which are counted apart."),
		AuthFailures: counter("auth_failures_total",
			"Long-connection handshakes the server refused on the credentials themselves (WeCom errcode 40001 / 40013). The bot stays down until somebody fixes the installation."),
		CallbacksQueued: counter("inbound_callbacks_total",
			"Inbound callbacks handed to the ingest worker. The baseline every other inbound number is read against."),
		CallbackQueueBlocked: counter("inbound_queue_blocked_total",
			"Times the read loop had to wait on a full ingest queue. Backpressure by design; a rising rate means the engine is behind and the socket is about to stop being drained."),
		StreamOpened: counter("stream_opened_total",
			"Bubbles painted on ingest. The denominator for the two below: opened minus finished minus fell_back is the number nobody ever ended, which is the only failure the finished/fell_back ratio cannot show."),
		StreamFinished: counter("stream_finished_total",
			"Answers that landed in the bubble the question opened."),
		StreamFellBack: counter("stream_fell_back_total",
			"Answers sent as a new message because the bubble refused the closing frame. The answer is not lost; the experience is the one the bubble was built to replace."),
		OutboundDelivered: counter("outbound_delivered_total",
			"Agent replies this adapter put in front of a WeCom user. The denominator the drop breakdown is read against."),
		OutboundDropped: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica", Subsystem: "wecom", Name: "outbound_dropped_total",
			Help: "Agent replies the adapter owed a WeCom user and did not deliver, by reason. Every reason here means somebody in WeCom is waiting on an answer that is not coming; most of them are settled before the frame reaches WeCom, so read this as a count of unpaid obligations rather than of network failures. The completions this adapter did not owe, and the ones nothing here can attribute to a WeCom chat, are counted apart, in outbound_skipped_total.",
		}, []string{"reason"}),
		OutboundSkipped: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica", Subsystem: "wecom", Name: "outbound_skipped_total",
			Help: "Completions this adapter did not send to WeCom, by reason: it did not owe them, or nothing here could attribute them to a WeCom chat. Kept apart from dropped, which counts the replies a WeCom user was owed and did not get; what separates the two is the obligation, not whether a frame was written. Four of these were never owed to WeCom, and counting them as drops would make ordinary usage read as an outage: origin_not_channel is a question typed in Multica on a WeCom-bound session, not_wecom_turn is another platform's turn on the bus this subscriber shares, installation_inactive means there is no longer an installation to deliver through, nothing_to_say is an empty completion carrying no file. The fifth is the one label here worth an alert: no_delivery_row is a turn the channel ingested with no row saying which chat, so a reply may well be owed and nothing left can name the room — in practice a task enqueued before an upgrade and finishing after it. The sixth is neither: route_unattributable is a turn with no row and no batch owner, so nothing here can say whether it was ever a channel's, let alone whether a reply was owed — in practice a row old enough to predate batch owners still finishing, and a rising count says only that such rows remain.",
		}, []string{"reason"}),
		AttachmentDelivered: counter("outbound_attachment_delivered_total",
			"Files put in front of a WeCom user. Counts FILES; the outbound_delivered/dropped/skipped trio counts REPLIES."),
		AttachmentDropped: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica", Subsystem: "wecom", Name: "outbound_attachment_dropped_total",
			Help: "Files an agent produced that did not reach the WeCom user, by reason. Separate from the reply counters because a reply whose words arrived and whose file did not is a delivered reply with a failed attachment, and one number cannot say both.",
		}, []string{"reason"}),
		AttachmentSheds: counter("outbound_attachment_delivery_shed_total",
			"Attachment delivery attempts refused admission before the lookup ran. Counts SCHEDULING decisions, not files: at that point nothing knows whether the turn carries zero files or five, so a per-file count from this gate would fabricate cardinality."),
		OutboundUnconfirmed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica", Subsystem: "wecom", Name: "outbound_unconfirmed_total",
			Help: "Agent replies whose delivery outcome is UNKNOWN: the frame reached the wire (write_attempted), the verdict never came back (ack_timeout), or the wait was cut short (interrupted). The message may already be in front of the user, which is why these are not drops — an operator paging on the drop rate must not be paged for deliveries that probably happened, and nothing here should prompt a resend.",
		}, []string{"reason"}),
		AttachmentUnconfirmed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica", Subsystem: "wecom", Name: "outbound_attachment_unconfirmed_total",
			Help: "Files whose delivery outcome is UNKNOWN, by reason. Counts FILES, same unit as the attachment delivered/dropped pair; see outbound_unconfirmed_total for why unknown is not a drop.",
		}, []string{"reason"}),
		RelayShed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica", Subsystem: "wecom", Name: "outbound_relay_shed_total",
			Help: "Frames the cross-replica dispatcher refused because a shard queue was full, by kind (reply|inbox). An ADMISSION decision, not a per-reply outcome: the relay carries inbox notifications too, and counting those as dropped replies would make the delivered/dropped ratio track which replica held a socket instead of what happened to anyone's message. Recorded on whichever replica refused the frame, and it moves no reply counter: every replica reads every frame, so no replica can tell locally whether its own shed cost the user anything. Whether a shed reply was in fact lost is settled once by the replica that routed it, as outbound_dropped_total{reason=\"no_live_connection\"} when no replica claimed the delivery.",
		}, []string{"kind"}),
	}
}

// Collectors 返回本组全部指标，供调用方一次性注册进 prometheus.Registry。
func (m *WecomMetrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{
		m.ConnectFailures, m.AuthFailures,
		m.CallbacksQueued, m.CallbackQueueBlocked,
		m.StreamOpened, m.StreamFinished, m.StreamFellBack,
		m.OutboundDelivered, m.OutboundDropped, m.OutboundSkipped,
		m.AttachmentDelivered, m.AttachmentDropped, m.AttachmentSheds,
		m.OutboundUnconfirmed, m.AttachmentUnconfirmed,
		m.RelayShed,
	}
}

// ---- 以下方法实现适配器的 Metrics 接口，逐条对号入座把业务事件记成计数 ----

// RecordConnectFailure 记录一次连接失败（无理由参数的纯计数）。
func (m *WecomMetrics) RecordConnectFailure() { m.ConnectFailures.Inc() }

// RecordAuthFailure 记录一次鉴权失败（无理由参数的纯计数）。
func (m *WecomMetrics) RecordAuthFailure() { m.AuthFailures.Inc() }

// RecordCallbackQueued 记录一个入站回调已入队。
func (m *WecomMetrics) RecordCallbackQueued() { m.CallbacksQueued.Inc() }

// RecordCallbackQueueBlocked 记录一次因入队队列满导致的等待。
func (m *WecomMetrics) RecordCallbackQueueBlocked() { m.CallbackQueueBlocked.Inc() }

// RecordOutboundDelivered 记录一条 agent 回复已送达企微用户。
func (m *WecomMetrics) RecordOutboundDelivered() { m.OutboundDelivered.Inc() }

// RecordOutboundDropped 按 reason 记录一条未送达的回复。
func (m *WecomMetrics) RecordOutboundDropped(reason string) {
	m.OutboundDropped.WithLabelValues(reason).Inc()
}

// RecordOutboundSkipped 按 reason 记录一次本就不需投递的完成品。
func (m *WecomMetrics) RecordOutboundSkipped(reason string) {
	m.OutboundSkipped.WithLabelValues(reason).Inc()
}

// RecordAttachmentDelivered 记录一个文件已送达企微用户。
func (m *WecomMetrics) RecordAttachmentDelivered() { m.AttachmentDelivered.Inc() }

// RecordAttachmentDropped 按 reason 记录一个未送达的文件。
func (m *WecomMetrics) RecordAttachmentDropped(reason string) {
	m.AttachmentDropped.WithLabelValues(reason).Inc()
}

// RecordAttachmentDeliveryShed 记录一次在查询前就被拒绝入场的附件投递调度。
func (m *WecomMetrics) RecordAttachmentDeliveryShed() { m.AttachmentSheds.Inc() }

// RecordOutboundUnconfirmed 按 reason 记录一条投递结果未知的回复。
func (m *WecomMetrics) RecordOutboundUnconfirmed(reason string) {
	m.OutboundUnconfirmed.WithLabelValues(reason).Inc()
}

// RecordAttachmentUnconfirmed 按 reason 记录一个投递结果未知的文件。
func (m *WecomMetrics) RecordAttachmentUnconfirmed(reason string) {
	m.AttachmentUnconfirmed.WithLabelValues(reason).Inc()
}

// RecordRelayShed 按帧类型 kind（reply|inbox）记录一次 relay 拒收。
func (m *WecomMetrics) RecordRelayShed(kind string) {
	m.RelayShed.WithLabelValues(kind).Inc()
}

func (m *WecomMetrics) RecordStreamOpened()   { m.StreamOpened.Inc() }
func (m *WecomMetrics) RecordStreamFinished() { m.StreamFinished.Inc() }
func (m *WecomMetrics) RecordStreamFellBack() { m.StreamFellBack.Inc() }
