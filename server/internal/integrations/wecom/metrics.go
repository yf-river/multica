// metrics.go —— 本适配器对外暴露的健康信号。
//
// 连接链路上的每一次失败都被刻意设计成「安静降级」。拨号失败、握手被服务端
// 拒绝，都把连接交还给 Supervisor 去退避重试；入站队列满了就停住读取循环、
// 不再排空 socket，直到 worker 追上进度。对聊天对面的人来说安静是对的，但对
// 背后运维的人就是错的——机器人已经一小时连不上，仪表盘上却什么都看不出来。
//
// 这里的计数器是按「什么值得被 paging」而不是按「全面覆盖」挑的：连接是不是
// 起不来、如果起不来是等个人处理还是等时间就行；以及读取循环是不是被跟不上的
// 入站 worker 逼着在等。
//
// 全程不带 installation id。它是一个无上限的标识符，metrics 包会直接拒绝这
// 一类 label（见 internal/metrics/labels.go 的 forbiddenMetricLabels）。要做归
// 因只能看结构化日志，而且日志里的归因并不统一：两个连接类计数带 id，因为它
// 们统计的失败同时也会回传给 Supervisor，由 Supervisor 带上 installation_id
// 记日志；两个入站类计数旁边什么都没有——队列阻塞写不产生任何日志行，所以
// 计数器只能告诉运维「有某个机器人落后了」，说不清是哪个。
package wecom

// Metrics 是本适配器上报数据的出口。每个方法都必须支持并发调用，且都不能
// 阻塞：它们跑在读取循环上。
type Metrics interface {
	// RecordConnectFailure —— 一次拨号、握手写、握手读没完成，或者服务端用
	// classifySubscribeAck 无法核验的握手码回复了（限流、平台侧故障）。
	// 不含纯凭证被拒的情况，那有单独的计数器：这里统计的一切都能自行恢复，
	// 那一种需要管理员介入。
	RecordConnectFailure()
	// RecordAuthFailure —— aibot_subscribe 因凭证问题被拒，按
	// classifySubscribeAck 的判断（ErrCredentialsRejected: 40001 / 40013）。
	// 刻意不是所有非零 errcode 都算：那些只代表「无法核验」的错误码归
	// RecordConnectFailure——为一个好端端的密钥去 paging 运维，等于再制造
	// 一次故障。机器人得等人修好凭证才会重连，所以这里的持续走高是告警，
	// 不是抖动。
	RecordAuthFailure()

	// RecordCallbackQueued —— 一条入站回调交给了 worker。它是其他所有
	// 入站数字的基准线。
	RecordCallbackQueued()
	// RecordCallbackQueueBlocked —— worker 队列满了，读取循环被迫等待。
	// 这是有意的背压：宁可让慢的入站停下来，也不丢消息。速率升高说明引擎
	// 跟不上某个机器人的流量，超过一定阈值后企业微信会认为 socket 一直没
	// 被读走，直接把连接顶掉。
	RecordCallbackQueueBlocked()

	// RecordStreamFinished / FellBack — how the bubble ended. A fall-back is
	// an ending that arrived as a new message because the bubble could not
	// take the closing frame; the words are not lost, but the experience is
	// the one the bubble was built to replace.
	//
	// Both are fed from the one line in sendersRegistry.recordEnding, and they
	// have to stay that way for the ratio to mean anything: every closer goes
	// through it — the answer, and the failure and cancellation notices the
	// typing indicator writes — so a ratio read off these two covers the same
	// population on both sides.
	RecordStreamFinished()
	RecordStreamFellBack()

	// RecordStreamOpened — one bubble is on a user's screen and something owes
	// it an ending. Counted where the handle is KEPT, which is not the same as
	// where a frame was accepted: an opening frame whose ack never came back
	// keeps its handle on purpose, because re-sending that stream id later
	// creates the message if the frame was lost.
	//
	// It is here because the ratio above cannot see the failure that matters
	// most. A bubble nobody ever closes moves neither counter — the relay gap
	// on a multi-replica deployment, a process restarted mid-run, a closing
	// frame that never happens — and from the two ending counters alone that
	// is indistinguishable from a quiet hour. opened minus finished minus
	// fell_back is that number, and it is the one an operator can act on.
	//
	// It does not settle to zero at any instant: bubbles in flight sit in the
	// difference, and a run can hold one for the length of the window. It is a
	// gauge of a backlog read over time, not a balance.
	RecordStreamOpened()

	// RecordOutboundDelivered — one reply reached the user. It is the
	// denominator: without it a flat drop counter cannot be told apart from a
	// quiet day, and "the bot went silent" is exactly the report that cannot
	// afford that ambiguity.
	RecordOutboundDelivered()
	// RecordOutboundDropped —— 本适配器欠用户一条回复却没送达，按原因打标
	// （outbound_outcome.go 的 closed reason 集合）。这里的每种原因都意味着
	// 有人在企业微信里等一个永远等不到的答案，所以它就是一个「错误总量」，
	// label 说明是哪一种失败。普通结局——在 Web 界面给企微绑定的会话打字、
	// 触发到回复之间安装被撤销——不在这里计，RecordOutboundSkipped 收。
	RecordOutboundDropped(reason string)
	// RecordOutboundSkipped —— 一次本适配器本就不打算投递的完成事件，因为
	// 它从一开始就不欠某个企业微信用户。刻意与 dropped 分开：把 Web 界面
	// 提问的答案当成一次失败的企业微信投递，会让普通 Web 使用看起来像故障。
	RecordOutboundSkipped(reason string)

	// RecordAttachmentDelivered / RecordAttachmentDropped 按「文件」计数，
	// 每个文件一条；上面三个按「回复」计数，每条回复一条。两个单位必须分开：
	// 一条回复文字到了、附件没到，是「送达的回复 + 失败的附件」，硬折成
	// 一个数字只能朝其中一个方向撒谎。
	RecordAttachmentDelivered()
	RecordAttachmentDropped(reason string)

	// RecordAttachmentDeliveryShed 计一次「调度」决策：某次投递尝试在查库
	// 之前就被拒之门外。它独立成类是因为这一刻谁都不知道这一轮带 0 个还是
	// 5 个文件——把它算成文件丢弃，两个方向都会编造出错误的数量。
	RecordAttachmentDeliveryShed()

	// RecordOutboundUnconfirmed / RecordAttachmentUnconfirmed —— 结局是
	// UNKNOWN（未知）：帧已经上了线路（或等待它裁定的过程被打断），消息
	// 可能已经出现在用户面前。它们单独成对，是因为并入 dropped 会把一个确凿
	// 的失败率注水——其中大部分发送可能成功了，按丢弃率 paging 的运维会被
	// 为「已经送达」的投递吵醒。
	RecordOutboundUnconfirmed(reason string)
	RecordAttachmentUnconfirmed(reason string)

	// RecordRelayShed 计一次跨副本分发器上的「准入」决策：一条被路由的帧
	// 因队列已满被拒。它独立成类，按帧类型打标，因为上面那些是「每条回复」
	// 计的，而中继还携带入站通知——把入站推送当成一条被丢弃的回复去计，会让
	// delivered/dropped 比例反映「哪个副本恰好握着 socket」，而不是任何真实
	// 结局。无论哪个副本拒了帧，都照记不误。
	//
	// 它不动任何回复计数器，连握着 socket 的那个副本也不动。每个副本都会读
	// 到每一帧，租约切换期间甚至有两个副本同时握着 sender，所以任何副本都
	// 无法在本地判断「自己丢掉的那次是否让用户付出了代价」——各答各的账，就
	// 会把同一条回复既报成已送达又报成已丢弃。一条被丢弃的回复是否真的丢了，
	// 最终由路由它的副本在 RelayOutbound.watchOutcomes 里裁定一次。
	RecordRelayShed(kind string)
}

// nopMetrics 是构造函数兜底用的空实现。nil 出口绝不能变成读取循环上的
// 空指针解引用。
type nopMetrics struct{}

func (nopMetrics) RecordConnectFailure()              {}
func (nopMetrics) RecordAuthFailure()                 {}
func (nopMetrics) RecordCallbackQueued()              {}
func (nopMetrics) RecordCallbackQueueBlocked()        {}
func (nopMetrics) RecordStreamOpened()                {}
func (nopMetrics) RecordStreamFinished()              {}
func (nopMetrics) RecordStreamFellBack()              {}
func (nopMetrics) RecordOutboundDelivered()           {}
func (nopMetrics) RecordOutboundDropped(string)       {}
func (nopMetrics) RecordOutboundSkipped(string)       {}
func (nopMetrics) RecordAttachmentDelivered()         {}
func (nopMetrics) RecordAttachmentDropped(string)     {}
func (nopMetrics) RecordAttachmentDeliveryShed()      {}
func (nopMetrics) RecordOutboundUnconfirmed(string)   {}
func (nopMetrics) RecordAttachmentUnconfirmed(string) {}
func (nopMetrics) RecordRelayShed(string)             {}

// orNopMetrics 把未设置的出口（nil）替换成一个可以安全调用的空实现。
func orNopMetrics(m Metrics) Metrics {
	if m == nil {
		return nopMetrics{}
	}
	return m
}
