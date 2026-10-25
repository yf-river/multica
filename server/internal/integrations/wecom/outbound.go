// outbound.go —— WeCom 的 EventChatDone 订阅者。agent 在总线上跑完一次会话回复
// 后，这个订阅者查出 wecom 的 chat_session 绑定、经共享注册表解析出活的
// wsSender、把回复以 aibot_send_msg 推回去。与 slack.Outbound 对齐；没有 wecom
// 绑定的会话被忽略，所以它能和共享总线上的 Slack / Lark 订阅者共存。
//
// 保持精简：aibot 没有 thread、没有按机器人的出站 REST、没有 mrkdwn 转换——回复
// 文本和 OutboundReplier 的消息走同一条 sendMsgTextBody（markdown msgtype，渲染
// 纯文本无需转义）。
//
// 副本拓扑：企微唯一的出站路径是 sendersRegistry 持有的进程内 WebSocket，而
// EventChatDone / EventInboxNew 在进程内 events.Bus 上分发——所以发布事件的副本
// 未必是握着该机器人 WS 租约的那个（Slack/Lark 免疫——它们的出站是任何副本都能
// 做的无状态 HTTP）。
//
// 在带分片 / 双副本 realtime relay 的部署里，非租约方产生的回复或收件箱推送会经
// relay 转发给租约持有方（relay_outbound.go），单副本约束不再适用于路由。没有
// relay——遗留模式、或没有 REDIS_URL——约束照旧：WeCom 启用后端必须单副本跑。
// 无论哪种模式，当没有任何副本握着活连接（全都处于重连中）时产生的投递仍会丢；
// 这个残余窗口是 relay 刻意不解决的持久化问题。启动日志会说明当前处于哪种体制。
// 见 router.go。
package wecom

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// outboundQueries 是 WeCom 出站订阅者需要的生成查询切片。*db.Queries 满足它。
type outboundQueries interface {
	// GetChannelTaskDelivery is the route this turn was admitted on, stamped
	// when the question was ingested. Read by task rather than by session
	// because /new and /clear re-point a session at a different binding: an
	// answer still in flight across one of those belongs to the room that
	// asked, not to whatever the session points at by the time it lands.
	GetChannelTaskDelivery(ctx context.Context, taskID pgtype.UUID) (db.ChannelTaskDelivery, error)
	// GetTaskChannelOrigin is the origin gate's whole question — does the task
	// row exist, and did its input arrive over a channel — in one round trip.
	// It answers what GetAgentTask followed by TaskHasChannelIngestedMessages
	// answers, and the query is a transcription of
	// engine.TaskInputIsChannelIngested rather than a second opinion on it.
	GetTaskChannelOrigin(ctx context.Context, id pgtype.UUID) (db.GetTaskChannelOriginRow, error)
	// GetAgentTask is the round matcher's, not the gate's: it resolves an
	// auto-retry clone back to the turn that owns its input batch, which is the
	// id the round was bound under. NewOutbound passes this same value as the
	// roundTaker's taskLookup.
	GetAgentTask(ctx context.Context, id pgtype.UUID) (db.AgentTaskQueue, error)
	GetChannelInstallation(ctx context.Context, arg db.GetChannelInstallationParams) (db.ChannelInstallation, error)
	// The two member lookups the inbox push picks its bot with, in the order it
	// tries them: the acting agent's own bot first, then the recipient-wide one
	// as the fallback. See memberBindingFor.
	FindChannelBindingForMemberOnAgentInstallation(ctx context.Context, arg db.FindChannelBindingForMemberOnAgentInstallationParams) (db.ChannelUserBinding, error)
	FindChannelBindingForMember(ctx context.Context, arg db.FindChannelBindingForMemberParams) (db.ChannelUserBinding, error)
	GetWorkspace(ctx context.Context, id pgtype.UUID) (db.Workspace, error)
	ListAttachmentsByChatMessage(ctx context.Context, arg db.ListAttachmentsByChatMessageParams) ([]db.Attachment, error)
}

// deliveryLookup is the pair of reads that turn a task id into the WeCom chat
// its words go to: the delivery row the engine wrote when the question was
// ingested, and the installation whose socket carries them. Shared with the
// typing indicator's failure notice, which addresses a bubble-less round the
// same way the answer does (taskAddress). *db.Queries satisfies it.
type deliveryLookup interface {
	GetChannelTaskDelivery(ctx context.Context, taskID pgtype.UUID) (db.ChannelTaskDelivery, error)
	GetChannelInstallation(ctx context.Context, arg db.GetChannelInstallationParams) (db.ChannelInstallation, error)
}

// Outbound 把 agent 的会话回复经入站循环拥有的同一条 aibot WebSocket 送回企微。
// 注册在共享事件总线上；没有 wecom 绑定的会话被静默忽略。
type Outbound struct {
	q outboundQueries
	// tasks is the retry-clone lookup behind rounds(): the same *db.Queries
	// as q, narrowed to the one row the round matcher reads.
	tasks   taskLookup
	senders *sendersRegistry
	streams *streamStore
	logger  *slog.Logger

	// objects 是部署的对象存储，没有则为 nil。非 nil 正是「打开文件投递」的开
	// 关（outbound_media.go）。
	objects mediaObjectStore

	// spawn 跑一次附件投递。做成字段而不是裸 `go`，让测试能内联运行它、确定性
	// 地观察结果。
	spawn func(func())

	// metrics 统计每条回复最后怎么了。nil 即丢弃；为什么要有丢弃细分见
	// outbound_outcome.go。
	metrics Metrics

	// relay routes a reply to the replica holding the bot's socket when this
	// one does not. Nil on a deployment with no Redis, where it is also
	// unnecessary: one replica publishes and holds the socket both.
	// noticeRouter rather than *RelayOutbound: publish is the only method used
	// here, it is the seam typing_indicator.go already routes through, and a
	// concrete type makes "was this ending routed at all" untestable — which is
	// exactly the question three gaps hid behind.
	relay noticeRouter

	// 两个计数器约束附件投递，是两个而不是一个，因为一个数没法同时待在两个地
	// 方。
	//
	// admittedAttachments 统计本订阅者已启动、还没看到返回的 goroutine。它在
	// spawn 之前占位，所以它既约束每个 goroutine 要跑的附件查询、也约束 goroutine
	// 本身。此刻对那一轮还一无所知，超出它只能记日志。
	//
	// pendingAttachments 统计「已查过那一轮、发现有文件」的投递。它在查询之后占
	// 位，正因如此，一个因容量不足被拒的投递可以在从未对「一个根本不存在的文件」
	// 报警的前提下上报给用户。
	//
	// admitted 上限刻意取两者中较大那个，这样一批「确实带文件」的积压会先填满
	// pending 上限、并在能说明丢了什么的那条路径上被拒。顶到 admitted 上限并不
	// 意味着 pending 满了：admission 贯穿 goroutine 的整个生命周期——包括它的查
	// 询、包括那些查完发现根本没文件的轮次——而那些从不占 pending 槽。
	pendingMu           sync.Mutex
	pendingAttachments  int
	admittedAttachments int
}

// NewOutbound 构造 WeCom 出站订阅者。senders 是和 wecom.ChannelDeps、
// OutboundReplier 用同一张进程级注册表——回复投递走「绑定所属安装的活 wsSender」，
// 所以 Supervisor 中途丢了租约的会话会静默丢弃，而不是另开第二条连接。
//
// streams is the same store the typing indicator writes to; nil disables the
// in-place reply and leaves every answer going out as a new message.
//
// WithAttachments is the one option that changes what can be delivered: pass
// the deployment's object storage and the files an agent produced are
// delivered into the chat behind the answer.
func NewOutbound(q outboundQueries, senders *sendersRegistry, streams *streamStore, logger *slog.Logger, opts ...OutboundOption) *Outbound {
	if logger == nil {
		logger = slog.Default()
	}
	o := &Outbound{
		q:       q,
		tasks:   q,
		senders: senders,
		streams: streams,
		logger:  logger,
		spawn:   func(f func()) { go f() },
	}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// Register 在总线上订阅会话完成事件。
func (o *Outbound) Register(bus *events.Bus) {
	bus.Subscribe(protocol.EventChatDone, o.handleEvent)
	// task:failed and task:cancelled are NOT subscribed here, and that is the
	// one place this adapter deliberately departs from #7952 as merged.
	//
	// A run that ends has a bubble waiting on it, and only the typing
	// indicator can seal that bubble — sending the notice from here would
	// leave the bubble spinning beside it, and subscribing in both places
	// would put two messages in the chat for one failure. So the indicator
	// owns every ending (typing_indicator.go), and it carries the same words
	// #7952 established: the platform's own redacted reason when there is
	// one, through taskFailedContent below.
	// Inbox notifications delivered through the smart bot: when the
	// recipient member has a WeCom binding with a live connection, their
	// inbox:new items are pushed to the aibot as a markdown card.
	bus.Subscribe(protocol.EventInboxNew, o.handleInboxNew)
}

func (o *Outbound) handleEvent(e events.Event) {
	// 总线投递是同步的——一次卡住的 WS 写绝不能楔死发布调用点。用带紧凑超时的
	// 新 ctx，和 Slack 一样。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// 「未投递的回复」只在一个人手里记录，所以一次丢弃被恰好计一次、且总带原
	// 因。processEvent 里那些「以自己的错误结束一轮」的分支各自记录并返回 nil；
	// 浮到这里的都由错误分类而来。
	if err := o.processEvent(ctx, e); err != nil {
		if reason := unconfirmedReason(err); reason != "" {
			o.unconfirmed(ctx, e, reason, err)
		} else {
			o.dropped(ctx, e, classifyDrop(err), err)
		}
	}
}

// answerOutcome is what deliverAnswer reports back about a turn's words, for
// the two decisions its caller still has to make once they are gone.
type answerOutcome struct {
	// addr is where the words went, or the zero value when none did. It is the
	// address the files that follow are sent to.
	addr roundAddress

	// spoke says words reached the user from THIS process. It is what the
	// attachment path needs to know whether the files are the whole answer:
	// a sealed bubble already put text on the screen, so files failing behind
	// it are a file problem, not a reply this adapter owed and lost.
	spoke bool

	// routed says the turn was handed to the replica holding the socket. That
	// replica sends the files too (relayFrame.CarriesFiles), so this one must
	// not, or the user gets each attachment twice.
	routed bool
}

func (o *Outbound) processEvent(ctx context.Context, e events.Event) error {
	sessionID, err := util.ParseUUID(e.ChatSessionID)
	if err != nil || !sessionID.Valid {
		// Issue / autopilot 类任务不带 chat_session。
		return nil
	}
	// chat:done is the only event this subscriber takes — the typing indicator
	// owns task:failed, because only it can seal the bubble that turn opened,
	// and two subscribers would answer the same failure twice. So the reply
	// text comes straight off the chat:done payload rather than through
	// deliverableContent, which exists to serve both.
	content := chatDoneContent(e.Payload)

	// An empty completion does NOT end the turn here, and that is the one
	// place this path has to differ from main. A bubble is already on the
	// asker's screen, and returning now would leave it turning until the
	// server's window runs out. deliverAnswer seals it with words instead —
	// merged, no reply, or no reply but files — and files still go out
	// underneath. The nothing_to_say exit lives there, where it can tell the
	// two cases apart.
	// Only bound, non-empty completions reach here, so classify the task
	// origin before loading credentials or sending. A question asked in the
	// Multica web UI can reuse a session that originated in WeCom — and its
	// answer belongs only in Multica. Without this gate that answer is pushed
	// into the WeCom chat, which in a group means in front of everyone in the
	// room. slack/outbound.go:118 and the lark and dingtalk equivalents all
	// gate here; WeCom was the one that did not.
	//
	// 失败即关闭：无法确证来源的不投递。
	//
	// 上面这一切都是读操作。请保持这样：闸门必须始终赶在「任何消费或改写本轮的
	// WeCom 侧状态」之前，因为一条不能进房间的答案，也不能接管房间的消息位置。
	taskID, ok := chatDoneTaskID(e)
	if !ok {
		o.dropped(ctx, e, dropTaskMissing, nil)
		return nil
	}
	// One keyed read, not two: GetTaskChannelOrigin answers what GetAgentTask
	// followed by engine.TaskInputIsChannelIngested answers, and answers it the
	// same way — including for a task whose input batch has no owner, which is
	// channel-ingested. This read is synchronous on the completion response
	// (see the query's comment), so the round trip it saves is one the daemon
	// waits for.
	origin, err := o.q.GetTaskChannelOrigin(ctx, taskID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 它的完成还在路上时被取消并删除。
			o.dropped(ctx, e, dropTaskMissing, nil)
			return nil
		}
		// Recorded rather than returned. The caller would file a context error
		// as an unconfirmed delivery — unconfirmedReason maps it to
		// "interrupted", which tells an operator the user may ALREADY HAVE this
		// reply and a resend would duplicate it. Nothing was written to a
		// socket here and nothing was going to be: this gate is upstream of
		// every send. The honest record is the one dropTransport describes.
		o.dropped(ctx, e, dropTransport, err)
		return nil
	}
	if !origin.ChannelIngested {
		// Give the bubble back. A run typed in Multica can hold the room's
		// round — it is bound off task:queued, and a chat task's event carries
		// nothing that tells the two apart — so returning here without
		// releasing leaves the round bound to a run that will never close it:
		// the asker watches the bubble turn until the platform ends it, and
		// their own answer finds no round and degrades to a plain message.
		// The same release the failure and cancellation gates perform.
		if o.streams != nil {
			o.streams.releaseRound(sessionID, taskIDFromEvent(e))
		}
		o.skipped(ctx, e, skipOriginNotChannel)
		return nil
	}

	// Whether the agent produced files for this turn, decided before the seal
	// because what the seal has to do depends on it. Everything it reads is
	// already in hand, so a deployment with no storage costs no query.
	carriesFiles := o.mayCarryAttachments(e)

	// The take removes the round under the store's lock, which is what makes
	// two closers racing for one run produce one closing frame. Whether it
	// found a bubble is all deliverAnswer needs to know: the bubble is a cache,
	// and a round with none goes down the plain path.
	t, _ := o.rounds().take(ctx, sessionID, byTask(taskIDFromEvent(e)))
	said, err := o.deliverAnswer(ctx, e, taskID, t, content, carriesFiles, origin.BatchOwnerUnknown)
	if errors.Is(err, errOutcomeRecorded) {
		// The branch that tried to speak has already filed its own outcome
		// through recordSend. Counting it here as well is the double count
		// this sentinel exists to make impossible.
		return nil
	}
	if errors.Is(err, errNothingToSay) {
		// Counted by whichever branch declined to speak — each one names its
		// own reason, and a blanket count here would file a revoked
		// installation and a silent agent under the same label.
		return nil
	}
	if err != nil {
		return err
	}
	if said.routed {
		// The replica holding the socket owns the rest of this turn, files
		// included — relayFrame.CarriesFiles is how it knows. Sending them from
		// here as well would put every attachment in the chat twice. A
		// published frame is owed an outcome by the relay, which records the
		// drop itself if nobody takes it (RelayOutbound.watchOutcomes).
		return nil
	}
	// Then whatever the agent produced alongside the words, as its own message —
	// a WeCom reply cannot carry a file inline. It goes wherever the answer just
	// went, bubble or plain message, which is the one address this turn has
	// established belongs to the room that asked. An address the round never
	// learned is a no-op inside deliverAttachments.
	//
	// carriesTheReply is true only when nothing has been shown to the user yet,
	// which makes the files the whole answer and their outcome the reply's. A
	// sealed bubble or a sent message has already settled that, so this is read
	// off what deliverAnswer did rather than off the completion being empty:
	// an empty completion under a bubble was answered in words all the same.
	o.deliverAttachments(e, attachmentTarget{
		InstallationID: said.addr.InstallationID,
		ChatID:         said.addr.ChatID,
		ChatType:       said.addr.ChatType,
		SessionID:      e.ChatSessionID,
		// Resolved here rather than at the failure, which happens on a detached
		// goroutine with no context left to read a profile with. In a 1:1 the
		// bound chatid IS the reader's userid, which is what localeFor wants; a
		// room ignores it and reads the deployment's language (language.go).
		Locale: deploymentLocale(),
	}, !said.spoke)
	return nil
}

// deliverAnswer writes an agent's answer wherever this round can still be
// reached, in the order the user would rather have it.
//
// The bubble comes first: the round opened one when the question arrived and
// the whole point of the feature is that the answer replaces it in place.
// Everything else is an ordinary message to the chat the task's delivery row
// names.
//
// Nothing here re-asks where the question came from. processEvent has already
// refused every run that is not this room's, which is what makes it safe for
// this function to write without asking.
// ownerUnknown is the origin gate's second fact, carried rather than re-read:
// it decides the LOG LEVEL on a turn that turns out to have no delivery row,
// and nothing else. See sendAsMessage.
func (o *Outbound) deliverAnswer(ctx context.Context, e events.Event, taskID pgtype.UUID, t roundTurn, content string, carriesFiles, ownerUnknown bool) (answerOutcome, error) {
	if t.HasBubble {
		// A bubble on screen has to end in words. An empty completion is a
		// legitimate outcome — the agent had nothing to add — but an endless
		// spinner is not, so the copy stands in for the silence. The round's own
		// language, captured when its bubble was opened. And when the agent said
		// nothing but produced files, the silence is not the end of the turn at
		// all: those files arrive as their own messages right underneath, so a
		// bubble reading "nothing to reply this round" would contradict the next
		// thing on screen.
		//
		// A ROUND THAT WAITED IN LINE SAYS THE SAME THING AS ONE THAT DID NOT.
		// "已并入上一条回复" reads as a merge, and nothing here merged anything:
		// QueuedBehind records only that another round was open when this one
		// was painted, which is not evidence that the reply ahead covered this
		// message. A real merged notice needs a real merge signal. Falling
		// through to StreamNoReply drops the claim without adding state, and
		// it puts the attachment case back in charge when files are carried.
		text := content
		if !hasVisibleChar(text) {
			c := copyFor(t.Handle.Locale)
			switch {
			case carriesFiles:
				text = c.StreamNoReplyWithFiles
			default:
				text = c.StreamNoReply
			}
		}
		sealErr := o.finishStream(ctx, t.Handle, text)
		switch classifySeal(sealErr) {
		case sealOnScreen:
			o.delivered()
			return answerOutcome{addr: t.Handle.address(), spoke: true}, nil
		case sealUnknown:
			o.unconfirmed(ctx, e, unconfirmedSealReason(sealErr), sealErr)
			return answerOutcome{addr: t.Handle.address(), spoke: true}, nil
		}
		// Proof the words are not on screen: say them as a message, on a budget
		// the seal cannot have already spent. On main the answer always had the
		// full budget for its own message; falling back must not cost it that.
		content = text
		var cancel context.CancelFunc
		ctx, cancel = fallbackBudget(ctx)
		defer cancel()
	}
	if !hasVisibleChar(content) {
		// No bubble to close and nothing to say. That is the end of it: a
		// completion with no words and no file was never a message, and no
		// bubble is waiting on one.
		if !carriesFiles {
			// NOTHING TO SAY IS STILL AN ENDING, and it still has to reach the
			// bubble. Reaching here means no round was found ON THIS REPLICA;
			// off-lease the round is on a sibling, and returning silently left
			// it turning for the rest of the protocol's window over a turn that
			// had already finished.
			o.relaySeal(e, taskID, sealReasonNoReply, false)
			o.skipped(ctx, e, skipNothingToSay)
			return answerOutcome{}, errNothingToSay
		}
		// The agent said nothing but produced files, and those still have to
		// reach the room. sendAsMessage is where the delivery row names it; with
		// no words to carry it sends none, and returns the address the files go
		// to.
	}
	return o.sendAsMessage(ctx, e, taskID, content, carriesFiles, ownerUnknown)
}

// taskAddress resolves the chat a turn's words go to, off the task's own
// delivery row.
//
// Three outcomes, read off the two results together. An address that is
// known() is where to write. A zero address with no skip reason is a turn this
// adapter was never going to answer — no delivery row, or another platform's
// row — which is the silent, ordinary case on a bus every channel publishes
// to. A zero address WITH a reason is a turn that was ours until the
// installation was revoked, and that one is worth counting.
//
// The address comes off channel_task_delivery rather than off the session's
// current binding: /new and /clear re-point a session, and an answer produced
// across one of those belongs to the room that asked. The bubble path is
// already routed that way — its handle carries the address the question came in
// on — so both paths of this adapter answer where they were asked.
func taskAddress(ctx context.Context, q deliveryLookup, taskID pgtype.UUID) (roundAddress, skipReason, bool, error) {
	delivery, err := q.GetChannelTaskDelivery(ctx, taskID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// NO ROW AT ALL IS NOT THE SAME ANSWER as a row naming another
			// platform, and the third return is what keeps them apart. A run
			// typed in Multica has no row by design — EnqueueChatTask writes no
			// external delivery snapshot — so "no row" is the one answer that
			// leaves the origin still open, and the only one a caller should
			// spend further reads on.
			return roundAddress{}, "", false, nil
		}
		return roundAddress{}, "", false, fmt.Errorf("wecom: lookup task delivery: %w", err)
	}
	if delivery.ChannelType != channelTypeWecom {
		return roundAddress{}, "", true, nil
	}
	binding := wecomBindingFromTaskDelivery(delivery)
	inst, err := q.GetChannelInstallation(ctx, db.GetChannelInstallationParams{
		ID:          binding.InstallationID,
		ChannelType: channelTypeWecom,
	})
	if err != nil {
		return roundAddress{}, "", true, fmt.Errorf("wecom: load installation: %w", err)
	}
	if inst.Status != string(InstallationActive) {
		// Revoked between trigger and reply.
		return roundAddress{}, skipInstallationInactive, true, nil
	}
	return roundAddress{
		InstallationID: inst.ID,
		ChatID:         binding.ChannelChatID,
		ChatType:       aibotChatTypeFromChannel(channel.ChatType(binding.ChatType)),
	}, "", true, nil
}

// routeFrame hands a frame to the relay and answers what that produced. A
// caller with no second route reads it through relayPublish.routed; the inbox
// push reads all four states, because only it can fall back to another bot.
//
// THE NIL CHECK IS HERE BECAUSE THE FIELD IS AN INTERFACE. It used to be a
// *RelayOutbound, whose publish begins with `if r == nil`, so a call through a
// nil pointer was safe and the call sites relied on that without saying so.
// A nil interface has no receiver to run that guard, so widening the field
// silently removed the safety three call sites were standing on. One place to
// check it, so the next call site cannot forget.
func (o *Outbound) routeFrame(f relayFrame, eventID string) relayPublish {
	if o.relay == nil {
		return relayNotWired
	}
	return o.relay.publish(f, eventID)
}

// routeFrameAwaiting is routeFrame plus the acceptance wait: it asks the relay
// whether a replica actually took the frame, bounded by ctx. Only the inbox
// push asks, and only for the attempt that has a second route behind it -- see
// RelayOutbound.publishAwaitingHolder for why that distinction is worth a
// second entry point.
func (o *Outbound) routeFrameAwaiting(ctx context.Context, f relayFrame, eventID string) relayPublish {
	if o.relay == nil {
		return relayNotWired
	}
	return o.relay.publishAwaitingHolder(ctx, f, eventID)
}

// relaySeal asks whichever replica holds this round to close it. Routed by
// round ownership, so it needs no address — see deliverRelayed.
func (o *Outbound) relaySeal(e events.Event, taskID pgtype.UUID, reason string, carriesFiles bool) {
	id := util.UUIDToString(taskID)
	if id == "" {
		return
	}
	o.routeFrame(relayFrame{
		Kind:         relayKindSeal,
		SealReason:   reason,
		TaskID:       id,
		SessionID:    e.ChatSessionID,
		CarriesFiles: carriesFiles,
	}, id)
}

// sendAsMessage pushes an answer to the chat this turn was admitted on, for a
// round with no bubble left to put it in — a restart mid-run, a stream past its
// window, a frame the server refused. It returns where it spoke, which is where
// the files that follow go.
func (o *Outbound) sendAsMessage(ctx context.Context, e events.Event, taskID pgtype.UUID, content string, carriesFiles, ownerUnknown bool) (answerOutcome, error) {
	addr, skip, hadRow, err := taskAddress(ctx, o.q, taskID)
	if err != nil {
		return answerOutcome{}, err
	}
	if skip != "" {
		o.skippedFor(ctx, e.ChatSessionID, skip)
		return answerOutcome{}, errNothingToSay
	}
	if !addr.known() {
		// The two reasons to have no address are one quiet return from the
		// outside, and only one of them means somebody is waiting. Every turn
		// that reaches here is past the origin gate, so its question DID come
		// in over a channel — which is what makes a missing row worth a word
		// rather than the ordinary traffic of a shared bus. Ahead of the gate
		// this same branch also caught every question ever typed in the Multica
		// web UI, and an exit shared with those could only be silent.
		//
		// Behind the gate, a missing row means a turn the channel ingested and
		// nobody can now address. In a steady-state deployment there is no such
		// turn: the row is written inside the same transaction that enqueues a
		// channel task. What produces one is an upgrade, and the answer it
		// belongs to is going nowhere — so it is counted and warned about
		// rather than left as a quiet return. See skipNoDeliveryRow.
		switch {
		case hadRow:
			o.skippedFor(ctx, e.ChatSessionID, skipNotWecomTurn)
		case ownerUnknown:
			// The gate delivered this turn on the open side of an unanswerable
			// verdict: its input batch has no owner, so nothing here ever
			// established it was a channel's. Warning would put the loudest
			// line this adapter has on a pre-158 web turn that auto-retried.
			o.skippedFor(ctx, e.ChatSessionID, skipRouteUnattributable)
		default:
			o.skippedFor(ctx, e.ChatSessionID, skipNoDeliveryRow)
		}
		return answerOutcome{}, errNothingToSay
	}
	if o.senders == nil {
		return answerOutcome{}, errors.New("wecom: sender registry not configured")
	}
	sender := o.senders.get(addr.InstallationID)
	if sender == nil {
		// 放弃之前先试一次：这条回复可能只是「在错的副本上产生」。把它交给握着
		// socket 的那一个。
		//
		// Counted by the replica that delivers it, not here — so a reply that
		// is routed and then delivered appears once, on the sender's side.
		// A reply routed while EVERY replica is mid-reconnect is read by
		// nobody and counted by nobody; that window is the durability problem
		// this deliberately does not solve (relay_outbound.go).
		if o.routeFrame(relayFrame{
			Kind:           relayKindReply,
			InstallationID: util.UUIDToString(addr.InstallationID),
			ChatID:         addr.ChatID,
			ChatType:       addr.ChatType,
			Content:        content,
			TaskID:         util.UUIDToString(taskID),
			MessageID:      chatDoneMessageID(e.Payload),
			WorkspaceID:    e.WorkspaceID,
			SessionID:      e.ChatSessionID,
			CarriesFiles:   carriesFiles,
		}, relayEventID(e, taskID)).routed() {
			o.logger.DebugContext(ctx, "wecom outbound: routed to the replica holding the socket",
				"installation_id", util.UUIDToString(addr.InstallationID), "chat_session_id", e.ChatSessionID)
			return answerOutcome{addr: addr, routed: true}, nil
		}
		// No live WS for this installation on this replica. Two causes:
		// (1) the Supervisor lost the lease or is mid-reconnect — transient,
		// and the user's next inbound message reaches the reconnected loop;
		// (2) on a multi-replica deployment the lease is held by a DIFFERENT
		// replica than the one that published this event, so it can never be
		// delivered from here (see the single-replica constraint in this
		// file's header). Either way, buffering is wrong — the reply is stale
		// by the time a socket returns — so we surface it to the caller's WARN
		// rather than drop it silently.
		return answerOutcome{}, errNoLiveConnection
	}
	// Words first — and only when there are any. An empty completion reaches
	// here only because a file is bound to the turn, and an empty markdown
	// message ahead of that file would be noise the user has to scroll past.
	//
	// Empty is hasVisibleChar's sense of it, not `!= ""`. A completion of "\n"
	// is a bubble with nothing in it on the reader's screen, and counting it as
	// the words that answered the turn also tells the file below it that the
	// reply has already been accounted for.
	if !hasVisibleChar(content) {
		return answerOutcome{addr: addr}, nil
	}
	err = sender.sendTextCtx(ctx, addr.ChatID, addr.ChatType, content)
	// Recorded here rather than returned, so this send and the relay's go
	// through the one mapping in recordSend (#8344).
	o.recordSend(ctx, e.ChatSessionID, e.Type, err)
	if err != nil && !errors.Is(err, errPartiallySent) {
		// Nothing of the answer landed. The files are not an answer on their
		// own, so the turn ends here.
		//
		// errOutcomeRecorded, NOT err: recordSend above already filed this
		// send, and handleEvent counts every error processEvent returns. The
		// same refusal would land on the counters twice — measured, once, as
		// platform_refused = 2.
		return answerOutcome{addr: addr}, errOutcomeRecorded
	}
	// errPartiallySent still spoke: part of the answer is on the reader's
	// screen, and the files below it are not the reply.
	return answerOutcome{addr: addr, spoke: true}, nil
}

func wecomBindingFromTaskDelivery(delivery db.ChannelTaskDelivery) db.ChannelChatSessionBinding {
	return db.ChannelChatSessionBinding{
		ID: delivery.BindingID, InstallationID: delivery.InstallationID,
		ChannelType: delivery.ChannelType, ChannelChatID: delivery.ChannelChatID,
		ChatType:      delivery.ChatType,
		LastMessageID: delivery.ChannelMessageID, LastThreadID: delivery.ChannelThreadID,
		RouteRevision: delivery.RouteRevision, Config: delivery.Config,
	}
}

// taskFailedPrefix marks a failure notice apart from a reply in the chat, the
// way DingTalk's and Lark's do.
const taskFailedPrefix = "⚠️ "

// taskFailedContent is the platform's own redacted reason for a failed run —
// the same text the web transcript shows — and nothing while an auto-retry is
// pending: the retry attempt reports its own outcome, and announcing a failure
// the next attempt may undo would be noise. Empty means the failure arrived
// without a reason, and the copy pack's generic line stands in for it.
//
// The reader is the typing indicator, which owns every ending this adapter
// announces (see Register above for why the subscription is not here).
func taskFailedContent(payload any) string {
	p, ok := payload.(map[string]any)
	if !ok {
		return ""
	}
	if pending, _ := p["retry_pending"].(bool); pending {
		return ""
	}
	msg, _ := p["error"].(string)
	if strings.TrimSpace(msg) == "" {
		return ""
	}
	return taskFailedPrefix + msg
}

// rounds builds the matcher that turns a task id on an event into the round it
// belongs to — the same one the typing indicator's endings go through.
func (o *Outbound) rounds() roundTaker {
	return roundTaker{streams: o.streams, tasks: o.tasks, log: o.logger}
}

// chatDoneTaskID recovers the task id an EventChatDone belongs to, as the row
// key the origin gate needs.
//
// It reads through taskIDFromEvent rather than repeating the extraction,
// because the gate and the bubble take have to be talking about the same run:
// two rules that disagree would let the gate clear task A while the take
// consumes the round bound to task B, which is the ordering bug with an extra
// step in it. taskIDFromEvent is where that rule lives — the envelope's TaskID
// first, then the payload, since service.broadcastChatDone sets
// ChatDonePayload.TaskID and leaves the envelope's empty.
func chatDoneTaskID(e events.Event) (pgtype.UUID, bool) {
	id, err := util.ParseUUID(taskIDFromEvent(e))
	return id, err == nil && id.Valid
}

// finishStream writes the answer into the bubble and seals it, through the
// store's seal — the one place the closing frame's retry policy lives. A
// failure here is not fatal to the reply — it means the caller falls back to a
// new message — so it is logged with the one detail that explains it: whether
// the stream is beyond saving (past its window, bad req_id) or the socket
// simply blinked.
//
// Both endings are counted inside sendersRegistry.recordEnding, which every
// bubble closer goes through — this one and the typing indicator's. Counted at
// all because from outside the two are indistinguishable: the user gets the
// answer either way, and nobody reports "the bubble I was watching turned into
// a separate message". A bubble that has stopped working at all — a WeCom-side
// change to the stream frame, a req_id convention that drifted — shows up as
// stream_fell_back climbing to meet stream_finished, and nowhere else.
//
// senders and streams are non-nil here: a turn has a bubble only through them.
func (o *Outbound) finishStream(ctx context.Context, h streamHandle, text string) error {
	err := o.streams.seal(ctx, o.senders, h, text)
	if err == nil {
		return nil
	}
	o.logger.WarnContext(ctx, "wecom outbound: in-place reply failed, sending a new message instead",
		"installation_id", uuidStringPub(h.InstallationID),
		"stream_unusable", streamUnusable(err), "error", err)
	return err
}

// chatDoneContent extracts the reply text from an EventChatDone payload
// (the typed payload, or its map form after a serialization round trip).
func chatDoneContent(payload any) string {
	switch p := payload.(type) {
	case protocol.ChatDonePayload:
		return p.Content
	case map[string]any:
		if s, ok := p["content"].(string); ok {
			return s
		}
	}
	return ""
}

// handleInboxNew 是经智能机器人投递成员通知的 inbox:new 订阅者。接收成员若有带
// 活连接的 WeCom 绑定，通知就被推到 aibot。任何不中——接收者非成员、没有 wecom
// 绑定、没有活 sender、发送失败——handler 都是空操作，成员照常通过应用内收件箱
// 收到通知。
func (o *Outbound) handleInboxNew(e events.Event) {
	payload, ok := e.Payload.(map[string]any)
	if !ok {
		return
	}
	item, ok := payload["item"].(map[string]any)
	if !ok {
		return
	}
	// 只投成员——agent 不通过会话通道收任何东西。
	if rt, _ := item["recipient_type"].(string); rt != "member" {
		return
	}
	recipientIDStr, _ := item["recipient_id"].(string)
	workspaceIDStr, _ := item["workspace_id"].(string)
	if recipientIDStr == "" || workspaceIDStr == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	o.tryDeliverInbox(ctx, item, recipientIDStr, workspaceIDStr)
}

// tryDeliverInbox 是投递核心。机器人确实推了通知才返回 true。
func (o *Outbound) tryDeliverInbox(ctx context.Context, item map[string]any, recipientIDStr, workspaceIDStr string) bool {
	recipientID, err := util.ParseUUID(recipientIDStr)
	if err != nil || !recipientID.Valid {
		return false
	}
	workspaceID, err := util.ParseUUID(workspaceIDStr)
	if err != nil || !workspaceID.Valid {
		return false
	}
	binding, actingAgentBot, err := o.memberBindingFor(ctx, item, workspaceID, recipientID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			o.logger.WarnContext(ctx, "wecom outbound: lookup member binding failed",
				"error", err, "workspace_id", workspaceIDStr, "recipient_id", recipientIDStr)
		}
		return false // 无绑定 → 无经机器人可投的东西
	}
	if o.senders == nil {
		return false
	}

	// Resolve slug for the link. Best-effort — a missing slug just falls
	// back to the workspace UUID in the URL.
	// The card is a 1:1 push to a known Multica member, so their own profile
	// language decides what it says — the one surface where the reader is
	// always resolvable by construction.
	cp := copyFor(deploymentLocale())

	slug := ""
	if ws, err := o.q.GetWorkspace(ctx, workspaceID); err == nil {
		slug = ws.Slug
	}
	content := buildInboxMarkdown(item, workspaceIDStr, slug, cp)
	if content == "" {
		return false
	}

	// awaitHolder is actingAgentBot and nothing else: only the agent-attributed
	// attempt has a second route behind it, so only that one may wait to hear
	// whether nobody took the frame. A member-authored notification has no
	// fallback at all, and the fallback below has nothing behind it, so both
	// keep the relay's non-blocking publish -- the wait happens on the request's
	// own goroutine, and a verdict nobody can act on is not worth holding it for.
	delivered, provenMiss := o.pushInbox(ctx, item, binding, recipientIDStr, content, actingAgentBot)
	if delivered {
		return true
	}
	// Only the preferred-and-agent-attributed bot needs a second route: a
	// recipient-wide binding answering false has nowhere else to go, and a
	// retry is owed only when the agent's bot provably delivered nothing.
	if !provenMiss || !actingAgentBot {
		return false
	}

	// The acting agent's bot could not have delivered, so fall back to the
	// binding the recipient-wide lookup names — the route this notification
	// took before the agent's own bot was preferred. A miss here is reported the
	// way the old path reported it: no bot push, and the in-app inbox still
	// carries the notification.
	fallback, err := o.q.FindChannelBindingForMember(ctx, db.FindChannelBindingForMemberParams{
		WorkspaceID:   workspaceID,
		MulticaUserID: recipientID,
		ChannelType:   channelTypeWecom,
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			o.logger.WarnContext(ctx, "wecom outbound: fallback member binding lookup failed",
				"error", err, "workspace_id", workspaceIDStr, "recipient_id", recipientIDStr)
		}
		return false
	}
	if fallback.InstallationID == binding.InstallationID {
		// The recipient-wide answer names the same bot; sending again would only
		// repeat the miss, not route around it.
		return false
	}
	o.logger.DebugContext(ctx, "wecom outbound: inbox push falls back to the recipient-wide bot",
		"agent_installation_id", uuidStringPub(binding.InstallationID),
		"installation_id", uuidStringPub(fallback.InstallationID))
	delivered, _ = o.pushInbox(ctx, item, fallback, recipientIDStr, content, false)
	return delivered
}

// pushInbox attempts one delivery of an already-rendered inbox card through one
// binding. provenMiss is true only when the attempt PROVES the card did not
// reach WeCom at all — no live socket for the binding anywhere in the fleet, or
// a send that failed before a byte could leave — which is the one case another
// binding may be tried. A partial or uncertain send answers false, because the
// card may already be in the member's chat and a retry would duplicate it.
// awaitHolder asks the relay to report relayNoHolder, which costs the
// acceptance window and is only worth paying where the caller has a second
// route to try (see tryDeliverInbox). Everything else about the attempt is the
// same either way.
func (o *Outbound) pushInbox(ctx context.Context, item map[string]any, binding db.ChannelUserBinding, recipientIDStr, content string, awaitHolder bool) (delivered, provenMiss bool) {
	sender := o.senders.get(binding.InstallationID)
	if sender == nil {
		// No socket here. Same shape as the reply path: hand it to the replica
		// that holds one. An inbox push is as user-visible as an answer, and
		// leaving it local was the reason the single-replica constraint had to
		// stay even with replies routed.
		frame := relayFrame{
			Kind:           relayKindInbox,
			InstallationID: util.UUIDToString(binding.InstallationID),
			ChatID:         binding.ChannelUserID,
			ChatType:       chatTypeSingleInt,
			Content:        content,
		}
		eventID := relayInboxEventID(itemIDOf(item), recipientIDStr, util.UUIDToString(binding.InstallationID))
		var verdict relayPublish
		if awaitHolder {
			verdict = o.routeFrameAwaiting(ctx, frame, eventID)
		} else {
			verdict = o.routeFrame(frame, eventID)
		}
		switch verdict {
		case relayAccepted:
			o.logger.DebugContext(ctx, "wecom outbound: routed an inbox push to the replica holding the socket",
				"installation_id", uuidStringPub(binding.InstallationID))
			return true, false
		case relayUncertain:
			// The bus call failed, or the claim store could not answer. A
			// frame that may have been accepted and may already be on its way
			// to the member's chat must NOT be re-routed: a duplicate card is
			// worse than a late one.
			o.logger.WarnContext(ctx, "wecom outbound: inbox push routed but its acceptance is unknown; not re-routing",
				"installation_id", uuidStringPub(binding.InstallationID),
				"recipient_id", recipientIDStr)
			return false, false
		case relayNotWired, relayNoHolder:
			// Proven not delivered; the shared warn and the proven-miss
			// return below carry it out.
		}
		// 记日志，不计入回复计数器。那些计数器的文档单位是 AGENT REPLIES，把一条
		// 收件箱通知记在那里会显示成「本适配器欠某人一条回复没送达」——与
		// deliverRelayed 里中继收件箱路径已避免的同一个单位错误，也正是 delivered/
		// dropped 比例能当作结局来读的原因。成员仍会在应用内收件箱收到它，这让一
		// 次未送达的机器人推送成为降级而非损失。
		o.logger.WarnContext(ctx, "wecom outbound: inbox push not delivered and not routable",
			"installation_id", uuidStringPub(binding.InstallationID),
			"recipient_id", recipientIDStr)
		// No replica holds a socket for this bot, so the push provably did not
		// reach WeCom and the caller may offer it to the recipient-wide binding.
		return false, true
	}
	if err := sender.sendTextCtx(ctx, binding.ChannelUserID, chatTypeSingleInt, content); err != nil {
		o.logger.WarnContext(ctx, "wecom outbound: inbox push failed",
			"error", err, "installation_id", uuidStringPub(binding.InstallationID),
			"recipient_id", recipientIDStr)
		// Only a failure that proves nothing left the process may be tried on
		// another bot; anything past the write may already be in the chat.
		return false, provablyNotSent(err)
	}
	o.logger.DebugContext(ctx, "wecom outbound: inbox delivered via bot",
		"installation_id", uuidStringPub(binding.InstallationID),
		"recipient_id", recipientIDStr,
		"inbox_type", item["type"])
	return true, false
}

// memberBindingFor resolves which of the recipient's WeCom bots carries this
// notification, and reports whether that bot is the acting agent's own.
//
// The recipient-wide lookup is deliberately no longer the only answer.
// FindChannelBindingForMember keys on the RECIPIENT and on nothing else, so in
// a multi-bot org — one bot per agent, one person bound to several of them —
// the member reads every agent's activity through whichever bot they bound
// most recently, and the bot↔agent mapping they set up disappears.
// When the notification names an AGENT as its actor, the bot that agent owns
// is the one the recipient talks to about that agent's work, so the agent's
// bot is tried first, through the recipient's binding ON it.
//
// The recipient-wide lookup answers when the agent's bot has no binding for
// this member (and always for a member-authored notification, which names no
// agent). tryDeliverInbox additionally re-tries the recipient-wide binding when
// the agent's bot provably delivered nothing, so preferring the agent's bot
// narrows WHERE a push is carried rather than WHETHER it is carried.
func (o *Outbound) memberBindingFor(ctx context.Context, item map[string]any, workspaceID, recipientID pgtype.UUID) (db.ChannelUserBinding, bool, error) {
	if agentID, ok := inboxActorAgent(item); ok {
		binding, err := o.q.FindChannelBindingForMemberOnAgentInstallation(ctx, db.FindChannelBindingForMemberOnAgentInstallationParams{
			WorkspaceID:   workspaceID,
			MulticaUserID: recipientID,
			ChannelType:   channelTypeWecom,
			AgentID:       agentID,
		})
		switch {
		case err == nil:
			o.logger.DebugContext(ctx, "wecom outbound: inbox push follows the acting agent's bot",
				"agent_id", uuidStringPub(agentID),
				"installation_id", uuidStringPub(binding.InstallationID))
			return binding, true, nil
		case !errors.Is(err, pgx.ErrNoRows):
			// A read failure must not silence a notification the fallback can
			// still deliver: the fallback's own error is the one the caller
			// reports and counts, and it is the same query the old path made.
			o.logger.WarnContext(ctx, "wecom outbound: agent-bot binding lookup failed",
				"error", err, "agent_id", uuidStringPub(agentID))
		}
	}
	binding, err := o.q.FindChannelBindingForMember(ctx, db.FindChannelBindingForMemberParams{
		WorkspaceID:   workspaceID,
		MulticaUserID: recipientID,
		ChannelType:   channelTypeWecom,
	})
	return binding, false, err
}

// inboxActorAgent reads the agent an inbox notification names as its actor,
// when it names one. The payload is the map inboxItemToResponse builds, whose
// actor fields are pointers (util.TextToPtr / util.UUIDToPtr) and nil for a
// notification a member authored. Both spellings are accepted, the same
// defensive shape itemIDOf uses, so a payload that was re-encoded on its way
// here reads like a native one.
func inboxActorAgent(item map[string]any) (pgtype.UUID, bool) {
	if inboxStringField(item["actor_type"]) != "agent" {
		return pgtype.UUID{}, false
	}
	id := inboxStringField(item["actor_id"])
	if id == "" {
		return pgtype.UUID{}, false
	}
	agentID, err := util.ParseUUID(id)
	if err != nil || !agentID.Valid {
		return pgtype.UUID{}, false
	}
	return agentID, true
}

// inboxStringField reads one payload string that may arrive plain or behind a
// pointer — which is how the two publishers of an inbox item spell the actor
// fields. Anything else, including a nil pointer, reads as absent.
func inboxStringField(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case *string:
		if s != nil {
			return *s
		}
	case pgtype.Text:
		if s.Valid {
			return s.String
		}
	}
	return ""
}

// uuidStringPub 给日志行渲染 pgtype.UUID，而不依赖 engine.uuidString（那是另一个
// 包）。
func uuidStringPub(u pgtype.UUID) string {
	return util.UUIDToString(u)
}

// installationCheck is what one permission read established. Three outcomes,
// because collapsing them is how the first version of this gate lost answers:
// a lookup that failed and a row that says revoked are the same refusal for THIS
// attempt and opposite facts about every later one.
type installationCheck int

const (
	// installationOK — active. Write.
	installationOK installationCheck = iota
	// installationGone — revoked, or the row is not there. Final: nobody may
	// write to it again, and an answer held for it is not owed to anyone.
	installationGone
	// installationUnreadable — the read itself failed. Nothing is established.
	// Refuse this attempt either way; what differs is what the caller owes
	// afterwards. An answer must NOT be reported finished — it is still the only
	// copy and still this subscriber's to deliver. An attachment has nowhere to
	// report to and nothing to lose by waiting: the file stays in object storage
	// and the user can ask again, so the delivery simply stops.
	installationUnreadable
)

// String names the outcomes for the log, so a line saying a delivery stopped
// also says whether anything is coming back — gone is final, unreadable is this
// read and no more. Same reason deliveryState carries one (outbound_media.go).
func (c installationCheck) String() string {
	switch c {
	case installationOK:
		return "ok"
	case installationGone:
		return "gone"
	case installationUnreadable:
		return "unreadable"
	default:
		return "invalid"
	}
}

// mayStillWrite reads the installation's permission for one attempt.
//
// The three-way split follows the convention the rest of this file already
// keeps: pgx.ErrNoRows is a fact, every other error is a failed question. The
// gate that preceded this returned a bool and answered "no" to both, so a
// database blip on one attempt confirmed a one-shot chat:done as handled and
// threw the answer away.
func (o *Outbound) mayStillWrite(ctx context.Context, id pgtype.UUID) installationCheck {
	if !id.Valid {
		return installationGone
	}
	inst, err := o.q.GetChannelInstallation(ctx, db.GetChannelInstallationParams{
		ID:          id,
		ChannelType: channelTypeWecom,
	})
	switch {
	case err == nil:
	case errors.Is(err, pgx.ErrNoRows):
		return installationGone
	default:
		return installationUnreadable
	}
	if inst.Status != string(InstallationActive) {
		return installationGone
	}
	return installationOK
}
