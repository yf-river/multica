// senders_registry.go —— 一张进程级的「installation_id → 存活 wsSender」
// 小映射表。wecomChannel.Connect 在进入时登记、退出时清除；OutboundReplier
// 和 Outbound 按 installation id 查表，把 aibot_send_msg 压进入站循环所持有
// 的同一条 socket（aibot 没有 REST 出站通道，所有写出都走 WebSocket）。
// wecomChannel.Send 不是读者——它直接返回 ErrSendNotSupported。
//
// 为什么用注册表而不是把 sender 挂在 wecomChannel 上：OutboundReplier 在
// 启动时就用共享的 engine.Router 建好了，它没有按安装划分的 Channel 句柄。
// 引擎调 Replier.Reply 时传入的是 engine.ResolvedInstallation，里面只有
// installation id，没有 Channel。这张注册表就是中间的接缝：让启动时就存在的
// Replier 够得到每条安装的存活连接，而不用把 Channel 一路穿针引线进引擎。
package wecom

import (
	"context"
	"sync"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
)

// sendersRegistry 是一张 goroutine 安全的 installation_id → wsSender 映射。
// SendersRegistry 之所以要导出，只是为了让启动流程能在中继的分片读者启动前
// 先造出一张；其余方法一律不导出。
type SendersRegistry = sendersRegistry

type sendersRegistry struct {
	mu    sync.RWMutex
	byKey map[string]*wsSender
	// metrics is the health sink every write through this registry reports
	// to. Set once at boot via WithMetrics; a registry built without one
	// discards, which is what a deployment with /metrics off gets.
	metrics Metrics
}

// newSendersRegistry 构造一张空注册表。
func newSendersRegistry() *sendersRegistry {
	return &sendersRegistry{byKey: make(map[string]*wsSender), metrics: nopMetrics{}}
}

// WithMetrics points the registry's counters at a real sink. Called once at
// boot, before any connection exists.
//
// The registry is where this belongs rather than in each caller's
// constructor: it is the one object every outbound write already goes
// through, and it is already held by the thing that needs to report — the
// outbound subscriber — which has no other reason to know that metrics exist.
func (r *sendersRegistry) WithMetrics(m Metrics) *sendersRegistry {
	r.metrics = orNopMetrics(m)
	return r
}

// mx is the sink, safe to call on a registry built by a test literal.
func (r *sendersRegistry) mx() Metrics {
	if r == nil || r.metrics == nil {
		return nopMetrics{}
	}
	return r.metrics
}

// NewSendersRegistry 是公开构造函数，启动时用它把同一张注册表同时注入
// wecom 的 ChannelDeps（写侧）和 OutboundReplier（读侧）。保持导出是为了
// router.go 接线时不必去 import 一个未导出类型。
func NewSendersRegistry() *SendersRegistry { return newSendersRegistry() }

func (r *sendersRegistry) set(id pgtype.UUID, s *wsSender) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byKey[util.UUIDToString(id)] = s
}

// clear 移除该安装的条目，但仅当 s 仍是这个 id 下登记的 sender 时才删。
// 正在退出的旧一代连接不能把接替自己的新连接挤掉：Connect 在进入时登记、
// 用 defer 在退出时清除，所以当租约在旧 socket 仍排空期间发生翻转时，新旧
// 两会短暂重叠，输家的 defer 会在赢家 set 之后才跑。此时若无条件 delete，
// 注册表会在一条健康连接尚在的情况下被清空，所有出站推送都解析成 nil——
// 机器人静默哑火，日志里却没有任何线索，直到下次重连碰巧重新登记。
//
// dingtalk_channel.go:74 用 `CompareAndSwap(c, nil)` 守护同样的交接；slack
// 和飞书根本没有注册表，因为它们的出站是 REST。WeCom 是唯一曾经无条件删除
// 的平台。
func (r *sendersRegistry) clear(id pgtype.UUID, s *wsSender) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := util.UUIDToString(id)
	if cur, ok := r.byKey[key]; ok && cur != s {
		return
	}
	delete(r.byKey, key)
}

// get 返回某条安装当前存活的 wsSender；没有任何连接被持有时返回 nil。
// 调用方必须把 nil 当作「连接未就绪」——Supervisor 可能正处在租约翻转后的
// 重连中。
func (r *sendersRegistry) get(id pgtype.UUID) *wsSender {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byKey[util.UUIDToString(id)]
}

// stream writes one frame of a streaming reply to the bubble h describes.
//
// A stream frame is only meaningful while the req_id it echoes is still fresh,
// so a frame that cannot go out now is worthless later — there is nothing
// useful to do with it but report the failure and let the caller say the same
// words as an ordinary message instead.
//
// The sender is resolved HERE, per frame, rather than captured when the bubble
// opened, and that is load-bearing rather than redundant: a callback's req_id
// belongs to the TURN on WeCom's side, not to the connection it arrived on. A
// bubble opened before a reconnect is therefore finished after it, over
// whatever socket the installation holds by then, which is what lets a run
// outlive the connection its question came in on. Binding a connection at
// stream-open time reads like the obvious tightening and would strand the
// bubble on every reconnect — a failure that shows up only when the connection
// flaps, and never in a test.
//
// Measured 2026-08-09 against a live tenant, with our own backend stopped so
// nothing competed for the bot's socket: one connection took a real
// aibot_msg_callback, opened a stream on it and closed. A second connection,
// dialled and subscribed fresh, then refreshed that stream in place (same
// stream id) and opened a further one on the same req_id (new stream id) —
// errcode 0 both times, and both confirmed by reading the chat rather than by
// the errcode, since a server that accepts a frame and drops it looks the same
// on the wire.
//
// This is the server's req_id. The ones newReqID mints for our own frames are
// a separate key space, meaningful only on the connection that wrote them
// (ws_sender.go).
func (r *sendersRegistry) stream(ctx context.Context, h streamHandle, content string, finish bool) error {
	sender := r.get(h.InstallationID)
	if sender == nil {
		return errNoLiveConnection
	}
	return sender.respondStream(ctx, h.ReqID, h.StreamID, content, finish)
}

// streamRewrite is stream for a frame already written once — seal's retry.
// See respondStreamRewrite for why an identical frame may pass the gate an
// ordinary one may not.
func (r *sendersRegistry) streamRewrite(ctx context.Context, h streamHandle, content string, finish bool) error {
	sender := r.get(h.InstallationID)
	if sender == nil {
		return errNoLiveConnection
	}
	return sender.respondStreamRewrite(ctx, h.ReqID, h.StreamID, content, finish)
}

// recordEnding counts how a bubble ended — BOTH halves, recorded on this one
// line — from the final error of its closing frame.
//
// Every closer comes through here, via streamStore.seal: the answer, the
// failure and cancellation notices the typing indicator writes, and the
// settled flush. A closing frame the server took is a bubble that ended in
// words; one it refused sends the caller to a plain message instead, which is
// a fall-back at every call site without exception.
//
// The pair is recorded together because the pair is the signal — the ratio is
// what says whether the bubble still works at all — and a counter fed from one
// site while its twin is fed from two reads as a healthy ratio while a whole
// class of endings quietly stops using the bubble. That is what happened when
// the fall-back was counted in outbound.finishStream: the typing indicator's
// own closers never touched it, so a WeCom-side change that refused every
// closing frame would have shown up in the ratio only for answers, never for
// the failure and cancellation notices.
//
// Counted once per ending rather than once per attempt: seal may write the
// same closing frame several times when an ack is lost, and a bubble that
// took the frame on the second try ended in words all the same.
// recordOpened counts one bubble that is now on screen and owed an ending.
// Separate from recordEnding because the two are written from opposite sides:
// an ending is known here, inside the seal; an opening is only known to the
// caller that decided to keep the handle.
func (r *sendersRegistry) recordOpened() {
	r.mx().RecordStreamOpened()
}

func (r *sendersRegistry) recordEnding(err error) {
	if err == nil {
		r.mx().RecordStreamFinished()
	} else {
		r.mx().RecordStreamFellBack()
	}
}

// sendTextCtx pushes a plain message to a chat over the installation's live
// connection — the fallback every closing frame degrades to. Separate from
// stream because a message has no req_id to expire: this is the path that
// still works when the bubble is beyond saving.
func (r *sendersRegistry) sendTextCtx(ctx context.Context, id pgtype.UUID, chatID string, chatType int, content string) error {
	sender := r.get(id)
	if sender == nil {
		return errNoLiveConnection
	}
	return sender.sendTextCtx(ctx, chatID, chatType, content)
}
